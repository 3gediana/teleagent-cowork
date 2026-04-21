package handler

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
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
	TaskID      string                `json:"task_id"`
	Description string                `json:"description"`
	Version     string                `json:"version"`
	Writes      []model.ChangeFileEntry `json:"writes"`
	Deletes     []string              `json:"deletes"`
}

func (h *ChangeHandler) Submit(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	projectID := c.Query("project_id")

	// Branch auto-routing: if agent is on a branch, use branch logic transparently
	var agent model.Agent
	if model.DB.Where("id = ?", agentID).First(&agent).Error == nil && agent.CurrentBranchID != nil {
		h.submitOnBranch(c, agentID.(string), *agent.CurrentBranchID)
		return
	}

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

	changeID := model.GenerateID("chg")
	pendingDir := filepath.Join(service.DataPath, "..", "pending", projectID, changeID)
	os.MkdirAll(pendingDir, 0755)

	modifiedFiles := make([]model.ChangeFileEntry, 0)
	newFiles := make([]string, 0)
	repoPath := filepath.Join(service.DataPath, projectID, "repo")

	for _, w := range req.Writes {
		if w.Content == "" {
			content, err := os.ReadFile(filepath.Join(repoPath, w.Path))
			if err == nil {
				w.Content = string(content)
			}
		}

		fullPath := filepath.Join(pendingDir, w.Path)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		os.WriteFile(fullPath, []byte(w.Content), 0644)

		existingPath := filepath.Join(repoPath, w.Path)
		if _, err := os.Stat(existingPath); os.IsNotExist(err) {
			newFiles = append(newFiles, w.Path)
		} else {
			modifiedFiles = append(modifiedFiles, w)
		}
	}

	for _, d := range req.Deletes {
		pendingDelPath := filepath.Join(pendingDir, d+".deleted")
		os.WriteFile(pendingDelPath, []byte{}, 0644)
	}

	// Build diff content for audit. Distinguish "new" (file did not exist
	// before) from "modified" (file existed and content changed). Previously
	// all writes were tagged "new", misleading the audit agent.
	newFileSet := make(map[string]bool, len(newFiles))
	for _, f := range newFiles {
		newFileSet[f] = true
	}
	diffMap := make(map[string]interface{})
	for _, w := range req.Writes {
		if w.Content == "" {
			continue
		}
		status := "modified"
		if newFileSet[w.Path] {
			status = "new"
		}
		diffMap[w.Path] = map[string]interface{}{
			"status":  status,
			"content": w.Content,
		}
	}
	for _, d := range req.Deletes {
		diffMap[d] = map[string]interface{}{
			"status": "deleted",
		}
	}
	diffJSON, _ := json.Marshal(diffMap)

	modifiedFilesJSON, _ := json.Marshal(modifiedFiles)
	newFilesJSON, _ := json.Marshal(newFiles)
	deletedFilesJSON, _ := json.Marshal(req.Deletes)

	// Check project auto_mode
	var project model.Project
	autoMode := true // default on
	if err := model.DB.Where("id = ?", projectID).First(&project).Error; err == nil {
		autoMode = project.AutoMode
	}

	changeStatus := "pending"
	if !autoMode {
		changeStatus = "pending_human_confirm"
	}

	// Check if this is a retry (same task has previous rejected change)
	retryCount := 0
	var prevChanges []model.Change
	if model.DB.Where("task_id = ? AND status IN ?", req.TaskID, []string{"rejected", "pending_fix"}).Find(&prevChanges); len(prevChanges) > 0 {
		retryCount = len(prevChanges)
	}

	change := model.Change{
		ID:            changeID,
		ProjectID:     projectID,
		AgentID:       agentID.(string),
		TaskID:        &req.TaskID,
		Version:       currentVersion,
		ModifiedFiles: string(modifiedFilesJSON),
		NewFiles:      string(newFilesJSON),
		DeletedFiles:  string(deletedFilesJSON),
		Diff:          string(diffJSON),
		Description:   req.Description,
		Status:        changeStatus,
		RetryCount:    retryCount,
	}

	if err := model.DB.Create(&change).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to create change"}})
		return
	}

	// Broadcast CHANGE_PENDING_CONFIRM event for manual mode
	if !autoMode {
		service.BroadcastEvent(projectID, "CHANGE_PENDING_CONFIRM", gin.H{
			"change_id":   changeID,
			"agent_id":    agentID.(string),
			"task_id":     req.TaskID,
			"description": req.Description,
		})

		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"change_id": changeID,
				"status":    "pending_human_confirm",
				"message":   "Waiting for human confirmation before audit",
			},
		})
		return
	}

	// Auto mode: trigger audit workflow and wait for result (blocking)
	result, err := service.StartAuditWorkflowAndWait(changeID, 120*time.Second)
	if err != nil {
		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"change_id":        changeID,
				"status":           "pending",
				"next_action":      "poll_change_status",
				"poll_endpoint":    "GET /api/v1/change/status?change_id=" + changeID,
				"message":          "Audit did not finish within 120s. Poll the endpoint above for the final result. Do NOT resubmit.",
			},
		})
		return
	}

	// Structured "what should the agent do next" guidance so LLMs don't have
	// to guess from status codes alone.
	nextAction := "done"
	message := "Audit approved and merged."
	switch result.Status {
	case "approved":
		nextAction = "done"
		message = "Audit approved; change merged. Task is now completed."
	case "pending_fix":
		nextAction = "wait"
		message = "Audit flagged L1 issues. A Fix Agent is already working on it — do NOT resubmit. Wait for the AUDIT_RESULT broadcast on your poll channel."
	case "rejected":
		nextAction = "revise"
		message = "Audit rejected your change (L2). Read audit_reason, revise your approach, and submit a new change. Your task is still claimed."
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"change_id":    changeID,
			"status":       result.Status,
			"audit_level":  result.AuditLevel,
			"audit_reason": result.AuditReason,
			"next_action":  nextAction,
			"message":      message,
		},
	})
}

