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
	"github.com/a3c/platform/internal/opencode"
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

// Metrics returns the ring-buffered token-usage samples and
// lifecycle events for a single pool instance. Shape:
//
//	{ tokens: [{at_ms, tokens, session_id}], events: [{at_ms, type, detail}] }
//
// Read-only; any authenticated agent can fetch (same gating as
// List). Dashboard polls this on card expand to render the
// sparkline and event log. Unknown instance id → 200 with
// empty arrays so the UI can render "no data yet" cleanly.
func (h *AgentPoolHandler) Metrics(c *gin.Context) {
	id := c.Param("instance_id")
	if id == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "instance_id required"}})
		return
	}
	m := agentpool.GetDefault()
	if m == nil {
		c.JSON(503, gin.H{"success": false, "error": gin.H{"code": "POOL_NOT_READY", "message": "agent pool not initialised"}})
		return
	}
	snap := m.MetricsFor(id)
	// Always return slices (not null) so the frontend doesn't have
	// to special-case empty history.
	if snap.Tokens == nil {
		snap.Tokens = []agentpool.TokenSample{}
	}
	if snap.Events == nil {
		snap.Events = []agentpool.PoolEvent{}
	}
	c.JSON(200, gin.H{"success": true, "data": snap})
}

// OpencodeProviders surfaces the provider/model catalogue opencode
// itself carries in ~/.config/opencode/opencode.json. This is what
// pool agents pin when Spawn puts A3C_OPENCODE_PROVIDER_ID /
// A3C_OPENCODE_MODEL_ID into the subprocess env — NOT the LLMEndpoint
// catalogue (/opencode/providers which is for the native runner).
// The frontend spawn modal consumes this endpoint to drive its
// provider + model dropdowns.
//
// Any authenticated agent can read this (there are no secrets in
// the response — apiKey is intentionally dropped by the reader).
// Missing config file is a 200 with empty providers, so fresh
// installs don't block the spawn modal.
func (h *AgentPoolHandler) OpencodeProviders(c *gin.Context) {
	providers, err := opencode.LoadProviders()
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "CONFIG_READ_FAILED", "message": err.Error()}})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"providers": providers}})
}

// Sleep puts a ready instance into dormancy manually. Mirrors what
// the background detector does on idle-timeout, just bypasses the
// clock. Body: {instance_id}.
func (h *AgentPoolHandler) Sleep(c *gin.Context) {
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
	// Cap at 30s so a hung archive-session creation doesn't block
	// the dashboard indefinitely. enterDormancy's inner archive
	// call also has its own 5s timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.EnterDormancy(ctx, req.InstanceID, "manual"); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SLEEP_FAILED", "message": err.Error()}})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"instance_id": req.InstanceID, "status": "dormant"}})
}

// Wake revives a dormant instance: re-spawn opencode, create a
// fresh session, flip status back to ready. Body: {instance_id}.
func (h *AgentPoolHandler) Wake(c *gin.Context) {
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
	// 60s cap like Spawn — wake does roughly the same amount of
	// work (prepare .opencode, spawn, health check, create session).
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	inst, err := m.Wake(ctx, req.InstanceID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "WAKE_FAILED", "message": err.Error()}})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": inst})
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
