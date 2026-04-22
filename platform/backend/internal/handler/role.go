package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/llm"
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

// GetProviders lists every model available across the registered LLM
// endpoints. Replaces the former opencode /provider proxy — the data
// source is now user-registered LLMEndpoint rows loaded into
// llm.DefaultRegistry at startup, so changing endpoints via the
// dashboard reflects here without a restart.
//
// Response shape keeps the legacy keys the frontend's Role Models
// selector reads: { providers: [{id,name,models:[{id,name,...}]}],
// models: [{provider_id,provider_name,model_id,model_name,...}],
// default: {provider_id, model_id} }
func (h *RoleHandler) GetProviders(c *gin.Context) {
	entries := llm.DefaultRegistry.List()
	if len(entries) == 0 {
		c.JSON(503, gin.H{"success": false, "error": gin.H{
			"code":    "NO_LLM_ENDPOINTS",
			"message": "No LLM endpoints registered — add one under Settings → LLM Endpoints first.",
		}})
		return
	}

	providers := make([]gin.H, 0, len(entries))
	flatModels := make([]gin.H, 0)
	var defaultEntry *llm.Entry
	for _, e := range entries {
		if defaultEntry == nil && e.DefaultModel != "" {
			defaultEntry = e
		}
		modelList := make([]gin.H, 0)
		for _, m := range e.Provider.Models() {
			modelList = append(modelList, gin.H{
				"id":                m.ID,
				"name":              m.Name,
				"context_window":    m.ContextWindow,
				"max_output_tokens": m.MaxOutputTokens,
				"supports_tools":    m.SupportsTools,
				"supports_vision":   m.SupportsVision,
			})
			flatModels = append(flatModels, gin.H{
				"provider_id":   e.EndpointID,
				"provider_name": e.EndpointName,
				"model_id":      m.ID,
				"model_name":    m.Name,
				"reasoning":     m.SupportsReasoning,
				"tool_call":     m.SupportsTools,
			})
		}
		providers = append(providers, gin.H{
			"id":     e.EndpointID,
			"name":   e.EndpointName,
			"format": string(e.Format),
			"models": modelList,
		})
	}

	defaultPayload := gin.H{}
	if defaultEntry != nil {
		defaultPayload = gin.H{
			"provider_id": defaultEntry.EndpointID,
			"model_id":    defaultEntry.DefaultModel,
		}
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"providers": providers,
			"models":    flatModels,
			"default":   defaultPayload,
		},
	})
}
