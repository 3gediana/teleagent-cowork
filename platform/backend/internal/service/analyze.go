package service

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/opencode"
)

// TriggerAnalyzeAgent creates and dispatches an Analyze Agent session for a project.
func TriggerAnalyzeAgent(projectID string) {
	if opencode.DefaultScheduler == nil {
		log.Printf("[Analyze] Scheduler not initialized, skipping")
		return
	}

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

	// Serialize experience IDs for the context
	expIDs := make([]string, 0, len(rawExperiences))
	for _, exp := range rawExperiences {
		expIDs = append(expIDs, exp.ID)
	}
	expIDsJSON, _ := json.Marshal(expIDs)

	ctx := &agent.SessionContext{
		InputContent: fmt.Sprintf(`## Raw Experiences (last %d)
%s

## Current Skills
%s

## Current Policies
%s

## Statistics
- Total sessions: %d
- L0 count: %d, L1 count: %d, L2 count: %d
- Experience IDs: %s`,
			len(rawExperiences), expSummary, skillSummary, policySummary,
			totalSessions, l0Count, l1Count, l2Count, string(expIDsJSON)),
		GlobalState: fmt.Sprintf("raw_experiences=%d, skills=%d, policies=%d",
			len(rawExperiences), len(currentSkills), len(currentPolicies)),
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

	// 4. Apply tag suggestions
	if tagSuggestions, ok := args["tag_suggestions"]; ok {
		var suggestions []struct {
			TaskID        string   `json:"task_id"`
			SuggestedTags []string `json:"suggested_tags"`
		}
		if b, err := json.Marshal(tagSuggestions); err == nil {
			json.Unmarshal(b, &suggestions)
		}
		for _, ts := range suggestions {
			for _, tag := range ts.SuggestedTags {
				tt := model.TaskTag{
					ID:     model.GenerateID("ttag"),
					TaskID: ts.TaskID,
					Tag:    tag,
					Source: "analyze",
				}
				model.DB.Create(&tt)
			}
		}
		log.Printf("[Analyze] Applied %d tag suggestions", len(suggestions))
	}

	// 5. Log model suggestions (for human review)
	if modelSuggestions, ok := args["model_suggestions"]; ok {
		b, _ := json.Marshal(modelSuggestions)
		log.Printf("[Analyze] Model suggestions: %s", string(b))
	}

	return nil
}
