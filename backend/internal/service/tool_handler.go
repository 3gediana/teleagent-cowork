package service

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
)

func HandleMaintainToolCall(projectID string, toolName string, args map[string]interface{}) error {
	switch toolName {
	case "create_task":
		return handleCreateTask(projectID, args)
	case "delete_task":
		return handleDeleteTask(args)
	case "update_milestone":
		return handleUpdateMilestone(projectID, args)
	case "propose_direction":
		return handleProposeDirection(projectID, args)
	case "write_milestone":
		return handleWriteMilestone(projectID, args)
	case "assess_output":
		return handleAssessOutput(projectID, args)
	default:
		return fmt.Errorf("unknown maintain tool: %s", toolName)
	}
}

func handleCreateTask(projectID string, args map[string]interface{}) error {
	name, _ := args["name"].(string)
	if name == "" {
		return fmt.Errorf("task name required")
	}
	description, _ := args["description"].(string)
	priority, _ := args["priority"].(string)
	if priority == "" {
		priority = "medium"
	}

	milestone, _ := repo.GetCurrentMilestone(projectID)
	var milestoneID *string
	if milestone != nil {
		milestoneID = &milestone.ID
	}

	task := model.Task{
		ID:          model.GenerateID("task"),
		ProjectID:   projectID,
		MilestoneID: milestoneID,
		Name:        name,
		Description: description,
		Priority:    priority,
		Status:      "pending",
		CreatedBy:   "maintain_agent",
	}

	if err := model.DB.Create(&task).Error; err != nil {
		return fmt.Errorf("failed to create task: %w", err)
	}

	log.Printf("[Maintain] Created task %s: %s (priority=%s, milestone=%s)", task.ID, name, priority, milestoneID)
	return nil
}

func handleDeleteTask(args map[string]interface{}) error {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return fmt.Errorf("task_id required")
	}

	var task model.Task
	if err := model.DB.Where("id = ?", taskID).First(&task).Error; err != nil {
		return fmt.Errorf("task not found: %s", taskID)
	}

	task.Status = "deleted"
	if err := model.DB.Save(&task).Error; err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}

	log.Printf("[Maintain] Deleted task %s", taskID)
	return nil
}

func handleUpdateMilestone(projectID string, args map[string]interface{}) error {
	content, _ := args["content"].(string)
	if content == "" {
		return fmt.Errorf("content required")
	}

	var cb model.ContentBlock
	result := model.DB.Where("project_id = ? AND block_type = 'milestone'", projectID).First(&cb)
	if result.Error != nil {
		cb = model.ContentBlock{
			ID:        model.GenerateID("cb"),
			ProjectID: projectID,
			BlockType: "milestone",
			Content:   content,
			Version:   1,
		}
		model.DB.Create(&cb)
	} else {
		cb.Content = content
		cb.Version++
		model.DB.Save(&cb)
	}

	log.Printf("[Maintain] Updated milestone block for project %s", projectID)
	return nil
}

func handleProposeDirection(projectID string, args map[string]interface{}) error {
	content, _ := args["content"].(string)
	if content == "" {
		return fmt.Errorf("content required")
	}

	var cb model.ContentBlock
	result := model.DB.Where("project_id = ? AND block_type = 'direction'", projectID).First(&cb)
	if result.Error != nil {
		cb = model.ContentBlock{
			ID:        model.GenerateID("cb"),
			ProjectID: projectID,
			BlockType: "direction",
			Content:   content,
			Version:   1,
		}
		model.DB.Create(&cb)
	} else {
		cb.Content = content
		cb.Version++
		model.DB.Save(&cb)
	}

	log.Printf("[Maintain] Proposed direction for project %s", projectID)
	return nil
}

func handleWriteMilestone(projectID string, args map[string]interface{}) error {
	content, _ := args["content"].(string)
	if content == "" {
		return fmt.Errorf("content required")
	}

	var cb model.ContentBlock
	result := model.DB.Where("project_id = ? AND block_type = 'milestone'", projectID).First(&cb)
	if result.Error != nil {
		cb = model.ContentBlock{
			ID:        model.GenerateID("cb"),
			ProjectID: projectID,
			BlockType: "milestone",
			Content:   content,
			Version:   1,
		}
		model.DB.Create(&cb)
	} else {
		cb.Content = content
		cb.Version++
		model.DB.Save(&cb)
	}

	log.Printf("[Maintain] Wrote milestone block for project %s", projectID)
	return nil
}

func handleAssessOutput(projectID string, args map[string]interface{}) error {
	content, _ := args["content"].(string)
	if content == "" {
		return fmt.Errorf("content required")
	}

	project, _ := repo.GetProjectByID(projectID)
	if project == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}

	repoPath := filepath.Join("data", "projects", projectID, "repo")
	assessPath := filepath.Join(repoPath, "ASSESS_DOC.md")

	if err := os.MkdirAll(filepath.Dir(assessPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	if err := os.WriteFile(assessPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write ASSESS_DOC.md: %w", err)
	}

	var cb model.ContentBlock
	result := model.DB.Where("project_id = ? AND block_type = 'milestone'", projectID).First(&cb)
	if result.Error == nil {
		cb.Content = fmt.Sprintf("# Milestone: Project Assessment\n\n## Assessment\n\nASSESS_DOC.md has been generated.\n\n%s", content)
		cb.Version++
		model.DB.Save(&cb)
	}

	log.Printf("[Assess] Wrote ASSESS_DOC.md for project %s (%d bytes)", projectID, len(content))
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

	default:
		log.Printf("[ToolHandler] Unknown tool: %s", toolName)
	}
}
