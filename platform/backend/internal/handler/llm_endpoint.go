package handler

// LLM endpoint management endpoints.
//
// Surfaces the user-registered LLM endpoint CRUD:
//   GET    /llm/endpoints           list (api_key redacted)
//   POST   /llm/endpoints           create
//   GET    /llm/endpoints/:id       read (api_key redacted)
//   PUT    /llm/endpoints/:id       update (api_key optional — blank keeps existing)
//   DELETE /llm/endpoints/:id       soft-delete (status=disabled + registry.Remove)
//   POST   /llm/endpoints/:id/test  dry-run a minimal request, surface any error
//
// Mutations are human-only (matches existing convention for anything
// that can affect other agents' runtime — tag lifecycle, chief chat).
// Listing and reading are open to any authenticated agent so MCP
// clients can introspect which models exist before requesting a
// particular capability.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/a3c/platform/internal/llm"
	"github.com/a3c/platform/internal/model"
)

type LLMEndpointHandler struct{}

func NewLLMEndpointHandler() *LLMEndpointHandler { return &LLMEndpointHandler{} }

// isValidEndpointFormat gates what the handler accepts. Must stay in
// sync with the switch in internal/llm/loader.go:installEntry — both
// sides need to agree on which labels exist. 'openai_compatible' is
// an alias for 'openai' (same wire protocol); keeping it as a
// separate label lets operators tell at a glance which endpoints are
// first-party OpenAI versus third-party compatible providers.
func isValidEndpointFormat(f string) bool {
	switch f {
	case "anthropic", "openai", "openai_compatible":
		return true
	}
	return false
}

