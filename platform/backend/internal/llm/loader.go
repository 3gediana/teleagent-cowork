package llm

// Loader: turns model.LLMEndpoint rows into live Entries in the
// Registry. Called once at server startup; callers also invoke
// LoadEndpoint / RemoveEndpoint from the handler layer whenever the
// dashboard changes an endpoint — this is what makes edits visible at
// runtime without a restart.
//
// Package boundary note: this file imports model.* because the Loader
// is fundamentally an adapter between the DB row shape and the Registry
// Entry shape. That's the only reason internal/llm touches internal/model.

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/a3c/platform/internal/model"
)

// LoadAll is called once at startup from main.go after the DB is up.
// It reads every active LLMEndpoint row, constructs a Provider adapter
// for each, and registers them into DefaultRegistry.
//
// Errors on individual rows are logged and skipped, not fatal: a
// deployment with four endpoints configured should keep booting even
// if one row has been corrupted — the others still work, and the
// operator sees the broken one in the dashboard with its Status
// column reflecting the error.
func LoadAll() int {
	var rows []model.LLMEndpoint
	if err := model.DB.Where("status = ?", "active").Find(&rows).Error; err != nil {
		log.Printf("[LLM] LoadAll: query failed: %v", err)
		return 0
	}
	n := 0
	for i := range rows {
		if err := installEntry(&rows[i]); err != nil {
			log.Printf("[LLM] skip endpoint %s (%s): %v", rows[i].ID, rows[i].Name, err)
			continue
		}
		n++
	}
	log.Printf("[LLM] Registry loaded %d endpoint(s)", n)
	return n
}

// LoadEndpoint is the single-row variant used by the handler after a
// POST/PUT. Re-reads the row (so the caller only has to commit) and
// swaps it into the Registry. Safe to call with an already-registered
// id — Registry.Register overwrites.
func LoadEndpoint(endpointID string) error {
	var row model.LLMEndpoint
	if err := model.DB.Where("id = ?", endpointID).First(&row).Error; err != nil {
		return fmt.Errorf("llm: load endpoint %s: %w", endpointID, err)
	}
	if row.Status != "active" {
		// Disabled rows stay in the DB for audit but don't route.
		// Removing (rather than installing) makes the disable action
		// take effect immediately.
		DefaultRegistry.Remove(endpointID)
		return nil
	}
	return installEntry(&row)
}

// RemoveEndpoint pulls a registered endpoint out of the Registry. The
// DB row is untouched — the handler handles persistence. Safe to call
// for ids that aren't currently registered (no-op).
func RemoveEndpoint(endpointID string) {
	DefaultRegistry.Remove(endpointID)
}

// installEntry is the shared core used by both LoadAll and LoadEndpoint.
// Parses the row's Models JSON, builds the right adapter for the row's
// Format, and pushes a fresh Entry into the registry.
func installEntry(row *model.LLMEndpoint) error {
	models, err := ParseModelsJSONStr(row.Models)
	if err != nil {
		return fmt.Errorf("bad models JSON: %w", err)
	}
	// Merge builtin pricing so unlisted fields fill in with sensible
	// defaults rather than zeros in cost computation.
	for i := range models {
		models[i] = MergePricing(models[i])
	}

	cfg := ProviderConfig{
		ID:      ProviderID(row.Format),
		Name:    row.Name,
		BaseURL: row.BaseURL,
		APIKey:  row.APIKey,
		Models:  models,
	}

	// openai_compatible is an alias for openai: same wire protocol,
	// different label so operators can distinguish first-party OpenAI
	// from compatible providers (DeepSeek, Zhipu, Moonshot, Together,
	// local vLLM, ...). Both route through NewOpenAIProvider; the
	// Format column keeps whichever the operator picked for display.
	var prov Provider
	switch strings.ToLower(row.Format) {
	case "anthropic":
		prov = NewAnthropicProvider(cfg)
	case "openai", "openai_compatible":
		prov = NewOpenAIProvider(cfg)
	default:
		return fmt.Errorf("unknown format %q (expected 'anthropic', 'openai' or 'openai_compatible')", row.Format)
	}

	// Default model fallback: if the operator didn't set one and the
	// endpoint exposes exactly one model, use that as the implicit
	// default. Saves one UI click for simple setups.
	defaultModel := row.DefaultModel
	if defaultModel == "" && len(models) == 1 {
		defaultModel = models[0].ID
	}

	DefaultRegistry.Register(&Entry{
		EndpointID:   row.ID,
		EndpointName: row.Name,
		Format:       ProviderID(row.Format),
		DefaultModel: defaultModel,
		Provider:     prov,
	})
	return nil
}

// ParseModelsJSONStr reads the Models column's JSON payload. The
// column is nullable + typically non-empty; empty strings and SQL
// NULL both parse to an empty slice, NOT an error — a fresh endpoint
// can be registered before any model ids are known (operator fills
// them in with a follow-up edit). Exported so handler code can share
// the tolerant parse semantics.
func ParseModelsJSONStr(raw string) ([]ModelInfo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var out []ModelInfo
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EncodeModelsJSON is the inverse — used by the handler when writing
// to the DB. Exported so handlers don't reinvent the wheel.
func EncodeModelsJSON(models []ModelInfo) (string, error) {
	if len(models) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(models)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
