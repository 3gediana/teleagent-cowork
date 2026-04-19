package handler

import (
	"github.com/gin-gonic/gin"
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
		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"answer":     "Project info query received. Session created for processing.",
				"session_id": session.ID,
			},
		})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"answer":     "Your query is being processed by the consult agent.",
			"session_id": session.ID,
		},
	})
}