package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"
)

type ChiefHandler struct{}

func NewChiefHandler() *ChiefHandler {
	return &ChiefHandler{}
}

// Chat handles human-to-Chief conversation requests. Every message
// spawns a fresh Chief agent session; prior turns are replayed as
// prompt prefix inside service.TriggerChiefChat so the model still
// sees multi-round context. Reply delivery is asynchronous — the
// frontend listens for CHIEF_CHAT_UPDATE on SSE.
//
// POST /chief/chat?project_id=xxx
func (h *ChiefHandler) Chat(c *gin.Context) {
	if !callerIsHuman(c) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "HUMAN_ONLY", "message": "Chief chat is reserved for human users"}})
		return
	}

	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	var req struct {
		Message string `json:"message" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	// Kick off async Chief session. TriggerChiefChat persists the
	// user turn to DialogueMessage before dispatch; the assistant
	// turn is written when the session completes via
	// service.HandleSessionCompletion.
	go service.TriggerChiefChat(projectID, req.Message)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"status":         "started",
			"agent_response": "",
			"message":        "Chief Agent session started. Listen for CHIEF_CHAT_UPDATE on SSE, or call /dashboard/messages?channel=chief for the rendered transcript.",
		},
	})
}

// Sessions returns session history for a project.
// GET /chief/sessions?project_id=xxx&role=xxx
func (h *ChiefHandler) Sessions(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	role := c.Query("role")
	query := model.DB.Where("project_id = ?", projectID)
	if role != "" {
		query = query.Where("role = ?", role)
	}

	var sessions []model.AgentSession
	query.Order("created_at DESC").Limit(50).Find(&sessions)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"sessions": sessions,
		},
	})
}

// ToolTraces returns tool call traces for a session.
// GET /chief/traces?session_id=xxx
func (h *ChiefHandler) ToolTraces(c *gin.Context) {
	sessionID := c.Query("session_id")
	if sessionID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "session_id is required"}})
		return
	}

	var traces []model.ToolCallTrace
	model.DB.Where("session_id = ?", sessionID).Order("created_at ASC").Find(&traces)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"traces": traces,
		},
	})
}

// Policies returns all active policies.
// GET /chief/policies
func (h *ChiefHandler) Policies(c *gin.Context) {
	status := c.Query("status")
	if status == "" {
		status = "active"
	}

	var policies []model.Policy
	model.DB.Where("status = ?", status).
		Order("priority DESC, created_at DESC").
		Limit(200).
		Find(&policies)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"policies": policies,
		},
	})
}

