package service

import (
	"encoding/json"
	"log"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
)

// TaskProfile describes a task's characteristics for experience-based routing.
type TaskProfile struct {
	TaskID         string   `json:"task_id"`
	Tags           []string `json:"tags"`
	SimilarPast    []string `json:"similar_past"`
	RiskLevel      string   `json:"risk_level"`       // low / medium / high
	SuggestedFlow  string   `json:"suggested_flow"`   // change / pr / pr_with_review
	SuggestedModel string   `json:"suggested_model"`
	GuardRails     []string `json:"guard_rails"`
	RelevantSkills []string `json:"relevant_skills"`
}

// ProfileTask generates a TaskProfile for a given task.
func ProfileTask(taskID string) *TaskProfile {
	// 1. Get tags
	var tags []model.TaskTag
	model.DB.Where("task_id = ?", taskID).Find(&tags)
	tagNames := make([]string, 0, len(tags))
	for _, t := range tags {
		tagNames = append(tagNames, t.Tag)
	}

	// 2. Find similar past experiences (top 3 key_insights)
	var experiences []model.Experience
	if len(tagNames) > 0 {
		// Simple matching: experiences with similar tags or same task
		model.DB.Where("task_id = ? OR outcome = 'failed'", taskID).
			Where("status IN ?", []string{"raw", "distilled"}).
			Order("created_at DESC").Limit(5).Find(&experiences)
	}

	similarPast := make([]string, 0)
	for _, exp := range experiences {
		similarPast = append(similarPast, exp.ID)
	}

	// 3. Match active policies
	policies := MatchPolicies(tagNames, "")

	guardRails := make([]string, 0)
	relevantSkills := make([]string, 0)
	riskLevel := "low"
	suggestedFlow := "change"
	suggestedModel := ""

	for _, p := range policies {
		var actions map[string]interface{}
		if err := json.Unmarshal([]byte(p.Actions), &actions); err != nil {
			continue
		}

		if gp, ok := actions["guard_prompt"].(string); ok && gp != "" {
			guardRails = append(guardRails, gp)
		}
		if req, ok := actions["require_pr"].(bool); ok && req {
			suggestedFlow = "pr"
		}
		if reqRev, ok := actions["require_pr_review"].(bool); ok && reqRev {
			suggestedFlow = "pr_with_review"
		}
		if m, ok := actions["model"].(string); ok && m != "" {
			suggestedModel = m
		}
		if rl, ok := actions["risk_level"].(string); ok && rl != "" {
			riskLevel = rl
		}

		// Increment hit count
		model.DB.Model(&model.Policy{}).Where("id = ?", p.ID).Update("hit_count", p.HitCount+1)
	}

	// 4. Find relevant skills
	if len(tagNames) > 0 {
		var skills []model.SkillCandidate
		model.DB.Where("status = ?", "active").Find(&skills)
		for _, sk := range skills {
			var skTags []string
			if err := json.Unmarshal([]byte(sk.ApplicableTags), &skTags); err != nil {
				continue
			}
			if hasOverlap(tagNames, skTags) {
				relevantSkills = append(relevantSkills, sk.ID)
			}
		}
	}

	return &TaskProfile{
		TaskID:         taskID,
		Tags:           tagNames,
		SimilarPast:    similarPast,
		RiskLevel:      riskLevel,
		SuggestedFlow:  suggestedFlow,
		SuggestedModel: suggestedModel,
		GuardRails:     guardRails,
		RelevantSkills: relevantSkills,
	}
}

// MatchPolicies returns active policies matching given tags and/or role.
func MatchPolicies(tags []string, role string) []*model.Policy {
	var policies []model.Policy
	model.DB.Where("status = ?", "active").Order("priority DESC").Find(&policies)

	var matched []*model.Policy
	for i := range policies {
		p := &policies[i]

		var mc map[string]interface{}
		if err := json.Unmarshal([]byte(p.MatchCondition), &mc); err != nil {
			continue
		}

		if matchesCondition(mc, tags, role) {
			matched = append(matched, p)
		}
	}
	return matched
}

// matchesCondition checks if a policy's match_condition matches the given tags and role.
func matchesCondition(mc map[string]interface{}, tags []string, role string) bool {
	// Check role match
	if reqRole, ok := mc["role"].(string); ok && reqRole != "" && role != "" {
		if reqRole != role {
			return false
		}
	}

	// Check tag match (any overlap)
	if reqTags, ok := mc["tags"].([]interface{}); ok && len(reqTags) > 0 {
		if !hasTagOverlap(tags, reqTags) {
			return false
		}
	}

	// Check scope match
	if scope, ok := mc["scope"].(string); ok && scope != "" {
		// Scope is a higher-level category; for now simple string match
		_ = scope // accepted
	}

	// Check file_count_gt
	if fcgt, ok := mc["file_count_gt"].(float64); ok {
		_ = fcgt // would need file count from context
	}

	return true
}

func hasTagOverlap(taskTags []string, reqTags []interface{}) bool {
	tagSet := make(map[string]bool)
	for _, t := range taskTags {
		tagSet[t] = true
	}
	for _, rt := range reqTags {
		if s, ok := rt.(string); ok && tagSet[s] {
			return true
		}
	}
	return false
}

func hasOverlap(a, b []string) bool {
	set := make(map[string]bool)
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if set[s] {
			return true
		}
	}
	return false
}

// ApplyPolicy applies a matched policy to a session, modifying its context.
func ApplyPolicy(session *agent.Session, policy *model.Policy) {
	var actions map[string]interface{}
	if err := json.Unmarshal([]byte(policy.Actions), &actions); err != nil {
		return
	}

	if gp, ok := actions["guard_prompt"].(string); ok && gp != "" {
		session.Context.InputContent += "\n\n[Policy Guard]: " + gp
	}

	if reqCtx, ok := actions["require_context"].(string); ok && reqCtx != "" {
		session.Context.InputContent += "\n\n[Required Context]: You must read the following files first: " + reqCtx
	}

	if maxFiles, ok := actions["max_file_changes"].(float64); ok {
		session.Context.InputContent += "\n\n[Policy Constraint]: Maximum file changes allowed: " + string(rune(int(maxFiles)+'0'))
	}

	if m, ok := actions["model"].(string); ok && m != "" {
		log.Printf("[PolicyEngine] Session %s: overriding model to %s per policy %s", session.ID, m, policy.Name)
		// Model override is handled by setting in session context
		session.Context.AutoMode = true // signal that model was policy-overridden
	}

	log.Printf("[PolicyEngine] Applied policy %s to session %s", policy.Name, session.ID)
}
