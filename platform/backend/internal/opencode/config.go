// Package opencode — on-disk config reader.
//
// Opencode keeps its provider/model catalogue in
// ~/.config/opencode/opencode.json. The shape we care about is:
//
//   {
//     "provider": {
//       "<provider-id>": {
//         "name":  "Friendly Name",
//         "npm":   "@ai-sdk/openai-compatible",
//         "options": { "baseURL": "...", "apiKey": "..." },
//         "models": {
//           "<model-id>": {
//             "name": "Friendly Model Name",
//             "limit": { "context": 1048576, "output": 131072 }
//           }
//         }
//       }
//     }
//   }
//
// The agent pool needs these provider + model ids (not the
// platform's own LLMEndpoint rows) because pool agents pin their
// session on whatever the local opencode serve recognises. This
// reader exposes the list in a format the spawn dashboard can
// render as a dropdown.
//
// Workspace overrides (.opencode/opencode.json inside a project
// checkout) are NOT read here — we're advertising the catalogue
// the operator's shell would use, not what a specific project
// has overridden. If that ever becomes useful we can take a
// workspace path on the Load call and merge.

package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ConfigProvider is the flattened view of one provider block. We
// strip auth details (apiKey) before surfacing anything to the
// dashboard — the config file sits on disk with the secrets, but
// the HTTP layer must not echo them back.
type ConfigProvider struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	NPM    string          `json:"npm,omitempty"`
	Models []ConfigModel   `json:"models"`
}

// ConfigModel carries enough for the UI to show a friendly name
// and the total context window.
type ConfigModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Context int64  `json:"context,omitempty"`
	Output  int64  `json:"output,omitempty"`
}

// DefaultConfigPath resolves ~/.config/opencode/opencode.json in a
// way that works on Windows (no ~ expansion) and Unix. Callers can
// override via LoadFromPath for tests.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "opencode.json"), nil
}

// LoadProviders reads the default opencode config file and returns
// its provider catalogue, sorted by id for deterministic UI. A
// missing config file is NOT an error — returns an empty list so
// the spawn modal can fall back to free-text input. Parse errors
// are surfaced so operators can fix the file instead of silently
// getting an empty dropdown.
func LoadProviders() ([]ConfigProvider, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadProvidersFromPath(path)
}

// LoadProvidersFromPath is the test seam. Missing path = empty list,
// not error; malformed JSON = error.
func LoadProvidersFromPath(path string) ([]ConfigProvider, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read opencode config %s: %w", path, err)
	}

	// Parse into a loose shape — opencode's schema carries dozens
	// of unrelated top-level keys (permission, keybinds, etc.) and
	// locking our struct to all of them would make the reader
	// fragile across opencode upgrades.
	var doc struct {
		Provider map[string]struct {
			Name   string `json:"name"`
			NPM    string `json:"npm"`
			Models map[string]struct {
				Name  string `json:"name"`
				Limit struct {
					Context int64 `json:"context"`
					Output  int64 `json:"output"`
				} `json:"limit"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse opencode config: %w", err)
	}

	out := make([]ConfigProvider, 0, len(doc.Provider))
	for id, p := range doc.Provider {
		friendly := p.Name
		if friendly == "" {
			friendly = id
		}
		models := make([]ConfigModel, 0, len(p.Models))
		for mid, m := range p.Models {
			mname := m.Name
			if mname == "" {
				mname = mid
			}
			models = append(models, ConfigModel{
				ID:      mid,
				Name:    mname,
				Context: m.Limit.Context,
				Output:  m.Limit.Output,
			})
		}
		// Sort models by id for UI stability too. Upstream JSON is
		// unordered; a stable order keeps the dropdown from
		// reshuffling between reloads.
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

		out = append(out, ConfigProvider{
			ID:     id,
			Name:   friendly,
			NPM:    p.NPM,
			Models: models,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
