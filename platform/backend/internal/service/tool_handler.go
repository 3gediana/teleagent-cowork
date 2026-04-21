package service

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
)

var DataPath string

func InitDataPath(path string) {
	DataPath = filepath.Join(path, "projects")
	log.Printf("[ToolHandler] DataPath initialized: %s", DataPath)
}

func GetProjectPath(projectID string) string {
	return filepath.Join(DataPath, projectID)
}

func HandleMaintainToolCall(projectID string, toolName string, args map[string]interface{}) error {
	projectPath := GetProjectPath(projectID)

	switch toolName {
	case "create_task":
		return handleCreateTask(projectID, projectPath, args)
	case "delete_task":
		return handleDeleteTask(projectID, projectPath, args)
	case "update_milestone":
		return handleUpdateMilestone(projectPath, args)
	case "propose_direction":
		return handleProposeDirection(projectPath, args)
	case "write_milestone":
		return handleWriteMilestone(projectPath, args)
	case "assess_output":
		return handleAssessOutput(projectPath, args)
	default:
		return fmt.Errorf("unknown maintain tool: %s", toolName)
	}
}

func handleCreateTask(projectID, projectPath string, args map[string]interface{}) error {
	name, _ := args["name"].(string)
	if name == "" {
		return fmt.Errorf("task name required")
	}
	description, _ := args["description"].(string)
	priority, _ := args["priority"].(string)
	if priority == "" {
		priority = "medium"
	}

	taskID := model.GenerateID("task")

	// Write to database
	task := model.Task{
		ID:          taskID,
		ProjectID:   projectID,
		Name:        name,
		Description: description,
		Priority:    priority,
		Status:      "pending",
	}
	if err := model.DB.Create(&task).Error; err != nil {
		return fmt.Errorf("failed to create task in database: %w", err)
	}

	// Write to TASKS.md file
	tasksPath := filepath.Join(projectPath, "TASKS.md")

	var content string
	if data, err := os.ReadFile(tasksPath); err == nil {
		content = string(data)
	} else {
		content = "# Tasks\n\n## Pending\n\n(none)\n\n## In Progress\n\n(none)\n\n## Completed\n\n(none)\n"
	}

	taskLine := fmt.Sprintf("- [%s] **%s** (priority: %s)", taskID, name, priority)
	if description != "" {
		taskLine += fmt.Sprintf(" - %s", description)
	}
	taskLine += fmt.Sprintf(" _added: %s_", time.Now().Format("2006-01-02"))

	pendingSection := "## Pending"
	if idx := strings.Index(content, pendingSection); idx != -1 {
		afterPending := content[idx+len(pendingSection):]
		nextSection := strings.Index(afterPending, "\n## ")
		if nextSection == -1 {
			nextSection = len(afterPending)
		}
		pendingTasks := afterPending[:nextSection]
		if strings.Contains(pendingTasks, "(none)") {
			pendingTasks = "\n" + taskLine + "\n"
		} else {
			pendingTasks = strings.TrimSuffix(pendingTasks, "\n") + "\n" + taskLine + "\n"
		}
		content = content[:idx+len(pendingSection)] + pendingTasks + afterPending[nextSection:]
	} else {
		content += "\n" + taskLine + "\n"
	}

	if err := os.WriteFile(tasksPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write TASKS.md: %w", err)
	}

	log.Printf("[Maintain] Created task %s: %s (priority=%s)", taskID, name, priority)
	return nil
}

func handleDeleteTask(projectID, projectPath string, args map[string]interface{}) error {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return fmt.Errorf("task_id required")
	}

	// Delete from database
	model.DB.Where("id = ? AND project_id = ?", taskID, projectID).Delete(&model.Task{})

	// Delete from TASKS.md file
	tasksPath := filepath.Join(projectPath, "TASKS.md")
	data, err := os.ReadFile(tasksPath)
	if err != nil {
		return fmt.Errorf("TASKS.md not found")
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	var newLines []string
	for _, line := range lines {
		if !strings.Contains(line, "["+taskID+"]") {
			newLines = append(newLines, line)
		}
	}

	if err := os.WriteFile(tasksPath, []byte(strings.Join(newLines, "\n")), 0644); err != nil {
		return fmt.Errorf("failed to write TASKS.md: %w", err)
	}

	log.Printf("[Maintain] Deleted task %s", taskID)
	return nil
}

