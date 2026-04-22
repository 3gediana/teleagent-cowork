package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"gorm.io/gorm"
)

// ctxBg is a tiny indirection so tests can override the background
// context used by Analyze's selector call (e.g. inject a cancelled ctx
// to exercise graceful degradation).
var ctxBg = func() context.Context { return context.Background() }

// buildPendingTagsBlock renders the project's oldest `limit` proposed
// TaskTag rows as an indented, per-tag markdown list for Analyze to see.
// Joining to Task so Analyze knows which task each tag belongs to
// (otherwise it can't judge correctness against the real execution
// outcome).
//
// Returns "(none)" when there's nothing to review, keeping the prompt
// concise instead of emitting a stray header.
func buildPendingTagsBlock(projectID string, limit int) string {
	type row struct {
		TagID      string
		TagDim     string
		TagValue   string
		Confidence float64
		Source     string
		TaskID     string
		TaskName   string
	}
	var rows []row
	// Single JOIN query — proposed tags + their parent tasks in the
	// given project. Order by created_at asc so the oldest (riskiest
	// to leave pending) surface first.
	model.DB.Raw(`
		SELECT t.id AS tag_id, t.dimension AS tag_dim, t.tag AS tag_value,
		       t.confidence AS confidence, t.source AS source,
		       tk.id AS task_id, tk.name AS task_name
		FROM task_tag t
		JOIN task tk ON tk.id = t.task_id
		WHERE t.status = 'proposed' AND tk.project_id = ?
		ORDER BY t.created_at ASC
		LIMIT ?
	`, projectID, limit).Scan(&rows)

	if len(rows) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf(
			"- tag_id=%s  [%s] %s  conf=%.2f source=%s  on task %q (%s)",
			r.TagID, r.TagDim, r.TagValue, r.Confidence, r.Source,
			r.TaskName, r.TaskID,
		))
	}
	return strings.Join(parts, "\n")
}

