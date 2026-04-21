package service

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
)

type AuditResult struct {
	Level        string        `json:"level"`
	Issues       []AuditIssue  `json:"issues,omitempty"`
	RejectReason string        `json:"reject_reason,omitempty"`
}

type AuditIssue struct {
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
	Type   string `json:"type"`
	Detail string `json:"detail"`
	Status string `json:"status"`
}

type FixResult struct {
	Action       string `json:"action"`
	Fixed        bool   `json:"fixed"`
	DelegateTo   string `json:"delegate_to,omitempty"`
	RejectReason string `json:"reject_reason,omitempty"`
}

type Audit2Result struct {
	Result       string `json:"result"`
	RejectReason string `json:"reject_reason,omitempty"`
}

// pendingChanges tracks change IDs waiting for audit completion
var pendingChanges = make(map[string]chan *AuditCompletion)
var pendingChangesMutex sync.RWMutex

type AuditCompletion struct {
	Status      string
	AuditLevel  string
	AuditReason string
}

func StartAuditWorkflowAndWait(changeID string, timeout time.Duration) (*AuditCompletion, error) {
	// Create completion channel
	done := make(chan *AuditCompletion, 1)
	pendingChangesMutex.Lock()
	pendingChanges[changeID] = done
	pendingChangesMutex.Unlock()

	defer func() {
		pendingChangesMutex.Lock()
		delete(pendingChanges, changeID)
		pendingChangesMutex.Unlock()
	}()

	// Start audit workflow
	if err := StartAuditWorkflow(changeID); err != nil {
		return nil, err
	}

	// Wait for completion or timeout
	select {
	case result := <-done:
		return result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("audit timeout")
	}
}

func notifyAuditCompletion(changeID string, status, level, reason string) {
	pendingChangesMutex.RLock()
	done, ok := pendingChanges[changeID]
	pendingChangesMutex.RUnlock()

	if ok {
		done <- &AuditCompletion{
			Status:      status,
			AuditLevel:  level,
			AuditReason: reason,
		}
	}
}

func StartAuditWorkflow(changeID string) error {
	var change model.Change
	if err := model.DB.Where("id = ?", changeID).First(&change).Error; err != nil {
		return fmt.Errorf("change not found: %w", err)
	}

	ctx := BuildChangeContext(&change)

	session := agent.DefaultManager.CreateSession(agent.RoleAudit1, change.ProjectID, ctx, "change_submitted")
	session.ChangeID = changeID

	log.Printf("[Audit] Created session %s for change %s, role=audit_1", session.ID, changeID)

	agent.DispatchSession(session)

	return nil
}

func approveChange(change *model.Change) error {
	change.Status = "approved"
	now := time.Now()
	change.ReviewedAt = &now
	model.DB.Save(change)

	var task model.Task
	taskName := ""
	taskDesc := ""
	if change.TaskID != nil {
		model.DB.Where("id = ?", *change.TaskID).First(&task)
		taskName = task.Name
		taskDesc = task.Description
		// Auto-complete task when change is approved
		task.Status = "completed"
		task.CompletedAt = &now
		model.DB.Save(&task)
		log.Printf("[Audit] Task %s auto-completed (change approved)", task.ID)
	}

	GitAddAndCommit(change.ProjectID, taskName, taskDesc)

	newVersion, _ := IncrementVersion(change.ProjectID)

	BroadcastEvent(change.ProjectID, "VERSION_UPDATE", map[string]interface{}{
		"block_type": "version",
		"content":    newVersion,
		"reason":     "change approved",
		"change_id":  change.ID,
	})

	log.Printf("[Audit] Change %s approved, version %s", change.ID, newVersion)
	return nil
}

func ProcessAuditOutput(changeID string, result *AuditResult) error {
	var change model.Change
	if err := model.DB.Where("id = ?", changeID).First(&change).Error; err != nil {
		return fmt.Errorf("change not found: %w", err)
	}

	now := time.Now()
	change.ReviewedAt = &now

	switch result.Level {
	case "L0":
		if err := approveChange(&change); err != nil {
			return err
		}
		change.AuditLevel = &result.Level
		model.DB.Save(&change)
		notifyAuditCompletion(changeID, "approved", "L0", "")

	case "L1":
		change.Status = "pending_fix"
		change.AuditLevel = &result.Level
		change.AuditReason = fmt.Sprintf("L1 issues: %d issues found", len(result.Issues))
		// Auto-label FailureMode based on issue types
		if failureMode := classifyFailureMode(result.Issues); failureMode != "" {
			change.FailureMode = failureMode
		}
		model.DB.Save(&change)
		notifyAuditCompletion(changeID, "pending_fix", "L1", change.AuditReason)

		issuesJSON, _ := json.Marshal(result.Issues)

		ctx := BuildChangeContext(&change)
		ctx.ChangeInfo.AuditIssues = string(issuesJSON)

		session := agent.DefaultManager.CreateSession(agent.RoleFix, change.ProjectID, ctx, "fix_needed")
		session.ChangeID = changeID

		log.Printf("[Audit] Change %s needs fix (L1), session %s", changeID, session.ID)

		agent.DispatchSession(session)

	case "L2":
		change.Status = "rejected"
		change.AuditLevel = &result.Level
		change.AuditReason = result.RejectReason
		model.DB.Save(&change)

		// Start 10-minute timer for task reclamation (requires heartbeat or resubmit)
		go func() {
			taskID := *change.TaskID
			agentID := change.AgentID
			for i := 0; i < 10; i++ {
				time.Sleep(1 * time.Minute)
				// Check for heartbeat or new submission each minute
				var a model.Agent
				if model.DB.Where("id = ?", agentID).First(&a).Error == nil {
					lastHeartbeat := a.LastHeartbeat
					if lastHeartbeat != nil && time.Since(*lastHeartbeat) < 2*time.Minute {
						continue // Agent is active, keep waiting
					}
				}
				// Check for new change submission from same agent for same task
				var newerChange model.Change
				if model.DB.Where("task_id = ? AND agent_id = ? AND created_at > ?", taskID, agentID, change.CreatedAt).First(&newerChange).Error == nil {
					continue // Agent resubmitted, keep waiting
				}
			}
			// 10 minutes passed without heartbeat or resubmit, reset task
			var task model.Task
			if err := model.DB.Where("id = ?", taskID).First(&task).Error; err == nil {
				if task.Status == "claimed" && task.AssigneeID != nil && *task.AssigneeID == agentID {
					task.Status = "pending"
					task.AssigneeID = nil
					model.DB.Save(&task)
					log.Printf("[Audit] Task %s reset to pending: no heartbeat/resubmit for 10 minutes after rejection", task.ID)
				}
			}
		}()

		notifyAuditCompletion(changeID, "rejected", "L2", result.RejectReason)

		log.Printf("[Audit] Change %s rejected (L2)", changeID)
	}

	return nil
}

