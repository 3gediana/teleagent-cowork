package agentpool

// prepareOpencodeDir materialises a clean `.opencode/` directory inside
// a pool agent's workDir so the subprocess `opencode serve` boots
// without hitting the `@opencode-ai/plugin` -> zod v4 incompatibility
// documented in anomalyco/opencode#20807 and #12336.
//
// Why this exists: opencode 1.14.21's `resolveTools()` feeds tool
// schemas through `zodToJsonSchema()` from zod-to-json-schema@3.24.5,
// which is *incompatible with zod v4*. `@opencode-ai/plugin@1.14.21`
// pulls in zod@4.1.8 transitively — the instant that lands in any
// `.opencode/node_modules/zod`, every prompt_async / message request
// crashes with `TypeError: undefined is not an object (n._zod.def)`
// and the assistant message comes back with `parts=0`.
//
// We therefore refuse to ship `@opencode-ai/plugin` into pool agents.
// Each pool agent only needs `@ai-sdk/openai-compatible` (for the
// minimax-coding-plan provider) plus zod@3 to keep the provider
// schemas out of the zod v4 code path.
//
// To keep spawn latency bounded we prime a single shared *template*
// directory once (npm install runs there) and hard-link / junction /
// copy it into each agent's workDir. The template lives under the
// pool root so it's cleaned up with the pool.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// opencodeTemplatePackageJSON is the minimal package.json we ship
// into each agent's `.opencode`. Deliberately excludes
// `@opencode-ai/plugin` — see file-level comment for why.
const opencodeTemplatePackageJSON = `{
  "name": "a3c-pool-agent-opencode",
  "private": true,
  "dependencies": {
    "@ai-sdk/openai-compatible": "1.0.24",
    "zod": "3.25.76"
  }
}
`

// prepareMu serialises template priming so concurrent Spawn() calls
// don't race each other into `npm install`.
var prepareMu sync.Mutex

// templateReady caches the fact that we've already materialised the
// shared template for the current process. A bool is enough because
// we never invalidate — if the template directory gets corrupted the
// operator should restart the platform.
var templateReady bool

// prepareOpencodeDir makes sure `<workDir>/.opencode/` exists and has
// a working `node_modules` containing `@ai-sdk/openai-compatible` and
// zod v3, *without* `@opencode-ai/plugin`. Called from the spawner
// before `opencode serve` boots.
//
// Layout after a successful call:
//
//	<workDir>/.opencode/
//	  package.json
//	  node_modules/
//	    @ai-sdk/openai-compatible/...
//	    zod/ (v3)
//
// poolRoot is where we keep the shared template across agents.
func prepareOpencodeDir(workDir, poolRoot string) error {
	if workDir == "" {
		return fmt.Errorf("prepareOpencodeDir: workDir is empty")
	}
	if poolRoot == "" {
		return fmt.Errorf("prepareOpencodeDir: poolRoot is empty")
	}

	targetDir := filepath.Join(workDir, ".opencode")
	templateDir := filepath.Join(poolRoot, ".opencode-template")

	// 1. Make sure the template exists. This is expensive the first
	// time (npm install) but cheap on subsequent calls.
	if err := ensureOpencodeTemplate(templateDir); err != nil {
		return fmt.Errorf("ensure template: %w", err)
	}

	// 2. Nuke any stale `.opencode` — especially ones containing
	// `@opencode-ai/plugin` carried over from an earlier opencode
	// TUI session or manual testing. Leaving those in place would
	// re-trigger the zod v4 crash we're trying to avoid.
	if stale, reason := opencodeDirIsPoisoned(targetDir); stale {
		log.Printf("[Pool] %s is poisoned (%s); removing before spawn", targetDir, reason)
		if err := os.RemoveAll(targetDir); err != nil {
			return fmt.Errorf("remove stale .opencode: %w", err)
		}
	}

	// 3. If target already exists and looks healthy, nothing to do.
	if opencodeDirLooksHealthy(targetDir) {
		return nil
	}

	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("mkdir workDir: %w", err)
	}

	// 4. Copy the template contents into target. We deliberately
	// copy (not junction) so each agent can mutate its own
	// `.opencode/opencode.json`, staging area, etc. without
	// stepping on siblings. node_modules copy is fast on NTFS/APFS
	// once the file cache is warm.
	if err := copyDir(templateDir, targetDir); err != nil {
		return fmt.Errorf("copy template -> target: %w", err)
	}
	return nil
}

// opencodeDirIsPoisoned detects a `.opencode` tree that would make
// opencode 1.14.21 crash on prompt. The two known triggers are:
//
//   - Presence of `@opencode-ai/plugin` (brings in zod@4).
//   - A top-level `zod/package.json` reporting any v4.x version.
//
// Returns (true, reasonString) if poisoned; (false, "") otherwise.
func opencodeDirIsPoisoned(dir string) (bool, string) {
	if _, err := os.Stat(dir); err != nil {
		return false, ""
	}
	pluginDir := filepath.Join(dir, "node_modules", "@opencode-ai", "plugin")
	if fi, err := os.Stat(pluginDir); err == nil && fi.IsDir() {
		return true, "contains @opencode-ai/plugin (pulls zod v4)"
	}
	zodPkg := filepath.Join(dir, "node_modules", "zod", "package.json")
	data, err := os.ReadFile(zodPkg)
	if err == nil {
		if containsV4Version(data) {
			return true, "zod is v4.x"
		}
	}
	return false, ""
}

