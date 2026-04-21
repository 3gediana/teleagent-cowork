package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
)

type SkillHandler struct{}

func NewSkillHandler() *SkillHandler {
	return &SkillHandler{}
}

// List returns skill candidates filtered by status.
// GET /skill/list?status=candidate
func (h *SkillHandler) List(c *gin.Context) {
	status := c.Query("status")

	query := model.DB
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var skills []model.SkillCandidate
	query.Order("created_at DESC").Limit(100).Find(&skills)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"skills": skills,
		},
	})
}

// Get returns a single skill by ID.
// GET /skill/:id
func (h *SkillHandler) Get(c *gin.Context) {
	id := c.Param("id")

	var skill model.SkillCandidate
	if model.DB.Where("id = ?", id).First(&skill).Error != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "NOT_FOUND", "message": "Skill not found"}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data":    skill,
	})
}

// Approve approves a skill candidate → status=active.
// POST /skill/:id/approve
func (h *SkillHandler) Approve(c *gin.Context) {
	id := c.Param("id")

	var skill model.SkillCandidate
	if model.DB.Where("id = ?", id).First(&skill).Error != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "NOT_FOUND", "message": "Skill not found"}})
		return
	}

	if skill.Status != "candidate" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_STATE", "message": "Only candidate skills can be approved"}})
		return
	}

	agentID, _ := c.Get("agent_id")
	approvedBy := ""
	if aid, ok := agentID.(string); ok {
		approvedBy = aid
	}

	model.DB.Model(&model.SkillCandidate{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":      "active",
		"approved_by": approvedBy,
		"updated_at":  time.Now(),
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":     id,
			"status": "active",
		},
	})
}

// Reject rejects a skill candidate → status=rejected.
// POST /skill/:id/reject
func (h *SkillHandler) Reject(c *gin.Context) {
	id := c.Param("id")

	var skill model.SkillCandidate
	if model.DB.Where("id = ?", id).First(&skill).Error != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "NOT_FOUND", "message": "Skill not found"}})
		return
	}

	if skill.Status != "candidate" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_STATE", "message": "Only candidate skills can be rejected"}})
		return
	}

	model.DB.Model(&model.SkillCandidate{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":     "rejected",
		"updated_at": time.Now(),
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":     id,
			"status": "rejected",
		},
	})
}
