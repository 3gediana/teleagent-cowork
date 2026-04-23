package agentpool

// These tests verify the `.opencode/` template hydration logic. They
// DO run a real `npm install`, so they're gated behind the env var
// A3C_RUN_NPM_TESTS=1 to keep `go test ./...` fast by default.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareOpencodeDir_SeedsCleanTemplate(t *testing.T) {
	if os.Getenv("A3C_RUN_NPM_TESTS") != "1" {
		t.Skip("Set A3C_RUN_NPM_TESTS=1 to run the npm-dependent pool tests.")
	}
	// Reset module-level cache so each test starts from scratch.
	prepareMu.Lock()
	templateReady = false
	prepareMu.Unlock()

	poolRoot := t.TempDir()
	workDir := filepath.Join(poolRoot, "pool_test123")

	if err := prepareOpencodeDir(workDir, poolRoot); err != nil {
		t.Fatalf("prepareOpencodeDir: %v", err)
	}

	// Confirm the expected markers.
	openaiPkg := filepath.Join(workDir, ".opencode", "node_modules", "@ai-sdk", "openai-compatible", "package.json")
	if _, err := os.Stat(openaiPkg); err != nil {
		t.Errorf("@ai-sdk/openai-compatible not installed: %v", err)
	}
	zodPkg := filepath.Join(workDir, ".opencode", "node_modules", "zod", "package.json")
	data, err := os.ReadFile(zodPkg)
	if err != nil {
		t.Fatalf("zod package.json missing: %v", err)
	}
	if !strings.Contains(string(data), `"version": "3.`) {
		t.Errorf("expected zod v3, got:\n%s", data)
	}

	// And confirm the poison marker is absent.
	pluginDir := filepath.Join(workDir, ".opencode", "node_modules", "@opencode-ai", "plugin")
	if _, err := os.Stat(pluginDir); err == nil {
		t.Errorf("@opencode-ai/plugin leaked into .opencode — this would re-trigger the zod v4 crash")
	}
}

func TestOpencodeDirIsPoisoned_DetectsPluginAndV4(t *testing.T) {
	tmp := t.TempDir()

	// Empty dir is not poisoned.
	if poisoned, _ := opencodeDirIsPoisoned(filepath.Join(tmp, "does-not-exist")); poisoned {
		t.Error("non-existent dir should not report poisoned")
	}

	// Simulated @opencode-ai/plugin presence.
	pluginDir := filepath.Join(tmp, "node_modules", "@opencode-ai", "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if poisoned, reason := opencodeDirIsPoisoned(tmp); !poisoned || !strings.Contains(reason, "@opencode-ai/plugin") {
		t.Errorf("plugin presence should flag poisoned; got %v %q", poisoned, reason)
	}
	// Clean up and test the zod v4 case.
	_ = os.RemoveAll(filepath.Join(tmp, "node_modules"))

	zodPkg := filepath.Join(tmp, "node_modules", "zod")
	if err := os.MkdirAll(zodPkg, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(zodPkg, "package.json"), []byte(`{"name":"zod","version": "4.1.8"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if poisoned, reason := opencodeDirIsPoisoned(tmp); !poisoned || !strings.Contains(reason, "v4") {
		t.Errorf("zod v4 should flag poisoned; got %v %q", poisoned, reason)
	}

	// Rewrite with zod v3 — should NOT be poisoned.
	if err := os.WriteFile(filepath.Join(zodPkg, "package.json"), []byte(`{"name":"zod","version": "3.25.76"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if poisoned, _ := opencodeDirIsPoisoned(tmp); poisoned {
		t.Error("zod v3 should not flag poisoned")
	}
}

func TestContainsV4Version_Matches(t *testing.T) {
	cases := map[string]bool{
		`{"version": "4.1.8"}`:   true,
		`{"version":"4.3.6"}`:    true,
		`{"version": "3.25.76"}`: false,
		`{"version": "14.0.0"}`:  false, // four-point-prefix would be 14.x — doesn't match our needle
		``:                       false,
	}
	for input, want := range cases {
		if got := containsV4Version([]byte(input)); got != want {
			t.Errorf("containsV4Version(%q) = %v, want %v", input, got, want)
		}
	}
}