// containsV4Version is a lazy byte scan — good enough for the one
// line we care about ("version": "4.x.x") without a full JSON parse.
// Returns true iff we see `"version"` followed by a v4 string on the
// same line.
func containsV4Version(data []byte) bool {
	// Cheapest signal: if the bytes literally contain "4." as part
	// of a `"version": "4.*"` declaration we treat it as v4. This
	// mis-reports v14/v24 but we never ship those and the recovery
	// path (nuke + recopy) is cheap even on a false positive.
	needle := []byte(`"version": "4.`)
	needle2 := []byte(`"version":"4.`)
	for _, probe := range [][]byte{needle, needle2} {
		if indexOf(data, probe) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(haystack, needle []byte) int {
	// Byte-level search; avoids importing bytes just for Index.
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// opencodeDirLooksHealthy is the positive side of opencodeDirIsPoisoned:
// a `.opencode` we're willing to reuse in-place rather than recopying.
// We require the three markers that opencode actually reads at boot.
func opencodeDirLooksHealthy(dir string) bool {
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	pkgMarker := filepath.Join(dir, "package.json")
	openaiMarker := filepath.Join(dir, "node_modules", "@ai-sdk", "openai-compatible", "package.json")
	zodMarker := filepath.Join(dir, "node_modules", "zod", "package.json")
	for _, p := range []string{pkgMarker, openaiMarker, zodMarker} {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	// Zod must be v3. (We already confirmed no @opencode-ai/plugin
	// via the poisoned check above before this is called.)
	data, err := os.ReadFile(zodMarker)
	if err != nil {
		return false
	}
	return !containsV4Version(data)
}

// ensureOpencodeTemplate materialises the shared template once. If
// the directory is already populated it's a no-op. Concurrent
// callers serialise on prepareMu so the first one pays the cost and
// the rest see `templateReady == true`.
func ensureOpencodeTemplate(templateDir string) error {
	prepareMu.Lock()
	defer prepareMu.Unlock()

	if templateReady && opencodeDirLooksHealthy(templateDir) {
		return nil
	}

	// Wipe any partial/poisoned template. Without this the first
	// operator who ever ran `opencode` in this repo leaves behind
	// `@opencode-ai/plugin` in the pool root, and every future
	// spawn inherits the zod v4 crash.
	if poisoned, reason := opencodeDirIsPoisoned(templateDir); poisoned {
		log.Printf("[Pool] opencode template at %s is poisoned (%s); rebuilding", templateDir, reason)
		if err := os.RemoveAll(templateDir); err != nil {
			return fmt.Errorf("remove poisoned template: %w", err)
		}
	}

	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		return fmt.Errorf("mkdir template: %w", err)
	}

	pkgJSONPath := filepath.Join(templateDir, "package.json")
	if err := os.WriteFile(pkgJSONPath, []byte(opencodeTemplatePackageJSON), 0o644); err != nil {
		return fmt.Errorf("write template package.json: %w", err)
	}

	// Run npm install. We pin the versions in package.json above so
	// this is deterministic across operators (no peerDep drift
	// pulling zod v4 back in).
	if err := runNpmInstall(templateDir); err != nil {
		return fmt.Errorf("npm install in template: %w", err)
	}

	// Double-check the outcome — npm's peerDependency resolver is
	// notorious for silently upgrading zod to the latest satisfying
	// v4 release even when the direct dep pins v3. If we detect
	// that, bail loudly so the operator can diagnose instead of
	// shipping a broken template to every agent.
	if poisoned, reason := opencodeDirIsPoisoned(templateDir); poisoned {
		return fmt.Errorf("template is still poisoned after npm install (%s); check npm registry / registry mirror", reason)
	}
	if !opencodeDirLooksHealthy(templateDir) {
		return fmt.Errorf("template failed healthy check after npm install")
	}

	templateReady = true
	log.Printf("[Pool] opencode template primed at %s", templateDir)
	return nil
}

// runNpmInstall executes `npm install --silent --no-audit --no-fund`
// in the given directory. We keep the flags minimal so the operator
// sees real errors (like registry timeouts) but suppress the usual
// npm marketing chatter.
func runNpmInstall(dir string) error {
	npmBin := "npm"
	if runtime.GOOS == "windows" {
		npmBin = "npm.cmd"
	}
	cmd := exec.Command(npmBin, "install", "--silent", "--no-audit", "--no-fund")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s install: %w", npmBin, err)
	}
	return nil
}

// copyDir / copyFile helpers live in skills.go — we reuse them for
// template hydration since the semantics (recursive, mode-preserving,
// same package) are identical.