// Status returns a single change by ID. Agents use this after a change.submit
// that returned status="pending" (audit didn't finish in time) to poll the
// final outcome without scanning the entire project list.
func (h *ChangeHandler) Status(c *gin.Context) {
	changeID := c.Query("change_id")
	if changeID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "change_id is required"}})
		return
	}

	var ch model.Change
	if err := model.DB.Where("id = ?", changeID).First(&ch).Error; err != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "CHANGE_NOT_FOUND", "message": "Change not found"}})
		return
	}

	agentIDRaw, _ := c.Get("agent_id")
	aid, _ := agentIDRaw.(string)
	if ch.AgentID != aid {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "FORBIDDEN", "message": "Not your change"}})
		return
	}

	level := ""
	if ch.AuditLevel != nil {
		level = *ch.AuditLevel
	}

	// Provide next_action guidance to match what change/submit returns.
	var nextAction, message string
	switch ch.Status {
	case "pending", "pending_human_confirm":
		nextAction = "wait"
		message = "Audit still running or waiting for human confirmation. Poll again in a few seconds."
	case "pending_fix":
		nextAction = "wait"
		message = "Fix Agent is working on it. Wait for AUDIT_RESULT broadcast; do not resubmit."
	case "approved":
		nextAction = "done"
		message = "Change approved and merged."
	case "rejected":
		nextAction = "revise"
		message = "Change was rejected. Read audit_reason, revise, and submit a new change."
	default:
		nextAction = "wait"
		message = "Change in state " + ch.Status
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"change_id":    ch.ID,
			"status":       ch.Status,
			"audit_level":  level,
			"audit_reason": ch.AuditReason,
			"failure_mode": ch.FailureMode,
			"retry_count":  ch.RetryCount,
			"reviewed_at":  ch.ReviewedAt,
			"next_action":  nextAction,
			"message":      message,
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
	limit := 100
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	offset := 0
	if o := c.Query("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}

	query := model.DB.Where("project_id = ?", projectID)
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Model(&model.Change{}).Count(&total)

	var changes []model.Change
	query.Order("created_at desc").Limit(limit).Offset(offset).Find(&changes)

	result := make([]gin.H, 0, len(changes))
	for _, ch := range changes {
		item := gin.H{
			"id":          ch.ID,
			"task_id":     ch.TaskID,
			"agent_id":    ch.AgentID,
			"version":     ch.Version,
			"description": ch.Description,
			"status":      ch.Status,
			"created_at":  ch.CreatedAt,
		}
		if ch.AuditLevel != nil {
			item["audit_level"] = *ch.AuditLevel
		}
		if ch.ReviewedAt != nil {
			item["reviewed_at"] = *ch.ReviewedAt
		}
		result = append(result, item)
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"changes": result,
			"total":   total,
			"limit":   limit,
			"offset":  offset,
		},
	})
}

// ApproveForReview handles human confirmation to send a change to audit (manual mode only)
func (h *ChangeHandler) ApproveForReview(c *gin.Context) {
	var req struct {
		ChangeID string `json:"change_id" binding:"required"`
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

	if change.Status != "pending_human_confirm" {
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "INVALID_STATUS", "message": "Change is not pending human confirmation"}})
		return
	}

	// Update status to pending and start audit
	change.Status = "pending"
	model.DB.Save(&change)

	go func() {
		result, err := service.StartAuditWorkflowAndWait(change.ID, 120*time.Second)
		if err != nil {
			log.Printf("[Change] Audit failed for %s: %v", change.ID, err)
			return
		}
		// Directed broadcast of audit result to the submitting agent
		service.BroadcastDirected(change.AgentID, "AUDIT_RESULT", gin.H{
			"change_id":    change.ID,
			"status":       result.Status,
			"audit_level":  result.AuditLevel,
			"audit_reason": result.AuditReason,
		})
	}()

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"change_id": change.ID,
			"status":    "pending",
			"message":   "Approved for review, audit started",
		},
	})
}

func (h *ChangeHandler) Review(c *gin.Context) {
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
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "CHANGE_ALREADY_APPROVED", "message": "Change already reviewed"}})
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

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"change_id": change.ID,
			"status":    change.Status,
		},
	})
}

// submitOnBranch handles change.submit when the agent is on a branch.
// It writes files directly to the branch worktree without audit/version checks.
func (h *ChangeHandler) submitOnBranch(c *gin.Context, agentID string, branchID string) {
	var req struct {
		Description string                   `json:"description"`
		Writes      []model.ChangeFileEntry  `json:"writes"`
		Deletes     []string                 `json:"deletes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if len(req.Writes) == 0 && len(req.Deletes) == 0 {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "NO_FILES", "message": "No files to submit"}})
		return
	}

	// Write files to branch worktree
	if err := service.WriteBranchFiles(branchID, req.Writes, req.Deletes); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "WRITE_FAILED", "message": err.Error()}})
		return
	}

	// Commit in branch
	desc := req.Description
	if desc == "" {
		desc = "branch changes"
	}
	if err := service.BranchCommit(branchID, desc); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "COMMIT_FAILED", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"branch_id":     branchID,
			"writes_count":  len(req.Writes),
			"deletes_count": len(req.Deletes),
			"message":       "Changes written to branch",
		},
	})
}
