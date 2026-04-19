package service

import (
	"encoding/json"
	"fmt"
	"log"
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

func StartAuditWorkflow(changeID string) error {
	var change model.Change
	if err := model.DB.Where("id = ?", changeID).First(&change).Error; err != nil {
		return fmt.Errorf("change not found: %w", err)
	}

	var task model.Task
	if change.TaskID != nil {
		model.DB.Where("id = ?", *change.TaskID).First(&task)
	}

	agentName := "unknown"
	var a model.Agent
	if model.DB.Where("id = ?", change.AgentID).First(&a).Error == nil {
		agentName = a.Name
	}

	var modifiedFiles []string
	json.Unmarshal([]byte(change.ModifiedFiles), &modifiedFiles)
	var newFiles []string
	json.Unmarshal([]byte(change.NewFiles), &newFiles)
	var deletedFiles []string
	json.Unmarshal([]byte(change.DeletedFiles), &deletedFiles)

	direction, _ := repo.GetContentBlock(change.ProjectID, "direction")
	milestone, _ := repo.GetCurrentMilestone(change.ProjectID)

	directionContent := ""
	if direction != nil {
		directionContent = direction.Content
	}
	milestoneContent := ""
	if milestone != nil {
		milestoneContent = milestone.Name
	}

	ctx := &agent.SessionContext{
		DirectionBlock:  directionContent,
		MilestoneBlock:  milestoneContent,
		AgentName:       agentName,
		ChangeInfo: &agent.ChangeContext{
			ChangeID:      change.ID,
			TaskName:      task.Name,
			TaskDesc:      task.Description,
			AgentName:     agentName,
			ModifiedFiles: modifiedFiles,
			NewFiles:      newFiles,
			DeletedFiles:  deletedFiles,
			Diff:          change.Diff,
		},
	}

	session := agent.DefaultManager.CreateSession(agent.RoleAudit1, change.ProjectID, ctx, "change_submitted")
	session.ChangeID = changeID

	log.Printf("[Audit] Created session %s for change %s, role=audit_1", session.ID, changeID)

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
	}

	GitAddAndCommit(change.ProjectID, taskName, taskDesc)

	newVersion, _ := IncrementVersion(change.ProjectID)

	BroadcastEvent(change.ProjectID, "VERSION_UPDATE", map[string]interface{}{
		"block_type": "version",
		"content":    newVersion,
		"reason":     "change approved",
		"change_id":  change.ID,
	})

	BroadcastEvent(change.ProjectID, "AUDIT_RESULT", map[string]interface{}{
		"change_id":   change.ID,
		"result":      "approved",
		"audit_level": change.AuditLevel,
		"new_version": newVersion,
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

	case "L1":
		change.Status = "pending_fix"
		change.AuditLevel = &result.Level
		change.AuditReason = fmt.Sprintf("L1 issues: %d issues found", len(result.Issues))
		model.DB.Save(&change)

		issuesJSON, _ := json.Marshal(result.Issues)

		ctx := &agent.SessionContext{
			ChangeInfo: &agent.ChangeContext{
				ChangeID:      change.ID,
				AuditIssues:   string(issuesJSON),
				ModifiedFiles: mustUnjson(change.ModifiedFiles),
				NewFiles:      mustUnjson(change.NewFiles),
				DeletedFiles:  mustUnjson(change.DeletedFiles),
				Diff:          change.Diff,
			},
		}
		session := agent.DefaultManager.CreateSession(agent.RoleFix, change.ProjectID, ctx, "fix_needed")
		session.ChangeID = changeID

		log.Printf("[Audit] Change %s needs fix (L1), session %s", changeID, session.ID)

	case "L2":
		change.Status = "rejected"
		change.AuditLevel = &result.Level
		change.AuditReason = result.RejectReason
		model.DB.Save(&change)

		BroadcastEvent(change.ProjectID, "AUDIT_RESULT", map[string]interface{}{
			"change_id":     change.ID,
			"result":       "rejected",
			"audit_level":   result.Level,
			"reject_reason": result.RejectReason,
		})

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
		direction, _ := repo.GetContentBlock(change.ProjectID, "direction")
		milestone, _ := repo.GetCurrentMilestone(change.ProjectID)

		directionContent := ""
		if direction != nil {
			directionContent = direction.Content
		}
		milestoneContent := ""
		if milestone != nil {
			milestoneContent = milestone.Name
		}

		ctx := &agent.SessionContext{
			DirectionBlock: directionContent,
			MilestoneBlock:  milestoneContent,
			ChangeInfo: &agent.ChangeContext{
				ChangeID:      change.ID,
				ModifiedFiles: mustUnjson(change.ModifiedFiles),
				NewFiles:      mustUnjson(change.NewFiles),
				DeletedFiles:  mustUnjson(change.DeletedFiles),
				Diff:          change.Diff,
			},
		}
		session := agent.DefaultManager.CreateSession(agent.RoleAudit2, change.ProjectID, ctx, "re_audit")
		session.ChangeID = changeID
		log.Printf("[Audit] Change %s delegated to audit_2, session %s", changeID, session.ID)

	case "reject":
		change.Status = "rejected"
		now := time.Now()
		change.ReviewedAt = &now
		change.AuditReason = result.RejectReason
		model.DB.Save(&change)

		BroadcastEvent(change.ProjectID, "AUDIT_RESULT", map[string]interface{}{
			"change_id":      change.ID,
			"result":        "rejected",
			"reject_reason": result.RejectReason,
		})
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

		BroadcastEvent(change.ProjectID, "AUDIT_RESULT", map[string]interface{}{
			"change_id":      change.ID,
			"result":         result.Result,
			"reject_reason":  result.RejectReason,
		})
	}

	log.Printf("[Audit] Change %s final decision: %s", changeID, result.Result)
	return nil
}

func mustUnjson(s string) []string {
	var result []string
	json.Unmarshal([]byte(s), &result)
	return result
}