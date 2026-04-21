package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
)

type ExperienceHandler struct{}

func NewExperienceHandler() *ExperienceHandler {
	return &ExperienceHandler{}
}

// List returns experiences filtered by project, status, and source_type.
// GET /experience/list?project_id=xxx&status=raw&source_type=agent_feedback&limit=50
func (h *ExperienceHandler) List(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	status := c.Query("status")
	sourceType := c.Query("source_type")
	limit := 50
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	query := model.DB.Where("project_id = ?", projectID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if sourceType != "" {
		query = query.Where("source_type = ?", sourceType)
	}

	var experiences []model.Experience
	query.Order("created_at DESC").Limit(limit).Find(&experiences)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"experiences": experiences,
		},
	})
}
