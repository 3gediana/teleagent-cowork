package handler

// Tag lifecycle endpoints
// =======================
//
// GET  /tag/list?task_id=...&status=proposed — list tags for a task
// POST /tag/confirm                            — confirm a proposed tag
// POST /tag/reject                             — reject a proposed tag
// POST /tag/supersede                          — replace one tag with another
//
// Only humans (Agent.IsHuman) can confirm / reject. This matches the
// existing ownership-check convention in handler/task.go:
// the review audit trail must not be forgeable by autonomous clients.
// Future work: allow an Analyze Agent identity with lower-confidence
// bumps (Snorkel-style weak-label source).

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"
)

type TagHandler struct{}

func NewTagHandler() *TagHandler { return &TagHandler{} }

// GET /tag/list?task_id=...&status=...
// status is optional; empty returns every state so reviewers can see
// the full history of how a task was classified over time.
func (h *TagHandler) List(c *gin.Context) {
	taskID := c.Query("task_id")
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "INVALID_PARAMS", "message": "task_id required"}})
		return
	}
	q := model.DB.Model(&model.TaskTag{}).Where("task_id = ?", taskID)
	if s := c.Query("status"); s != "" {
		q = q.Where("status = ?", s)
	}
	var tags []model.TaskTag
	q.Order("dimension, status, confidence DESC").Find(&tags)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"tags": tags}})
}

type tagActionRequest struct {
	TagID string `json:"tag_id" binding:"required"`
	Note  string `json:"note"`
}

// POST /tag/confirm  { tag_id, note? }
func (h *TagHandler) Confirm(c *gin.Context) {
	h.applyAction(c, service.ConfirmTag)
}

// POST /tag/reject  { tag_id, note? }
func (h *TagHandler) Reject(c *gin.Context) {
	h.applyAction(c, service.RejectTag)
}

// applyAction is the shared implementation for confirm/reject. Both
// require a human caller (see module-level doc).
func (h *TagHandler) applyAction(c *gin.Context, action func(tagID, reviewer, note string) error) {
	agentIDVal, _ := c.Get("agent_id")
	agentID, _ := agentIDVal.(string)

	if !requireHuman(c, agentID) {
		return
	}

	var req tagActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if err := action(req.TagID, "human:"+agentID, req.Note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false,
			"error": gin.H{"code": "TAG_ACTION_FAILED", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// POST /tag/supersede  { old_tag_id, new_tag_id }
// The reviewer must have already created `new_tag_id` themselves (via a
// manual insert through another endpoint — we deliberately don't allow
// supersede to implicitly create the replacement, that's a separate
// human intent).
type supersedeRequest struct {
	OldTagID string `json:"old_tag_id" binding:"required"`
	NewTagID string `json:"new_tag_id" binding:"required"`
}

func (h *TagHandler) Supersede(c *gin.Context) {
	agentIDVal, _ := c.Get("agent_id")
	agentID, _ := agentIDVal.(string)
	if !requireHuman(c, agentID) {
		return
	}
	var req supersedeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}
	if err := service.SupersedeTag(req.OldTagID, req.NewTagID, "human:"+agentID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false,
			"error": gin.H{"code": "TAG_ACTION_FAILED", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// requireHuman looks up the caller and 403s if it's not flagged
// Agent.IsHuman=true. Centralised here rather than via a middleware
// because only a handful of endpoints need this check (matches the
// existing convention).
func requireHuman(c *gin.Context, agentID string) bool {
	if agentID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false,
			"error": gin.H{"code": "UNAUTHENTICATED", "message": "agent_id missing"}})
		return false
	}
	var a model.Agent
	if err := model.DB.Where("id = ?", agentID).First(&a).Error; err != nil {
		c.JSON(http.StatusForbidden, gin.H{"success": false,
			"error": gin.H{"code": "AGENT_NOT_FOUND"}})
		return false
	}
	if !a.IsHuman {
		c.JSON(http.StatusForbidden, gin.H{"success": false,
			"error": gin.H{"code": "HUMAN_ONLY", "message": "only human reviewers may transition tag lifecycle"}})
		return false
	}
	return true
}