func ProcessFixOutput(changeID string, result *FixResult) error {
	var change model.Change
	if err := model.DB.Where("id = ?", changeID).First(&change).Error; err != nil {
		return fmt.Errorf("change not found: %w", err)
	}

	switch result.Action {
	case "fix":
		if err := approveChange(&change); err != nil {
			return err
		}
		log.Printf("[Audit] Change %s fixed and approved", changeID)

	case "delegate":
		ctx := BuildChangeContext(&change)
		session := agent.DefaultManager.CreateSession(agent.RoleAudit2, change.ProjectID, ctx, "re_audit")
		session.ChangeID = changeID
		log.Printf("[Audit] Change %s delegated to audit_2, session %s", changeID, session.ID)

		agent.DispatchSession(session)

	case "reject":
		change.Status = "rejected"
		now := time.Now()
		change.ReviewedAt = &now
		change.AuditReason = result.RejectReason
		model.DB.Save(&change)

		log.Printf("[Audit] Change %s rejected by fix agent", changeID)
	}

	return nil
}

func ProcessAudit2Output(changeID string, result *Audit2Result) error {
	var change model.Change
	if err := model.DB.Where("id = ?", changeID).First(&change).Error; err != nil {
		return fmt.Errorf("change not found: %w", err)
	}

	now := time.Now()
	change.ReviewedAt = &now

	if result.Result == "merge" {
		if err := approveChange(&change); err != nil {
			return err
		}
	} else {
		change.Status = "rejected"
		change.AuditReason = result.RejectReason
		model.DB.Save(&change)
	}

	log.Printf("[Audit] Change %s final decision: %s", changeID, result.Result)
	return nil
}

func mustUnjson(s string) []string {
	var result []string
	json.Unmarshal([]byte(s), &result)
	return result
}

// BuildChangeContext constructs a SessionContext for a change, including direction/milestone/task/agent info
func BuildChangeContext(change *model.Change) *agent.SessionContext {
	direction, _ := repo.GetContentBlock(change.ProjectID, "direction")
	milestone, _ := repo.GetCurrentMilestone(change.ProjectID)

	directionContent := ""
	if direction != nil {
		directionContent = direction.Content
	}
	milestoneContent := ""
	if milestone != nil {
		milestoneContent = milestone.Name + "\n" + milestone.Description
	}

	var task model.Task
	taskName := ""
	taskDesc := ""
	if change.TaskID != nil {
		model.DB.Where("id = ?", *change.TaskID).First(&task)
		taskName = task.Name
		taskDesc = task.Description
	}

	agentName := "unknown"
	var a model.Agent
	if model.DB.Where("id = ?", change.AgentID).First(&a).Error == nil {
		agentName = a.Name
	}

	return &agent.SessionContext{
		DirectionBlock: directionContent,
		MilestoneBlock: milestoneContent,
		AgentName:      agentName,
		ChangeInfo: &agent.ChangeContext{
			ChangeID:      change.ID,
			TaskName:      taskName,
			TaskDesc:      taskDesc,
			AgentName:     agentName,
			ModifiedFiles: mustUnjson(change.ModifiedFiles),
			NewFiles:      mustUnjson(change.NewFiles),
			DeletedFiles:  mustUnjson(change.DeletedFiles),
			Diff:          change.Diff,
		},
	}
}

// classifyFailureMode maps audit issue types to a failure mode label.
// Returns the first matching mode, or empty string if no known type found.
func classifyFailureMode(issues []AuditIssue) string {
	typeToMode := map[string]string{
		"wrong_assumption": "wrong_assumption",
		"missing_context":  "missing_context",
		"tool_misuse":      "tool_misuse",
		"over_edit":        "over_edit",
		"invalid_output":   "invalid_output",
	}
	for _, issue := range issues {
		if mode, ok := typeToMode[issue.Type]; ok {
			return mode
		}
	}
	return ""
}