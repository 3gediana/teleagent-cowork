package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
)

type PolicyHandler struct{}

func NewPolicyHandler() *PolicyHandler {
	return &PolicyHandler{}
}

// List returns policies filtered by status.
// GET /policy/list?status=active
func (h *PolicyHandler) List(c *gin.Context) {
	status := c.Query("status")

	query := model.DB
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var policies []model.Policy
	query.Order("priority DESC, created_at DESC").Limit(100).Find(&policies)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"policies": policies,
		},
	})
}

// Get returns a single policy by ID.
// GET /policy/:id
func (h *PolicyHandler) Get(c *gin.Context) {
	id := c.Param("id")

	var policy model.Policy
	if model.DB.Where("id = ?", id).First(&policy).Error != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "NOT_FOUND", "message": "Policy not found"}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data":    policy,
	})
}

// Activate activates a policy → status=active.
// POST /policy/:id/activate
func (h *PolicyHandler) Activate(c *gin.Context) {
	id := c.Param("id")

	var policy model.Policy
	if model.DB.Where("id = ?", id).First(&policy).Error != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "NOT_FOUND", "message": "Policy not found"}})
		return
	}

	if policy.Status == "active" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_STATE", "message": "Policy is already active"}})
		return
	}

	model.DB.Model(&model.Policy{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":     "active",
		"updated_at": time.Now(),
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":     id,
			"status": "active",
		},
	})
}

// Deactivate deactivates a policy → status=deprecated.
// POST /policy/:id/deactivate
func (h *PolicyHandler) Deactivate(c *gin.Context) {
	id := c.Param("id")

	var policy model.Policy
	if model.DB.Where("id = ?", id).First(&policy).Error != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "NOT_FOUND", "message": "Policy not found"}})
		return
	}

	if policy.Status != "active" && policy.Status != "candidate" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_STATE", "message": "Only active or candidate policies can be deactivated"}})
		return
	}

	model.DB.Model(&model.Policy{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":     "deprecated",
		"updated_at": time.Now(),
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":     id,
			"status": "deprecated",
		},
	})
}