func handleUpdateMilestone(projectPath string, args map[string]interface{}) error {
	content, _ := args["content"].(string)
	if content == "" {
		return fmt.Errorf("content required")
	}

	milestonePath := filepath.Join(projectPath, "MILESTONE.md")
	if err := os.WriteFile(milestonePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write MILESTONE.md: %w", err)
	}

	log.Printf("[Maintain] Updated MILESTONE.md for project %s", projectPath)
	return nil
}

func handleProposeDirection(projectPath string, args map[string]interface{}) error {
	content, _ := args["content"].(string)
	if content == "" {
		return fmt.Errorf("content required")
	}

	directionPath := filepath.Join(projectPath, "DIRECTION.md")
	if err := os.WriteFile(directionPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write DIRECTION.md: %w", err)
	}

	log.Printf("[Maintain] Proposed direction for project %s", projectPath)
	return nil
}

func handleWriteMilestone(projectPath string, args map[string]interface{}) error {
	content, _ := args["content"].(string)
	if content == "" {
		return fmt.Errorf("content required")
	}

	milestonePath := filepath.Join(projectPath, "MILESTONE.md")
	if err := os.WriteFile(milestonePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write MILESTONE.md: %w", err)
	}

	log.Printf("[Maintain] Wrote MILESTONE.md for project %s", projectPath)
	return nil
}

func handleAssessOutput(projectPath string, args map[string]interface{}) error {
	content, _ := args["content"].(string)
	if content == "" {
		return fmt.Errorf("content required")
	}

	assessPath := filepath.Join(projectPath, "ASSESS_DOC.md")
	if err := os.WriteFile(assessPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write ASSESS_DOC.md: %w", err)
	}

	log.Printf("[Assess] Wrote ASSESS_DOC.md for project %s", projectPath)
	return nil
}

