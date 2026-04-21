package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/opencode"
)

type RoleHandler struct{}

func NewRoleHandler() *RoleHandler {
	return &RoleHandler{}
}

// ListRoles returns all agent roles with their current model config
func (h *RoleHandler) ListRoles(c *gin.Context) {
	configs := agent.GetAllRoleConfigs()
	c.JSON(200, gin.H{
		"success": true,
		"data":    configs,
	})
}

// UpdateRoleModel sets the model provider/ID override for a role
func (h *RoleHandler) UpdateRoleModel(c *gin.Context) {
	var req struct {
		Role          string `json:"role" binding:"required"`
		ModelProvider string `json:"model_provider"`
		ModelID       string `json:"model_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	// Validate role exists
	if agent.GetRoleConfig(agent.Role(req.Role)) == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "ROLE_NOT_FOUND", "message": "Unknown role: " + req.Role}})
		return
	}

	if err := agent.SetRoleOverride(agent.Role(req.Role), req.ModelProvider, req.ModelID); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"role":          req.Role,
			"model_provider": req.ModelProvider,
			"model_id":      req.ModelID,
		},
	})
}

// GetProviders proxies the OpenCode serve /provider endpoint
func (h *RoleHandler) GetProviders(c *gin.Context) {
	client := opencode.DefaultClient
	if client == nil {
		c.JSON(503, gin.H{"success": false, "error": gin.H{"code": "OPENCODE_UNAVAILABLE", "message": "OpenCode serve not connected"}})
		return
	}

	providers, defaultModel, err := client.GetProviders()
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "PROVIDER_FETCH_FAILED", "message": err.Error()}})
		return
	}

	// Return both raw providers and flattened model list for UI
	flatModels := opencode.FlattenProviders(providers)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"providers":    providers,
			"models":       flatModels,
			"default":      defaultModel,
		},
	})
}
