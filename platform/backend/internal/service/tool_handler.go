package service

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

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

	case "evaluate_output":
		if err := HandleEvaluateOutput(sessionID, projectID, args); err != nil {
			log.Printf("[ToolHandler] evaluate_output error: %v", err)
		}

	case "merge_output":
		if err := HandleMergeOutput(sessionID, projectID, args); err != nil {
			log.Printf("[ToolHandler] merge_output error: %v", err)
		}

	default:
		log.Printf("[ToolHandler] Unknown tool: %s", toolName)
	}
}
