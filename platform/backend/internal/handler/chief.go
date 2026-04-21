package handler

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/opencode"
	"github.com/a3c/platform/internal/service"
)

type ChiefHandler struct{}

func NewChiefHandler() *ChiefHandler {
	return &ChiefHandler{}
}

// Chat handles human-to-Chief conversation requests.
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

	// Check for existing Chief session with active OpenCode session
	existingSession := findActiveChiefSession(projectID)
	if existingSession != nil && existingSession.OpenCodeSessionID != "" {
		// Multi-round: send follow-up message to existing serve session
		scheduler := opencode.DefaultScheduler
		if scheduler != nil {
			modelStr := "minimax-coding-plan/MiniMax-M2.7"
			roleConfig := agent.GetRoleConfigWithOverride(agent.RoleChief)
			if roleConfig != nil && roleConfig.ModelProvider != "" {
				modelStr = roleConfig.ModelProvider + "/" + roleConfig.ModelID
			}

			msgResp, err := scheduler.SendToExistingSession(
				existingSession.OpenCodeSessionID,
				req.Message,
				"chief",
				modelStr,
			)
			if err != nil {
				log.Printf("[Chief] Failed to send follow-up message: %v", err)
				c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to send message"}})
				return
			}

			// Extract text response
			var agentText string
			for _, part := range msgResp.Parts {
				if part.Type == "text" {
					agentText += part.Text
				}
			}

			// Broadcast chat update
			service.BroadcastEvent(projectID, "CHIEF_CHAT_UPDATE", gin.H{
				"role":    "chief",
				"content": agentText,
			})

			c.JSON(200, gin.H{
				"success": true,
				"data": gin.H{
					"session_id":            existingSession.ID,
					"status":               "active",
					"agent_response":        agentText,
					"opencode_session_id":   existingSession.OpenCodeSessionID,
				},
			})
			return
		}
	}

	// No active session: create new Chief chat session. Response shape matches
	// the multi-round case: { session_id, status, agent_response } — agent_response
	// is empty for async starts; the caller should poll /chief/sessions or listen
	// for CHIEF_CHAT_UPDATE broadcasts to get the reply.
	go service.TriggerChiefChat(projectID, req.Message)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"status":              "started",
			"agent_response":      "",
			"opencode_session_id": "",
			"message":             "Chief Agent session started. Listen for CHIEF_CHAT_UPDATE on SSE, or poll /chief/sessions for the session row.",
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

// findActiveChiefSession finds an active Chief session for a project.
func findActiveChiefSession(projectID string) *agent.Session {
	for _, s := range agent.DefaultManager.Sessions() {
		if s.ProjectID == projectID && s.Role == agent.RoleChief && s.Status == "running" {
			return s
		}
	}
	return nil
}
