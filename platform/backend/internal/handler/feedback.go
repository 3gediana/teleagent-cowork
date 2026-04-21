package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"
)

type FeedbackHandler struct{}

func NewFeedbackHandler() *FeedbackHandler {
	return &FeedbackHandler{}
}

// Submit receives structured feedback from an agent and stores it as an Experience.
// POST /feedback/submit?project_id=xxx
func (h *FeedbackHandler) Submit(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	agentID, _ := c.Get("agent_id")
	var agent model.Agent
	if model.DB.Where("id = ?", agentID).First(&agent).Error != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "Agent not found"}})
		return
	}

	var req struct {
		TaskID          string   `json:"task_id"`
		Outcome         string   `json:"outcome"` // success / partial / failed
		Approach        string   `json:"approach"`
		Pitfalls        string   `json:"pitfalls"`
		KeyInsight      string   `json:"key_insight"`
		MissingContext  string   `json:"missing_context"`
		DoDifferently   string   `json:"would_do_differently"`
		FilesRead       []string `json:"files_read"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if req.Outcome == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "outcome is required"}})
		return
	}

	filesJSON := "null"
	if len(req.FilesRead) > 0 {
		service.MarshalToJSON(req.FilesRead, &filesJSON)
	}

	exp := model.Experience{
		ID:             model.GenerateID("exp"),
		ProjectID:      projectID,
		SourceType:     "agent_feedback",
		SourceID:       agent.ID,
		AgentRole:      agent.Name, // Agent name identifies the role context
		TaskID:         req.TaskID,
		Outcome:        req.Outcome,
		Approach:       req.Approach,
		Pitfalls:       req.Pitfalls,
		KeyInsight:     req.KeyInsight,
		MissingContext: req.MissingContext,
		DoDifferently:  req.DoDifferently,
		FilesInvolved:  filesJSON,
		Status:         "raw",
		CreatedAt:      time.Now(),
	}

	if err := model.DB.Create(&exp).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to save experience"}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":      exp.ID,
			"status":  exp.Status,
			"message": "Experience recorded successfully",
		},
	})
}
