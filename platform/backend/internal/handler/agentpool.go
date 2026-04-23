package handler

// HTTP surface for the agent pool.
//
// Authentication model: all endpoints gated on IsHuman — only the
// dashboard operator can spawn / shut down pool instances. The
// spawned agents themselves use the normal /agent/register flow
// (their access key is provisioned at spawn time).

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/agentpool"
)

type AgentPoolHandler struct{}

func NewAgentPoolHandler() *AgentPoolHandler {
	return &AgentPoolHandler{}
}

// List returns every pool instance the platform currently knows about.
// Includes stopped / crashed ones until Purge clears them — humans
// want to see recent crashes to understand why a task didn't get
// picked up.
func (h *AgentPoolHandler) List(c *gin.Context) {
	m := agentpool.GetDefault()
	if m == nil {
		c.JSON(503, gin.H{"success": false, "error": gin.H{"code": "POOL_NOT_READY", "message": "agent pool not initialised"}})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"instances": m.List()}})
}

// Spawn brings up a new instance. Body: {project_id, role_hint?, name?}.
// Returns the Instance record once the subprocess is healthy, or a
// 500 if health never went green within StartupTimeout.
func (h *AgentPoolHandler) Spawn(c *gin.Context) {
	if !callerIsHuman(c) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "HUMAN_ONLY", "message": "agent pool is human-controlled"}})
		return
	}
	m := agentpool.GetDefault()
	if m == nil {
		c.JSON(503, gin.H{"success": false, "error": gin.H{"code": "POOL_NOT_READY", "message": "agent pool not initialised"}})
		return
	}
	var req struct {
		ProjectID          string `json:"project_id"`
		RoleHint           string `json:"role_hint"`
		Name               string `json:"name"`
		OpencodeProviderID string `json:"opencode_provider_id"`
		OpencodeModelID    string `json:"opencode_model_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	// Cap the Spawn call at 60s so a stuck subprocess startup
	// doesn't hold the HTTP handler forever.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	inst, err := m.Spawn(ctx, agentpool.SpawnRequest{
		ProjectID:          req.ProjectID,
		RoleHint:           agent.Role(req.RoleHint),
		Name:               req.Name,
		OpencodeProviderID: req.OpencodeProviderID,
		OpencodeModelID:    req.OpencodeModelID,
	})
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SPAWN_FAILED", "message": err.Error()}})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": inst})
}

// Shutdown stops a specific instance. Body: {instance_id}.
func (h *AgentPoolHandler) Shutdown(c *gin.Context) {
	if !callerIsHuman(c) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "HUMAN_ONLY", "message": "agent pool is human-controlled"}})
		return
	}
	m := agentpool.GetDefault()
	if m == nil {
		c.JSON(503, gin.H{"success": false, "error": gin.H{"code": "POOL_NOT_READY", "message": "agent pool not initialised"}})
		return
	}
	var req struct {
		InstanceID string `json:"instance_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}
	if err := m.Shutdown(req.InstanceID); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SHUTDOWN_FAILED", "message": err.Error()}})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"instance_id": req.InstanceID, "status": "stopped"}})
}

// Purge drops a stopped / crashed instance from the pool (removes
// the zombie Agent row the dashboard still shows).
func (h *AgentPoolHandler) Purge(c *gin.Context) {
	if !callerIsHuman(c) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "HUMAN_ONLY", "message": "agent pool is human-controlled"}})
		return
	}
	m := agentpool.GetDefault()
	if m == nil {
		c.JSON(503, gin.H{"success": false, "error": gin.H{"code": "POOL_NOT_READY", "message": "agent pool not initialised"}})
		return
	}
	var req struct {
		InstanceID string `json:"instance_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}
	if err := m.Purge(req.InstanceID); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "PURGE_FAILED", "message": err.Error()}})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"instance_id": req.InstanceID, "purged": true}})
}