func HandleToolCallResult(sessionID string, changeID string, projectID string, toolName string, args map[string]interface{}) {
	switch toolName {
	case "audit_output":
		level, _ := args["level"].(string)
		var issues []AuditIssue
		if raw, ok := args["issues"]; ok {
			if b, err := json.Marshal(raw); err == nil {
				json.Unmarshal(b, &issues)
			}
		}
		rejectReason, _ := args["reject_reason"].(string)
		result := &AuditResult{
			Level:        level,
			Issues:       make([]AuditIssue, 0, len(issues)),
			RejectReason: rejectReason,
		}
		for _, issue := range issues {
			result.Issues = append(result.Issues, AuditIssue{
				File:   issue.File,
				Line:   issue.Line,
				Type:   issue.Type,
				Detail: issue.Detail,
				Status: issue.Status,
			})
		}
		if err := ProcessAuditOutput(changeID, result); err != nil {
			log.Printf("[ToolHandler] audit_output error: %v", err)
		}

		// M19: Capture audit observation as Experience
		patternObserved, _ := args["pattern_observed"].(string)
		suggestionForSubmitter, _ := args["suggestion_for_submitter"].(string)
		if patternObserved != "" || suggestionForSubmitter != "" {
			go CreateExperienceFromAudit(projectID, sessionID, "audit_1", changeID, patternObserved, suggestionForSubmitter)
		}

	case "fix_output":
		action, _ := args["action"].(string)
		fixed, _ := args["fixed"].(bool)
		delegateTo, _ := args["delegate_to"].(string)
		rejectReason, _ := args["reject_reason"].(string)
		result := &FixResult{
			Action:       action,
			Fixed:        fixed,
			DelegateTo:   delegateTo,
			RejectReason: rejectReason,
		}
		if err := ProcessFixOutput(changeID, result); err != nil {
			log.Printf("[ToolHandler] fix_output error: %v", err)
		}

		// M19: Capture fix strategy as Experience
		fixStrategy, _ := args["fix_strategy"].(string)
		falsePositive, _ := args["false_positive"].(bool)
		if fixStrategy != "" || falsePositive {
			go CreateExperienceFromFix(projectID, sessionID, changeID, fixStrategy, falsePositive)
		}

	case "audit2_output":
		resultStr, _ := args["result"].(string)
		rejectReason, _ := args["reject_reason"].(string)
		result := &Audit2Result{
			Result:        resultStr,
			RejectReason: rejectReason,
		}
		if err := ProcessAudit2Output(changeID, result); err != nil {
			log.Printf("[ToolHandler] audit2_output error: %v", err)
		}

	case "create_task", "delete_task", "update_milestone", "propose_direction", "write_milestone", "assess_output":
		if err := HandleMaintainToolCall(projectID, toolName, args); err != nil {
			log.Printf("[ToolHandler] %s error: %v", toolName, err)
		}

	case "approve_pr":
		if err := handleApprovePR(projectID, args); err != nil {
			log.Printf("[ToolHandler] approve_pr error: %v", err)
		}

	case "reject_pr":
		if err := handleRejectPR(projectID, args); err != nil {
			log.Printf("[ToolHandler] reject_pr error: %v", err)
		}

	case "switch_milestone":
		if err := handleSwitchMilestone(projectID, args); err != nil {
			log.Printf("[ToolHandler] switch_milestone error: %v", err)
		}

	case "create_policy":
		if err := handleCreatePolicy(args); err != nil {
			log.Printf("[ToolHandler] create_policy error: %v", err)
		}

	case "chief_output":
		result, _ := args["result"].(string)
		summary, _ := args["summary"].(string)
		log.Printf("[Chief] Session %s output: result=%s, summary=%s", sessionID, result, summary)
		agent.DefaultManager.UpdateSessionOutput(sessionID, summary)

	case "evaluate_output":
		if err := HandleEvaluateOutput(sessionID, projectID, args); err != nil {
			log.Printf("[ToolHandler] evaluate_output error: %v", err)
		}

		// M19: Capture eval patterns as Experience
		qualityPatterns, _ := args["quality_patterns"].(string)
		commonMistakes, _ := args["common_mistakes"].(string)
		if qualityPatterns != "" || commonMistakes != "" {
			prID := ""
			if s := agent.DefaultManager.GetSession(sessionID); s != nil {
				prID = s.PRID
			}
			go CreateExperienceFromEvaluate(projectID, sessionID, prID, qualityPatterns, commonMistakes)
		}

	case "merge_output":
		if err := HandleMergeOutput(sessionID, projectID, args); err != nil {
			log.Printf("[ToolHandler] merge_output error: %v", err)
		}

	case "biz_review_output":
		if err := HandleBizReviewOutput(sessionID, projectID, args); err != nil {
			log.Printf("[ToolHandler] biz_review_output error: %v", err)
		}

		// M19: Capture biz review rationale as Experience
		alignmentRationale, _ := args["alignment_rationale"].(string)
		if alignmentRationale != "" {
			prID := ""
			if s := agent.DefaultManager.GetSession(sessionID); s != nil {
				prID = s.PRID
			}
			go CreateExperienceFromBizReview(projectID, sessionID, prID, alignmentRationale)
		}

	case "analyze_output":
		if err := HandleAnalyzeOutput(sessionID, projectID, args); err != nil {
			log.Printf("[ToolHandler] analyze_output error: %v", err)
		}

	default:
		log.Printf("[ToolHandler] Unknown tool: %s", toolName)
	}
}

