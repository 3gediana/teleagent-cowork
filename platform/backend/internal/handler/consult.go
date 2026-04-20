package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/service"
)

type ConsultHandler struct{}

func NewConsultHandler() *ConsultHandler {
	return &ConsultHandler{}
}

func (h *ConsultHandler) ProjectInfo(c *gin.Context) {
	var req struct {
		Query string `json:"query" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	session, err := service.TriggerConsultAgent(projectID, req.Query)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	for i := 0; i < 60; i++ {
		time.Sleep(2 * time.Second)
		s := agent.DefaultManager.GetSession(session.ID)
		if s == nil {
			continue
		}
		if s.Status == "completed" {
			c.JSON(200, gin.H{
				"success": true,
				"data": gin.H{
					"answer":     s.Output,
					"session_id": session.ID,
				},
			})
			return
		}
		if s.Status == "failed" {
			c.JSON(500, gin.H{
				"success": false,
				"error": gin.H{"code": "SYSTEM_ERROR", "message": "Consult agent failed to process query"},
			})
			return
		}
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"answer":     "Query is taking longer than expected. Check session " + session.ID + " for results.",
			"session_id": session.ID,
		},
	})
}
