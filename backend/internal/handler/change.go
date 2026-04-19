package handler

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

type ChangeHandler struct{}

func NewChangeHandler() *ChangeHandler {
	return &ChangeHandler{}
}

type SubmitChangeRequest struct {
	TaskID      string   `json:"task_id"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Writes      []model.ChangeFileEntry `json:"writes"`
	Deletes     []string `json:"deletes"`
}

func (h *ChangeHandler) Submit(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	projectID := c.Query("project_id")

	var req SubmitChangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if req.TaskID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "task_id is required"}})
		return
	}

	if len(req.Writes) == 0 && len(req.Deletes) == 0 {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "NO_FILES", "message": "No files to submit"}})
		return
	}

	var task model.Task
	if err := model.DB.Where("id = ? AND status = 'claimed'", req.TaskID).First(&task).Error; err != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "TASK_NOT_FOUND", "message": "Task not found or not claimed"}})
		return
	}

	if task.AssigneeID == nil || *task.AssigneeID != agentID.(string) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "TASK_NOT_CLAIMED_BY_YOU", "message": "Task not claimed by you"}})
		return
	}

	versionBlock, _ := repo.GetContentBlock(projectID, "version")
	currentVersion := "v1.0"
	if versionBlock != nil {
		currentVersion = versionBlock.Content
	}

	if req.Version != "" && req.Version != currentVersion {
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "VERSION_OUTDATED", "message": "Version conflict", "current_version": currentVersion}})
		return
	}

	modifiedFiles, _ := json.Marshal(req.Writes)
	deletedFiles, _ := json.Marshal(req.Deletes)

	change := model.Change{
		ID:            model.GenerateID("chg"),
		ProjectID:     projectID,
		AgentID:       agentID.(string),
		TaskID:        &req.TaskID,
		Version:       currentVersion,
		ModifiedFiles: string(modifiedFiles),
		NewFiles:      "[]",
		DeletedFiles:  string(deletedFiles),
		Diff:          "{}",
		Description:   req.Description,
		Status:        "pending",
	}

	if err := model.DB.Create(&change).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to create change"}})
		return
	}

	service.BroadcastEvent(projectID, "AUDIT_RESULT", gin.H{
		"change_id": change.ID,
		"agent":     agentID.(string),
		"status":    "pending",
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"change_id": change.ID,
			"message":   "Submitted, waiting for audit result",
		},
	})
}

func (h *ChangeHandler) List(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	status := c.Query("status")
	query := model.DB.Where("project_id = ?", projectID)
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var changes []model.Change
	query.Order("created_at desc").Find(&changes)

	result := make([]gin.H, 0, len(changes))
	for _, ch := range changes {
		item := gin.H{
			"id":         ch.ID,
			"task_id":    ch.TaskID,
			"agent_id":   ch.AgentID,
			"version":    ch.Version,
			"description": ch.Description,
			"status":     ch.Status,
			"created_at": ch.CreatedAt,
		}
		if ch.AuditLevel != nil {
			item["audit_level"] = *ch.AuditLevel
		}
		if ch.ReviewedAt != nil {
			item["reviewed_at"] = *ch.ReviewedAt
		}
		result = append(result, item)
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"changes": result}})
}

func (h *ChangeHandler) Review(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	var req struct {
		ChangeID string `json:"change_id" binding:"required"`
		Level    string `json:"level"`
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	var change model.Change
	if err := model.DB.Where("id = ?", req.ChangeID).First(&change).Error; err != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "CHANGE_NOT_FOUND", "message": "Change not found"}})
		return
	}

	if change.Status != "pending" {
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "CHANGE_SUBMIT_FAILED", "message": "Change already reviewed"}})
		return
	}

	now := time.Now()
	change.ReviewedAt = &now
	change.AuditReason = req.Reason
	change.AuditLevel = &req.Level

	if req.Approved {
		change.Status = "approved"
	} else {
		change.Status = "rejected"
	}
	model.DB.Save(&change)

	reason := req.Reason
	if req.Approved {
		reason = ""
	}
	service.BroadcastEvent(change.ProjectID, "AUDIT_RESULT", gin.H{
		"change_id":     change.ID,
		"agent":         agentID.(string),
		"result":        change.Status,
		"audit_level":   req.Level,
		"reject_reason": reason,
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"change_id": change.ID,
			"status":    change.Status,
		},
	})
}