// handleApprovePR processes Chief Agent's approve_pr tool call.
func handleApprovePR(projectID string, args map[string]interface{}) error {
	prID, _ := args["pr_id"].(string)
	action, _ := args["action"].(string)
	reason, _ := args["reason"].(string)
	if prID == "" {
		return fmt.Errorf("pr_id required")
	}

	var pr model.PullRequest
	if model.DB.Where("id = ?", prID).First(&pr).Error != nil {
		return fmt.Errorf("PR not found: %s", prID)
	}

	switch action {
	case "approve_review":
		if pr.Status != "pending_human_review" {
			return fmt.Errorf("PR %s is not pending_human_review (status=%s)", prID, pr.Status)
		}
		pr.Status = "evaluating"
		model.DB.Save(&pr)
		log.Printf("[Chief] Approved review for PR %s: %s", prID, reason)

		go func() {
			if err := TriggerEvaluateAgent(&pr); err != nil {
				log.Printf("[Chief] Failed to trigger evaluate agent for PR %s: %v", prID, err)
			}
		}()

	case "approve_merge":
		if pr.Status != "pending_human_merge" {
			return fmt.Errorf("PR %s is not pending_human_merge (status=%s)", prID, pr.Status)
		}
		pr.Status = "merging"
		model.DB.Save(&pr)
		log.Printf("[Chief] Approved merge for PR %s: %s", prID, reason)

		if err := ExecuteMerge(pr.BranchID); err != nil {
			pr.Status = "merge_failed"
			model.DB.Save(&pr)
			return fmt.Errorf("merge failed: %w", err)
		}

		now := time.Now()
		pr.Status = "merged"
		pr.MergedAt = &now
		model.DB.Save(&pr)

		newVersion, _ := IncrementVersion(pr.ProjectID)
		log.Printf("[Chief] PR %s merged, new version: %s", prID, newVersion)

	default:
		return fmt.Errorf("unknown action: %s (expected approve_review or approve_merge)", action)
	}

	return nil
}

// handleRejectPR processes Chief Agent's reject_pr tool call.
func handleRejectPR(projectID string, args map[string]interface{}) error {
	prID, _ := args["pr_id"].(string)
	reason, _ := args["reason"].(string)
	if prID == "" {
		return fmt.Errorf("pr_id required")
	}

	var pr model.PullRequest
	if model.DB.Where("id = ?", prID).First(&pr).Error != nil {
		return fmt.Errorf("PR not found: %s", prID)
	}

	pr.Status = "rejected"
	model.DB.Save(&pr)
	log.Printf("[Chief] Rejected PR %s: %s", prID, reason)

	BroadcastEvent(projectID, "PR_REJECTED", map[string]interface{}{
		"pr_id":  prID,
		"reason": reason,
	})

	return nil
}

// handleSwitchMilestone processes Chief Agent's switch_milestone tool call.
func handleSwitchMilestone(projectID string, args map[string]interface{}) error {
	milestoneID, _ := args["milestone_id"].(string)
	reason, _ := args["reason"].(string)
	if milestoneID == "" {
		return fmt.Errorf("milestone_id required")
	}

	// Complete current milestone
	var current model.Milestone
	if model.DB.Where("project_id = ? AND status = 'in_progress'", projectID).First(&current).Error == nil {
		now := time.Now()
		current.Status = "completed"
		current.CompletedAt = &now
		model.DB.Save(&current)
		log.Printf("[Chief] Completed milestone %s", current.ID)
	}

	// Activate target milestone
	var target model.Milestone
	if model.DB.Where("id = ? AND project_id = ?", milestoneID, projectID).First(&target).Error != nil {
		return fmt.Errorf("milestone not found: %s", milestoneID)
	}
	target.Status = "in_progress"
	model.DB.Save(&target)
	log.Printf("[Chief] Switched to milestone %s: %s", milestoneID, reason)

	return nil
}

// handleCreatePolicy processes Chief Agent's create_policy tool call.
func handleCreatePolicy(args map[string]interface{}) error {
	name, _ := args["name"].(string)
	matchCondition, _ := args["match_condition"].(string)
	actions, _ := args["actions"].(string)
	if name == "" || matchCondition == "" || actions == "" {
		return fmt.Errorf("name, match_condition, and actions are required")
	}

	priority := 0
	if p, ok := args["priority"].(float64); ok {
		priority = int(p)
	}

	// Validate JSON
	if !json.Valid([]byte(matchCondition)) {
		return fmt.Errorf("match_condition must be valid JSON")
	}
	if !json.Valid([]byte(actions)) {
		return fmt.Errorf("actions must be valid JSON")
	}

	policy := model.Policy{
		ID:             model.GenerateID("pol"),
		Name:           name,
		MatchCondition: matchCondition,
		Actions:        actions,
		Priority:       priority,
		Status:         "active",
		Source:         "human",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := model.DB.Create(&policy).Error; err != nil {
		return fmt.Errorf("failed to create policy: %w", err)
	}

	log.Printf("[Chief] Created policy %s: %s (priority=%d)", policy.ID, name, priority)
	return nil
}