// endpointResponse is the wire shape for GET responses. Mirrors the
// DB row but replaces APIKey with a redacted preview and decodes the
// Models JSON into a typed slice so the frontend doesn't have to parse
// it again.
type endpointResponse struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Format          string           `json:"format"`
	BaseURL         string           `json:"base_url"`
	APIKeyRedacted  string           `json:"api_key_redacted"`
	APIKeySet       bool             `json:"api_key_set"`
	Models          []llm.ModelInfo  `json:"models"`
	DefaultModel    string           `json:"default_model"`
	Status          string           `json:"status"`
	CreatedBy       string           `json:"created_by"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	// Registered indicates whether the row is currently loaded into
	// the live Registry. Useful for the dashboard to flag "edited but
	// not yet reloaded" states — in practice always matches status.
	Registered bool `json:"registered"`
}

func toResponse(row *model.LLMEndpoint) endpointResponse {
	models, _ := llm.ParseModelsJSONStr(row.Models)
	_, err := llm.DefaultRegistry.Get(row.ID)
	return endpointResponse{
		ID:             row.ID,
		Name:           row.Name,
		Format:         row.Format,
		BaseURL:        row.BaseURL,
		APIKeyRedacted: row.RedactAPIKey(),
		APIKeySet:      row.APIKey != "",
		Models:         models,
		DefaultModel:   row.DefaultModel,
		Status:         row.Status,
		CreatedBy:      row.CreatedBy,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		Registered:     err == nil,
	}
}

// List returns every endpoint (active + disabled). The dashboard sorts
// and filters client-side.
func (h *LLMEndpointHandler) List(c *gin.Context) {
	var rows []model.LLMEndpoint
	if err := model.DB.Order("created_at DESC").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false,
			"error": gin.H{"code": "DB_ERROR", "message": err.Error()}})
		return
	}
	out := make([]endpointResponse, 0, len(rows))
	for i := range rows {
		out = append(out, toResponse(&rows[i]))
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"endpoints": out}})
}

// Get reads one endpoint by id.
func (h *LLMEndpointHandler) Get(c *gin.Context) {
	id := c.Param("id")
	var row model.LLMEndpoint
	if err := model.DB.Where("id = ?", id).First(&row).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false,
			"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": toResponse(&row)})
}

// createOrUpdateRequest is the shared body for POST + PUT. APIKey is
// optional on PUT — empty means "keep the current secret". Models is
// required on POST; on PUT if omitted it preserves the existing list.
type createOrUpdateRequest struct {
	Name         string           `json:"name"`
	Format       string           `json:"format"`
	BaseURL      string           `json:"base_url"`
	APIKey       string           `json:"api_key"`
	Models       []llm.ModelInfo  `json:"models"`
	DefaultModel string           `json:"default_model"`
	Status       string           `json:"status"`
}

// Create installs a new endpoint. Validates format and name uniqueness
// server-side — don't rely on the DB's unique constraint for the error
// message, we want a clear "name is taken" 409 rather than a generic
// GORM duplicate-key error.
func (h *LLMEndpointHandler) Create(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	aid, _ := agentID.(string)
	if !requireHuman(c, aid) {
		return
	}

	var req createOrUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Format = strings.ToLower(strings.TrimSpace(req.Format))
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "INVALID_PARAMS", "message": "name required"}})
		return
	}
	if !isValidEndpointFormat(req.Format) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "INVALID_PARAMS", "message": "format must be 'anthropic', 'openai' or 'openai_compatible'"}})
		return
	}
	if req.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "INVALID_PARAMS", "message": "api_key required"}})
		return
	}
	var dup int64
	model.DB.Model(&model.LLMEndpoint{}).Where("name = ?", req.Name).Count(&dup)
	if dup > 0 {
		c.JSON(http.StatusConflict, gin.H{"success": false,
			"error": gin.H{"code": "NAME_TAKEN", "message": "an endpoint with this name already exists"}})
		return
	}

	modelsJSON, err := llm.EncodeModelsJSON(req.Models)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "INVALID_PARAMS", "message": "bad models payload: " + err.Error()}})
		return
	}
	status := req.Status
	if status == "" {
		status = "active"
	}

	row := model.LLMEndpoint{
		ID:           model.GenerateID("llm"),
		Name:         req.Name,
		Format:       req.Format,
		BaseURL:      req.BaseURL,
		APIKey:       req.APIKey,
		Models:       modelsJSON,
		DefaultModel: req.DefaultModel,
		Status:       status,
		CreatedBy:    aid,
	}
	if err := model.DB.Create(&row).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false,
			"error": gin.H{"code": "DB_ERROR", "message": err.Error()}})
		return
	}
	// Reload into the registry so the endpoint is usable immediately.
	// Load failure downgrades to a log line — the DB row is saved
	// either way, and the operator can fix + retry via PUT.
	if err := llm.LoadEndpoint(row.ID); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data":    toResponse(&row),
			"warning": "endpoint saved but failed to load into runtime registry: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": toResponse(&row)})
}

// Update edits an existing endpoint. Fields left blank on PUT preserve
// their previous value (critical for APIKey — GET redacts it, so a
// client round-tripping the form wouldn't have the real value to send
// back).
func (h *LLMEndpointHandler) Update(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	aid, _ := agentID.(string)
	if !requireHuman(c, aid) {
		return
	}
	id := c.Param("id")

	var row model.LLMEndpoint
	if err := model.DB.Where("id = ?", id).First(&row).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false,
			"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
		return
	}

	var req createOrUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	// Apply changes — treat blank strings as "unchanged" except where
	// blank is meaningful (BaseURL is allowed to be cleared so it
	// falls back to the provider's canonical URL).
	updates := map[string]any{}
	if name := strings.TrimSpace(req.Name); name != "" && name != row.Name {
		var dup int64
		model.DB.Model(&model.LLMEndpoint{}).Where("name = ? AND id <> ?", name, id).Count(&dup)
		if dup > 0 {
			c.JSON(http.StatusConflict, gin.H{"success": false,
				"error": gin.H{"code": "NAME_TAKEN", "message": "another endpoint already uses this name"}})
			return
		}
		updates["name"] = name
	}
	if f := strings.ToLower(strings.TrimSpace(req.Format)); f != "" && f != row.Format {
		if !isValidEndpointFormat(f) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false,
				"error": gin.H{"code": "INVALID_PARAMS", "message": "format must be 'anthropic', 'openai' or 'openai_compatible'"}})
			return
		}
		updates["format"] = f
	}
	// BaseURL: empty string IS a legitimate edit (reset to canonical).
	// Distinguish "field not sent at all" from "sent as empty" via the
	// presence of a raw JSON key. c.ShouldBindJSON doesn't surface
	// that distinction with the current struct shape; mitigate by
	// always writing what the client sent.
	updates["base_url"] = req.BaseURL
	if req.APIKey != "" {
		updates["api_key"] = req.APIKey
	}
	if req.Models != nil {
		modelsJSON, err := llm.EncodeModelsJSON(req.Models)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false,
				"error": gin.H{"code": "INVALID_PARAMS", "message": "bad models payload: " + err.Error()}})
			return
		}
		updates["models"] = modelsJSON
	}
	if req.DefaultModel != "" {
		updates["default_model"] = req.DefaultModel
	}
	if s := strings.TrimSpace(req.Status); s != "" && s != row.Status {
		if s != "active" && s != "disabled" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false,
				"error": gin.H{"code": "INVALID_PARAMS", "message": "status must be 'active' or 'disabled'"}})
			return
		}
		updates["status"] = s
	}

	if err := model.DB.Model(&row).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false,
			"error": gin.H{"code": "DB_ERROR", "message": err.Error()}})
		return
	}
	// Re-read to get the post-update UpdatedAt and any DB-side default
	// application.
	model.DB.Where("id = ?", id).First(&row)

	if err := llm.LoadEndpoint(row.ID); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data":    toResponse(&row),
			"warning": "endpoint saved but failed to reload: " + err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": toResponse(&row)})
}

// Delete is a soft delete — flips status to disabled and pulls from
// registry. Hard delete would break audit links from RoleOverride.
// Operators that really want the row gone can DELETE twice: first
// call disables, second call (observing status=disabled) hard-deletes.
// Implementation here: first call sets disabled; second call (if row
// is already disabled) removes the row entirely.
func (h *LLMEndpointHandler) Delete(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	aid, _ := agentID.(string)
	if !requireHuman(c, aid) {
		return
	}
	id := c.Param("id")
	var row model.LLMEndpoint
	if err := model.DB.Where("id = ?", id).First(&row).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false,
			"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
		return
	}

	if row.Status == "disabled" {
		// Hard delete path.
		if err := model.DB.Delete(&row).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false,
				"error": gin.H{"code": "DB_ERROR", "message": err.Error()}})
			return
		}
		llm.RemoveEndpoint(id)
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"deleted": "hard"}})
		return
	}

	// Soft delete — disable + pull from registry.
	if err := model.DB.Model(&row).Update("status", "disabled").Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false,
			"error": gin.H{"code": "DB_ERROR", "message": err.Error()}})
		return
	}
	llm.RemoveEndpoint(id)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"deleted": "soft"}})
}

// Test dispatches a 1-token completion request to confirm the endpoint
// is reachable with the current credentials. Returns the full error
// from the provider on failure — worth the information leak because
// this is a human-only, intentionally-diagnostic endpoint.
func (h *LLMEndpointHandler) Test(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	aid, _ := agentID.(string)
	if !requireHuman(c, aid) {
		return
	}
	id := c.Param("id")
	entry, err := llm.DefaultRegistry.Get(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false,
			"error": gin.H{"code": "NOT_REGISTERED", "message": err.Error()}})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	modelID := entry.DefaultModel
	if q := c.Query("model"); q != "" {
		modelID = q
	}
	if modelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "NO_MODEL", "message": "endpoint has no default model; pass ?model=... to test a specific one"}})
		return
	}
	ch, err := entry.Provider.ChatStream(ctx, llm.ChatRequest{
		Model:     modelID,
		MaxTokens: 8,
		Messages:  []llm.Message{llm.NewUserText("ping")},
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false,
			"error": gin.H{"code": "PROVIDER_ERROR", "message": err.Error()}})
		return
	}

	// Drain the stream. Success = we see an EvMessageStop; failure =
	// EvError with a concrete message.
	var text strings.Builder
	var lastErr error
	var usage llm.Usage
	for ev := range ch {
		switch ev.Type {
		case llm.EvTextDelta:
			text.WriteString(ev.TextDelta)
		case llm.EvMessageStop:
			usage = ev.Usage
		case llm.EvError:
			lastErr = ev.Err
		}
	}
	if lastErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false,
			"error": gin.H{"code": "PROVIDER_ERROR", "message": lastErr.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"reply_preview": text.String(),
			"usage":         usage,
			"model":         modelID,
		},
	})
}