// buildAnalyzeQueryText summarises the batch of raw experiences Analyze
// is about to distill. The names (task_id + outcome tag) form a compact
// topic signal for the bge encoder — enough to pull related patterns
// without bloating the query beyond the model's useful range.
func buildAnalyzeQueryText(exps []model.Experience) string {
	if len(exps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(exps))
	for i, e := range exps {
		if i >= 20 {
			break // bge-zh handles ~512 tokens; keep query concise
		}
		// TaskID + outcome is the most discriminative pair for recall.
		parts = append(parts, fmt.Sprintf("%s %s", e.TaskID, e.Outcome))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// TriggerAnalyzeAgent creates and dispatches an Analyze Agent session for a project.
func TriggerAnalyzeAgent(projectID string) {
	// Gather raw experiences for context
	var rawExperiences []model.Experience
	model.DB.Where("project_id = ? AND status = ?", projectID, "raw").
		Order("created_at DESC").Limit(100).Find(&rawExperiences)

	if len(rawExperiences) == 0 {
		log.Printf("[Analyze] No raw experiences for project %s, skipping", projectID)
		return
	}

	// Gather current skills and policies for context
	var currentSkills []model.SkillCandidate
	model.DB.Where("status IN ?", []string{"active", "candidate"}).Find(&currentSkills)

	var currentPolicies []model.Policy
	model.DB.Where("status = ?", "active").Find(&currentPolicies)

	// Gather statistics
	var totalSessions int64
	model.DB.Model(&model.AgentSession{}).Where("project_id = ?", projectID).Count(&totalSessions)

	var l0Count, l1Count, l2Count int64
	model.DB.Model(&model.AgentSession{}).Where("project_id = ? AND trigger_reason = ?", projectID, "change_submitted").Count(&l0Count)
	model.DB.Model(&model.AgentSession{}).Where("project_id = ? AND status = ?", projectID, "pending_fix").Count(&l1Count)
	model.DB.Model(&model.AgentSession{}).Where("project_id = ? AND status = ?", projectID, "rejected").Count(&l2Count)

	// Build experience summary for prompt
	expSummary := ""
	for _, exp := range rawExperiences {
		expSummary += fmt.Sprintf("\n- [%s] %s task=%s outcome=%s", exp.SourceType, exp.AgentRole, exp.TaskID, exp.Outcome)
		if exp.KeyInsight != "" {
			expSummary += fmt.Sprintf("\n  insight: %s", exp.KeyInsight)
		}
		if exp.Pitfalls != "" {
			expSummary += fmt.Sprintf("\n  pitfalls: %s", exp.Pitfalls)
		}
		if exp.DoDifferently != "" {
			expSummary += fmt.Sprintf("\n  do_differently: %s", exp.DoDifferently)
		}
		if exp.PatternObserved != "" {
			expSummary += fmt.Sprintf("\n  pattern: %s", exp.PatternObserved)
		}
		if exp.FixStrategy != "" {
			expSummary += fmt.Sprintf("\n  fix_strategy: %s", exp.FixStrategy)
		}
	}

	skillSummary := ""
	for _, sk := range currentSkills {
		skillSummary += fmt.Sprintf("\n- [%s] %s (%s): %s", sk.Status, sk.Name, sk.Type, sk.Action)
	}

	policySummary := ""
	for _, p := range currentPolicies {
		policySummary += fmt.Sprintf("\n- [%s] %s: match=%s actions=%s", p.Status, p.Name, p.MatchCondition, p.Actions)
	}

	// Refinery knowledge artifacts via the scoped selector. Analyze gets the
	// "analyzer" budget — the full-width view over all agent-facing kinds —
	// because its job is to spot gaps/conflicts across the whole artifact
	// set, not just find a few relevant ones. Query text is the titles of
	// the raw experiences being shepherded, so semantic retrieval biases
	// toward artifacts touching the same topics.
	queryText := buildAnalyzeQueryText(rawExperiences)
	injected := SelectArtifactsForInjection(ctxBg(), ArtifactQuery{
		ProjectID: projectID,
		Audience:  AudienceAnalyzer,
		QueryText: queryText,
	})
	artifactSummary := ""
	artifactIDs := make([]string, 0, len(injected))
	for _, ia := range injected {
		a := ia.Artifact
		artifactSummary += fmt.Sprintf("\n- [%s] %s (score=%.2f via %s): %s",
			a.Kind, a.Name, ia.Score, ia.Reason, a.Summary)
		artifactIDs = append(artifactIDs, a.ID)
	}
	// Bump usage_count for each injected artifact (feedback loop for lifecycle)
	if len(artifactIDs) > 0 {
		model.DB.Model(&model.KnowledgeArtifact{}).Where("id IN ?", artifactIDs).
			Update("usage_count", gorm.Expr("usage_count + 1"))
		model.DB.Model(&model.KnowledgeArtifact{}).Where("id IN ?", artifactIDs).
			Update("last_used_at", time.Now())
	}

	// Serialize experience IDs for the context
	expIDs := make([]string, 0, len(rawExperiences))
	for _, exp := range rawExperiences {
		expIDs = append(expIDs, exp.ID)
	}
	expIDsJSON, _ := json.Marshal(expIDs)

	// Surface pending proposed tags so Analyze can issue tag_reviews
	// ("the rule said bugfix but the session turned out to be a refactor
	// — reject"). We cap to the most recent 40 so prompts stay bounded;
	// the remainder will be picked up in subsequent Analyze runs.
	pendingTagsBlock := buildPendingTagsBlock(projectID, 40)

	ctx := &agent.SessionContext{
		InputContent: fmt.Sprintf(`## Raw Experiences (last %d)
%s

## Current Skills
%s

## Current Policies
%s

## Refinery Knowledge Artifacts
%s

## Pending Proposed Tags (review via tag_reviews in analyze_output)
%s

## Statistics
- Total sessions: %d
- L0 count: %d, L1 count: %d, L2 count: %d
- Experience IDs: %s`,
			len(rawExperiences), expSummary, skillSummary, policySummary, artifactSummary,
			pendingTagsBlock,
			totalSessions, l0Count, l1Count, l2Count, string(expIDsJSON)),
		GlobalState: fmt.Sprintf("raw_experiences=%d, skills=%d, policies=%d",
			len(rawExperiences), len(currentSkills), len(currentPolicies)),
		InjectedArtifactIDs: artifactIDs,
	}

	session := agent.DefaultManager.CreateSession(agent.RoleAnalyze, projectID, ctx, "analyze_distill")
	log.Printf("[Analyze] Created session %s for project %s with %d raw experiences", session.ID, projectID, len(rawExperiences))

	agent.DispatchSession(session)
}

// HandleAnalyzeOutput processes the analyze_output tool call from the Analyze Agent.
func HandleAnalyzeOutput(sessionID, projectID string, args map[string]interface{}) error {
	// 1. Mark distilled experiences
	if distilledIDs, ok := args["distilled_experience_ids"]; ok {
		var ids []string
		if b, err := json.Marshal(distilledIDs); err == nil {
			json.Unmarshal(b, &ids)
		}
		for _, id := range ids {
			model.DB.Model(&model.Experience{}).Where("id = ?", id).Update("status", "distilled")
		}
		log.Printf("[Analyze] Marked %d experiences as distilled", len(ids))
	}

	// 2. Create skill candidates
	if candidates, ok := args["skill_candidates"]; ok {
		var skills []struct {
			Name           string   `json:"name"`
			Type           string   `json:"type"`
			ApplicableTags []string `json:"applicable_tags"`
			Precondition   string   `json:"precondition"`
			Action         string   `json:"action"`
			Prohibition    string   `json:"prohibition"`
			Evidence       string   `json:"evidence"`
		}
		if b, err := json.Marshal(candidates); err == nil {
			json.Unmarshal(b, &skills)
		}
		for _, sk := range skills {
			tagsJSON, _ := json.Marshal(sk.ApplicableTags)
			skill := model.SkillCandidate{
				ID:              model.GenerateID("skill"),
				Name:            sk.Name,
				Type:            sk.Type,
				ApplicableTags:  string(tagsJSON),
				Precondition:    sk.Precondition,
				Action:          sk.Action,
				Prohibition:     sk.Prohibition,
				Evidence:        sk.Evidence,
				SourceCaseIDs:   "[]",
				Status:          "candidate",
				Version:         1,
			}
			skill.CreatedAt = time.Now()
			skill.UpdatedAt = time.Now()
			if err := model.DB.Create(&skill).Error; err != nil {
				log.Printf("[Analyze] Failed to create skill %s: %v", sk.Name, err)
			}
		}
		log.Printf("[Analyze] Created %d skill candidates", len(skills))
	}

	// 3. Create policy suggestions
	if suggestions, ok := args["policy_suggestions"]; ok {
		var policies []struct {
			Name           string                 `json:"name"`
			MatchCondition map[string]interface{} `json:"match_condition"`
			Actions        map[string]interface{} `json:"actions"`
			Priority       int                    `json:"priority"`
		}
		if b, err := json.Marshal(suggestions); err == nil {
			json.Unmarshal(b, &policies)
		}
		for _, p := range policies {
			mcJSON, _ := json.Marshal(p.MatchCondition)
			actJSON, _ := json.Marshal(p.Actions)
			policy := model.Policy{
				ID:             model.GenerateID("pol"),
				Name:           p.Name,
				MatchCondition: string(mcJSON),
				Actions:        string(actJSON),
				Priority:       p.Priority,
				Status:         "candidate",
				Source:         "analyze",
			}
			if err := model.DB.Create(&policy).Error; err != nil {
				log.Printf("[Analyze] Failed to create policy %s: %v", p.Name, err)
			}
		}
		log.Printf("[Analyze] Created %d policy suggestions", len(policies))
	}

	// 4. Apply tag suggestions — fresh tags Analyze wants to attach
	//    based on real execution data. These land as `confirmed` with
	//    source=analyze because Analyze is a stronger-than-rule signal
	//    (it saw the actual session run). Idempotent dedup on
	//    (task_id, dimension, tag) so repeated Analyze runs don't flood
	//    the table with duplicates.
	if tagSuggestions, ok := args["tag_suggestions"]; ok {
		var suggestions []struct {
			TaskID        string   `json:"task_id"`
			SuggestedTags []string `json:"suggested_tags"`
			Dimension     string   `json:"dimension,omitempty"` // optional, defaults to "category"
		}
		if b, err := json.Marshal(tagSuggestions); err == nil {
			json.Unmarshal(b, &suggestions)
		}
		added := 0
		for _, ts := range suggestions {
			dimension := ts.Dimension
			if dimension == "" {
				dimension = "category"
			}
			for _, tag := range ts.SuggestedTags {
				// Skip if the pair already exists in any state — a
				// rejected tag should block Analyze from re-proposing
				// it just like it blocks the rule engine.
				var existing model.TaskTag
				if err := model.DB.Where("task_id = ? AND dimension = ? AND tag = ?",
					ts.TaskID, dimension, tag).First(&existing).Error; err == nil {
					continue
				}
				now := time.Now()
				tt := model.TaskTag{
					ID:         model.GenerateID("ttag"),
					TaskID:     ts.TaskID,
					Tag:        tag,
					Dimension:  dimension,
					Source:     "analyze",
					Status:     "confirmed",
					Confidence: 0.85,
					ReviewedBy: "analyze/v1",
					ReviewedAt: &now,
					CreatedAt:  now,
					UpdatedAt:  now,
				}
				if err := model.DB.Create(&tt).Error; err == nil {
					added++
				}
			}
		}
		log.Printf("[Analyze] Applied %d new tag suggestions (dedup skipped pre-existing)", added)
	}

	// 4b. Apply tag reviews — Analyze re-adjudicates RULE-proposed tags
	//     that have real execution evidence backing (or contradicting)
	//     them. Uses AnalyzeReviewTag which refuses to overwrite
	//     human-touched rows, so Analyze can never undo a manual call.
	if tagReviews, ok := args["tag_reviews"]; ok {
		var reviews []struct {
			TagID  string `json:"tag_id"`
			Action string `json:"action"` // "confirm" | "reject"
			Note   string `json:"note"`
		}
		if b, err := json.Marshal(tagReviews); err == nil {
			json.Unmarshal(b, &reviews)
		}
		applied := 0
		for _, r := range reviews {
			if err := AnalyzeReviewTag(r.TagID, r.Action, r.Note); err == nil {
				applied++
			}
		}
		log.Printf("[Analyze] Applied %d/%d tag reviews", applied, len(reviews))
	}

	// 5. Log model suggestions (for human review)
	if modelSuggestions, ok := args["model_suggestions"]; ok {
		b, _ := json.Marshal(modelSuggestions)
		log.Printf("[Analyze] Model suggestions: %s", string(b))
	}

	return nil
}
