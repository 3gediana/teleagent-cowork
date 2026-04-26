package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/config"
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

	// InjectedArtifactIDs is the list of KnowledgeArtifact IDs the client
	// received on task.claim and was guided by while producing this
	// change. The server stores it on the Change row and — once Audit
	// gives L0/L1/L2 — calls HandleChangeAudit to bump success/failure
	// counters on those exact artifacts. Safe to omit: older clients
	// that never learned the hints protocol still work, they just don't
	// contribute to the feedback loop.
	InjectedArtifactIDs []string `json:"injected_artifact_ids,omitempty"`

	// InjectedRefs is the richer shape that also preserves per-artifact
	// selection metadata (reason + score at claim time). Preferred over
	// InjectedArtifactIDs when the client sends both; falls back to
	// InjectedArtifactIDs otherwise. HandleChangeAudit uses the ids in
	// either shape; the reason/score fields let us compute per-reason
	// success rates for offline analysis.
	InjectedRefs []service.InjectedRef `json:"injected_refs,omitempty"`
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

	// Soft nudge: if the change touches structural source files without
	// also updating OVERVIEW.md, tell the agent. The audit still runs —
	// this is a hint for future-agent-friendliness, not a block.
	// Built once here and echoed on every response path below so a client
	// that ignores it on the manual-confirm path still sees it when the
	// audit eventually finishes.
	overviewReminder := checkOverviewStale(req.Writes, req.Deletes)

	var task model.Task
	if err := model.DB.Where("id = ?", req.TaskID).First(&task).Error; err != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "TASK_NOT_FOUND", "message": "Task not found"}})
		return
	}

	// Idempotent short-circuit: if the task is already completed by THIS
	// agent, the previous change_submit call has almost certainly
	// succeeded — the client just timed out waiting for audit (120s cap)
	// and retried. Returning 404 here would mislead the agent into
	// thinking the task is still open; returning success-shaped data
	// with next_action=done lets the agent move on to feedback without
	// re-executing the write leg. We look up the most recent change for
	// this task+agent so the agent can reference it downstream.
	if task.Status == "completed" && task.AssigneeID != nil && *task.AssigneeID == agentID.(string) {
		var latest model.Change
		_ = model.DB.Where("task_id = ? AND agent_id = ?", task.ID, agentID).
			Order("created_at DESC").First(&latest).Error
		resp := gin.H{
			"task_id":     task.ID,
			"next_action": "done",
			"message":     "Task already completed by this agent (previous change_submit succeeded; probably a client-side timeout retry). No action needed — proceed to a3c_feedback.",
		}
		if latest.ID != "" {
			resp["change_id"] = latest.ID
			resp["status"] = latest.Status
			resp["audit_level"] = latest.AuditLevel
		}
		c.JSON(200, gin.H{"success": true, "data": resp})
		return
	}

	if task.Status != "claimed" {
		c.JSON(409, gin.H{"success": false, "error": gin.H{
			"code":    "TASK_NOT_CLAIMABLE",
			"message": fmt.Sprintf("Task status is %q; change_submit requires 'claimed'. Call a3c_task action=claim task_id=%s first.", task.Status, task.ID),
			"status":  task.Status,
		}})
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

	// Global autopilot kill-switch: when off, every change goes
	// through human review regardless of the per-project auto_mode
	// flag. The platform's role in this mode is collaboration-hub:
	// the server never spawns audit/fix LLM sessions on its own.
	// Any team member (or their local AI assistant acting on their
	// behalf) calls /change/review to give a verdict; the
	// submitting agent gets the result via the existing
	// AUDIT_RESULT directed broadcast (see Review handler).
	if !config.IsAutopilotEnabled() {
		autoMode = false
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

	// Persist the artifacts the client was guided by so HandleChangeAudit
	// can attribute the audit verdict back to them. We prefer the richer
	// `injected_refs` shape (id + reason + score) because it lets the
	// feedback loop compute per-reason success rates downstream. If the
	// client only sent the flat id array we synthesise refs with an
	// empty reason — HandleChangeAudit still bumps counters fine, just
	// without the per-reason breakdown.
	//
	// Silent no-op when neither is present: the loop simply doesn't
	// contribute to artifact feedback for this change, matching the
	// graceful-degradation contract for every piece of the refinery
	// pipeline.
	// Default to a valid empty JSON array, not "". MySQL strict JSON
	// mode rejects empty strings in json-typed columns, so leaving
	// this blank blocks the audit workflow from persisting its
	// verdict later (the Change.Save in ProcessAuditOutput fails
	// with "Invalid JSON text: The document is empty" and the
	// waitForChangeStatus poller in clients never sees a terminal
	// status).
	injectedArtifactsJSON := "[]"
	switch {
	case len(req.InjectedRefs) > 0:
		if b, err := json.Marshal(req.InjectedRefs); err == nil {
			injectedArtifactsJSON = string(b)
		}
	case len(req.InjectedArtifactIDs) > 0:
		refs := make([]service.InjectedRef, len(req.InjectedArtifactIDs))
		for i, id := range req.InjectedArtifactIDs {
			refs[i] = service.InjectedRef{ID: id}
		}
		if b, err := json.Marshal(refs); err == nil {
			injectedArtifactsJSON = string(b)
		}
	}

	change := model.Change{
		ID:                changeID,
		ProjectID:         projectID,
		AgentID:           agentID.(string),
		TaskID:            &req.TaskID,
		Version:           currentVersion,
		ModifiedFiles:     string(modifiedFilesJSON),
		NewFiles:          string(newFilesJSON),
		DeletedFiles:      string(deletedFilesJSON),
		Diff:              string(diffJSON),
		Description:       req.Description,
		Status:            changeStatus,
		RetryCount:        retryCount,
		InjectedArtifacts: injectedArtifactsJSON,
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

		manualData := gin.H{
			"change_id": changeID,
			"status":    "pending_human_confirm",
			"message":   "Waiting for human confirmation before audit",
		}
		if overviewReminder != "" {
			manualData["overview_reminder"] = overviewReminder
		}
		c.JSON(200, gin.H{
			"success": true,
			"data":    manualData,
		})
		return
	}

	// Auto mode: trigger audit workflow and wait briefly. Previously
	// we blocked up to 120s here waiting for the full L0/L1/L2
	// verdict — but audit is a whole LLM session and routinely takes
	// 30-90s, during which the submitting agent sits idle on a
	// synchronous HTTP call instead of moving on to the next task.
	// With N parallel pool agents that adds up to the biggest
	// single contributor to platform overhead.
	//
	// New policy: wait up to 15s for the fast path (small diffs, cached
	// contexts, lucky timing), then return "pending" with a clear
	// next_action so the agent can immediately claim its next task
	// while audit finishes in the background. The final verdict is
	// delivered via the AUDIT_RESULT broadcast + can still be polled
	// via /change/status for clients that prefer sync.
	result, err := service.StartAuditWorkflowAndWait(changeID, 15*time.Second)
	if err != nil {
		timeoutData := gin.H{
			"change_id":     changeID,
			"status":        "pending",
			"next_action":   "claim_next",
			"poll_endpoint": "GET /api/v1/change/status?change_id=" + changeID,
			"message":       "Audit is running asynchronously. You can move on: call a3c_task action=claim for the next pending task right now. The audit verdict will arrive as an AUDIT_RESULT broadcast; if it comes back L1/L2 you can revise then. Do NOT resubmit this change.",
		}
		if overviewReminder != "" {
			timeoutData["overview_reminder"] = overviewReminder
		}
		c.JSON(200, gin.H{
			"success": true,
			"data":    timeoutData,
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

	resultData := gin.H{
		"change_id":    changeID,
		"status":       result.Status,
		"audit_level":  result.AuditLevel,
		"audit_reason": result.AuditReason,
		"next_action":  nextAction,
		"message":      message,
	}
	if overviewReminder != "" {
		resultData["overview_reminder"] = overviewReminder
	}
	c.JSON(200, gin.H{
		"success": true,
		"data":    resultData,
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

	// In collaboration-hub mode (autopilot off) the server never
	// spawns audit/fix sessions on its own — there is no LLM
	// pipeline to forward this change to. ApproveForReview was
	// designed for the legacy autopilot flow where a human gates
	// the auto-audit. With autopilot off the right path is to
	// call /change/review directly (which a project member or
	// their local AI assistant can do). Reject explicitly so the
	// caller doesn't silently end up with a dangling pending row.
	if !config.IsAutopilotEnabled() {
		c.JSON(409, gin.H{"success": false, "error": gin.H{
			"code":    "AUTOPILOT_DISABLED",
			"message": "Autopilot is off; /change/approve_for_review only applies to the legacy auto-audit flow. Use POST /change/review directly with {change_id, approved, level, reason} to submit a verdict.",
		}})
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

// Review records a human (or human-via-AI-assistant) verdict on a
// change. This is the primary review entry point in
// collaboration-hub mode (autopilot off): any project member can
// call it, supplying {approved, level, reason}.
//
// Accepted source statuses:
//   - pending             : legacy autopilot flow, after audit finished
//   - pending_human_confirm: collab-hub flow, fresh out of /change/submit
//
// On approve we run the same side-effects the autopilot path runs
// (git commit, version bump, task auto-complete, VERSION_UPDATE
// broadcast) by delegating to service.ApproveAndCommitChange. On
// reject we just record the verdict.
//
// Either way we fire a directed AUDIT_RESULT broadcast to the
// submitting agent so the same client-side handler that listens for
// autopilot verdicts works unchanged.
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

	if change.Status != "pending" && change.Status != "pending_human_confirm" {
		c.JSON(409, gin.H{"success": false, "error": gin.H{
			"code":    "CHANGE_NOT_REVIEWABLE",
			"message": fmt.Sprintf("Change is %s; only pending / pending_human_confirm changes can be reviewed", change.Status),
		}})
		return
	}

	// Default level to "L0" on approve / "L2" on reject if caller
	// omitted it — saves polling clients from having to pick a
	// level when "approve" / "reject" is the whole signal.
	level := strings.TrimSpace(req.Level)
	if level == "" {
		if req.Approved {
			level = "L0"
		} else {
			level = "L2"
		}
	}

	now := time.Now()
	if req.Approved {
		// Delegate to the same approval path that autopilot uses
		// so the change actually gets a git commit + version bump
		// + task completion. Before this change Review only
		// flipped the status column, leaving task=claimed and the
		// repo with no commit — which silently broke every
		// downstream "task done" signal.
		if err := service.ApproveAndCommitChange(&change); err != nil {
			c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "APPROVE_FAILED", "message": err.Error()}})
			return
		}
		change.AuditLevel = &level
		change.AuditReason = req.Reason
		change.ReviewedAt = &now
		if err := model.DB.Save(&change).Error; err != nil {
			log.Printf("[Review] persist audit_level/reason for %s: %v", change.ID, err)
		}
	} else {
		change.Status = "rejected"
		change.AuditLevel = &level
		change.AuditReason = req.Reason
		change.ReviewedAt = &now
		if err := model.DB.Save(&change).Error; err != nil {
			c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "REJECT_PERSIST_FAILED", "message": err.Error()}})
			return
		}
	}

	// Notify the submitter via the same directed-broadcast channel
	// the autopilot AUDIT_RESULT path uses, so client SDKs only
	// need one verdict-handling code path.
	service.BroadcastDirected(change.AgentID, "AUDIT_RESULT", gin.H{
		"change_id":    change.ID,
		"status":       change.Status,
		"audit_level":  level,
		"audit_reason": req.Reason,
		"reviewed_by":  c.GetString("agent_id"),
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"change_id":   change.ID,
			"status":      change.Status,
			"audit_level": level,
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

	// Same soft nudge as the main-branch Submit: remind agents to keep
	// OVERVIEW.md current when they touch structural code. Branch flow
	// accumulates many commits before PR audit, so surfacing the hint
	// per-commit gives agents a chance to correct before pr_submit.
	branchOverviewReminder := checkOverviewStale(req.Writes, req.Deletes)

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

	branchData := gin.H{
		"branch_id":     branchID,
		"writes_count":  len(req.Writes),
		"deletes_count": len(req.Deletes),
		"message":       "Changes written to branch",
	}
	if branchOverviewReminder != "" {
		branchData["overview_reminder"] = branchOverviewReminder
	}
	c.JSON(200, gin.H{
		"success": true,
		"data":    branchData,
	})
}

// checkOverviewStale returns a reminder string when a change touches
// structural source files without also updating OVERVIEW.md. Returns
// empty string when the change is either (a) non-structural or (b)
// already includes an OVERVIEW edit. Deterministic — no LLM or DB.
//
// The threshold of 3 structural files is conservative: single-file
// bug fixes rarely warrant an overview update and we don't want to
// nag agents about trivial changes, but a 3+ file change usually
// carries enough structural intent that OVERVIEW needs a line.
//
// Detection is path-suffix-based (no AST parse) because we want this
// check to stay cheap and deterministic. "Structural" here means
// production source in a language the platform commonly hosts —
// tests, fixtures, generated code and docs are filtered out so they
// don't trip the nag.
func checkOverviewStale(writes []model.ChangeFileEntry, deletes []string) string {
	touchedOverview := false
	structuralCount := 0
	sampleNames := make([]string, 0, 3)

	for _, w := range writes {
		if isOverviewPath(w.Path) {
			touchedOverview = true
			continue
		}
		if isStructuralSourceFile(w.Path) {
			structuralCount++
			if len(sampleNames) < 3 {
				sampleNames = append(sampleNames, filepath.Base(w.Path))
			}
		}
	}
	for _, d := range deletes {
		if isOverviewPath(d) {
			// Deleting OVERVIEW.md is a structural change in itself, but
			// treat it as a special case — don't complain, the agent is
			// explicitly rearranging project docs.
			touchedOverview = true
			continue
		}
		if isStructuralSourceFile(d) {
			structuralCount++
			if len(sampleNames) < 3 {
				sampleNames = append(sampleNames, filepath.Base(d)+" (deleted)")
			}
		}
	}
	if touchedOverview || structuralCount < 3 {
		return ""
	}
	return fmt.Sprintf(
		"This change modifies %d source files (%s) but OVERVIEW.md wasn't updated. Consider editing OVERVIEW.md in the same change to reflect new or renamed modules/files — future agents rely on it as their project map.",
		structuralCount, strings.Join(sampleNames, ", "),
	)
}

// isOverviewPath matches OVERVIEW.md at the repo root — and nothing
// else. Case-insensitive to tolerate Windows/macOS case folding but
// we insist on the file living at the root: nested overviews (e.g.
// docs/OVERVIEW.md) serve a different purpose and don't satisfy the
// agent-facing map protocol.
func isOverviewPath(path string) bool {
	clean := strings.TrimLeft(filepath.ToSlash(path), "./")
	return strings.EqualFold(clean, "OVERVIEW.md")
}

// isStructuralSourceFile returns true when the path looks like
// production source in a language the platform commonly hosts. Tests
// and generated code are filtered out so routine test-only changes
// don't trigger the OVERVIEW nag.
func isStructuralSourceFile(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))

	// Exclude common test / fixture / generated patterns first.
	switch {
	case strings.HasSuffix(lower, "_test.go"):
		return false
	case strings.HasSuffix(lower, ".test.ts"), strings.HasSuffix(lower, ".test.tsx"),
		strings.HasSuffix(lower, ".test.js"), strings.HasSuffix(lower, ".test.jsx"):
		return false
	case strings.HasSuffix(lower, ".spec.ts"), strings.HasSuffix(lower, ".spec.tsx"),
		strings.HasSuffix(lower, ".spec.js"), strings.HasSuffix(lower, ".spec.jsx"):
		return false
	case strings.HasSuffix(lower, "_test.py"), strings.HasPrefix(filepath.Base(lower), "test_"):
		return false
	case strings.Contains(lower, "/testdata/"), strings.Contains(lower, "/fixtures/"),
		strings.Contains(lower, "/__tests__/"), strings.Contains(lower, "/tests/"):
		return false
	case strings.HasSuffix(lower, ".pb.go"), strings.HasSuffix(lower, ".gen.go"),
		strings.HasSuffix(lower, "_generated.go"):
		return false
	}

	// Accept the usual production-source extensions. Purposely not
	// exhaustive — this is a heuristic, false negatives just mean no
	// nag, which is the safe side.
	structuralExts := []string{
		".go", ".rs", ".py", ".rb", ".java", ".kt", ".swift",
		".ts", ".tsx", ".js", ".jsx", ".vue", ".svelte",
		".c", ".cc", ".cpp", ".cxx", ".h", ".hpp",
		".cs", ".fs", ".scala", ".ex", ".exs",
	}
	for _, ext := range structuralExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
