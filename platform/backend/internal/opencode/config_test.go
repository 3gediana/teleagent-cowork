package opencode

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadProvidersFromPath_Sample — the reader should parse a
// representative opencode.json and expose the provider + model ids
// the spawn dashboard needs. Checks ordering (sorted by id) and
// friendly-name fallback.
func TestLoadProvidersFromPath_Sample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	sample := `{
	  "$schema": "https://opencode.ai/config.json",
	  "permission": "allow",
	  "provider": {
	    "minimax-coding-plan": {
	      "npm": "@ai-sdk/openai-compatible",
	      "name": "MiniMax Coding Plan",
	      "options": {"baseURL": "x", "apiKey": "SECRET"},
	      "models": {
	        "MiniMax-M2.7": {
	          "name": "MiniMax M2.7",
	          "limit": {"context": 1000000, "output": 131072}
	        }
	      }
	    },
	    "anthropic": {
	      "models": {
	        "claude-sonnet-4-5-20250929": {}
	      }
	    }
	  }
	}`
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	providers, err := LoadProvidersFromPath(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}
	// Sorted by id: anthropic < minimax-coding-plan
	if providers[0].ID != "anthropic" {
		t.Errorf("providers should be sorted by id, got %q first", providers[0].ID)
	}
	// Name falls back to id when the block omits "name".
	if providers[0].Name != "anthropic" {
		t.Errorf("missing name should fall back to id, got %q", providers[0].Name)
	}
	mmax := providers[1]
	if mmax.Name != "MiniMax Coding Plan" {
		t.Errorf("name carried wrong: %q", mmax.Name)
	}
	if len(mmax.Models) != 1 || mmax.Models[0].ID != "MiniMax-M2.7" {
		t.Errorf("model id missing: %+v", mmax.Models)
	}
	if mmax.Models[0].Context != 1_000_000 {
		t.Errorf("context limit missing: %d", mmax.Models[0].Context)
	}

	// Secrets must not be in the output. Cheap sanity check: no
	// field type carries apiKey so the struct naturally drops it.
	// If that ever regresses we'd catch it here by string scanning.
	for _, p := range providers {
		for _, m := range p.Models {
			if m.ID == "SECRET" || m.Name == "SECRET" {
				t.Error("secret leaked into model output")
			}
		}
	}
}

// TestLoadProvidersFromPath_Missing — a fresh install may not have
// the config file yet. Reader should return (nil, nil) so the
// dashboard falls back to free-text input instead of erroring.
func TestLoadProvidersFromPath_Missing(t *testing.T) {
	providers, err := LoadProvidersFromPath(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Errorf("missing file should not error, got %v", err)
	}
	if providers != nil {
		t.Errorf("missing file should yield nil slice, got %+v", providers)
	}
}

// TestLoadProvidersFromPath_Malformed — a typo'd config file is a
// real error the operator needs to see; don't silently swallow.
func TestLoadProvidersFromPath_Malformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, []byte(`{this is not json`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadProvidersFromPath(path); err == nil {
		t.Error("malformed json should surface as an error")
	}
}
