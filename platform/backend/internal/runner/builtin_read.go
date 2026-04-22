package runner

// Builtin file tools (Phase 1): read + glob.
// Every file tool is sandboxed to the session's project path — an
// attempted traversal outside that root returns an error back to the
// model (never panics, never silently succeeds).

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// sandboxPath enforces the "stay inside ProjectPath" invariant. Every
// tool call that accepts a path MUST route it through here before
// touching the filesystem. Returns the absolute, cleaned path on
// success; otherwise returns a user-readable error string.
//
// Rules:
//   - Input must be either an absolute path within ProjectPath, or a
//     relative path (which is resolved against ProjectPath).
//   - Symlinks that resolve outside ProjectPath are rejected.
//   - The path itself doesn't need to exist; callers may probe.
func sandboxPath(projectPath, input string) (string, error) {
	if projectPath == "" {
		return "", fmt.Errorf("project path not set on session; tool refused")
	}
	absRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return "", fmt.Errorf("bad project path: %v", err)
	}
	absRoot = filepath.Clean(absRoot)

	var target string
	if filepath.IsAbs(input) {
		target = filepath.Clean(input)
	} else {
		target = filepath.Clean(filepath.Join(absRoot, input))
	}

	// Rel computes the relative path from root to target. If it
	// starts with ".." or a drive letter, target is outside root.
	rel, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", fmt.Errorf("path escape: %v", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside project root", input)
	}

	// Follow symlinks if the target exists; tolerate non-existent
	// paths (e.g. for "edit" to create a new file).
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, rerr := filepath.EvalSymlinks(target)
		if rerr == nil {
			rel2, rerr2 := filepath.Rel(absRoot, resolved)
			if rerr2 == nil && (rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator))) {
				return "", fmt.Errorf("symlink %q escapes project root", input)
			}
		}
	}
	return target, nil
}

// ---- read tool ------------------------------------------------------

// ReadTool returns the contents of a single text file. Truncates to
// MaxBytes (default 256 KiB) to avoid feeding a runaway file into the
// context window. For larger needs, caller should use grep or read
// with an offset/limit window.
type ReadTool struct{}

func (ReadTool) Name() string { return "read" }

// IsConcurrencySafe: pure read, no state mutation. Always safe to run
// alongside any number of other safe tools.
func (ReadTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (ReadTool) Description() string {
	return "Read the contents of a text file inside the project. Truncates to 256 KiB; use offset/limit for larger files. Returns the file content; on error, returns a message starting with 'Error:' and is_error=true."
}

func (ReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path. Relative is resolved against project root; absolute must be inside project root.",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Optional 1-indexed starting line. Default 1.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Optional max number of lines to return. Default: everything (subject to 256 KiB cap).",
			},
		},
		"required": []string{"path"},
	}
}

type readInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func (ReadTool) Execute(ctx context.Context, sess *RunnerSession, raw json.RawMessage) (string, bool, error) {
	var in readInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Sprintf("Error: invalid arguments: %v", err), true, nil
	}
	if in.Path == "" {
		return "Error: path is required", true, nil
	}
	projectPath := ""
	if sess.AgentSession != nil && sess.AgentSession.Context != nil {
		projectPath = sess.AgentSession.Context.ProjectPath
	}
	abs, err := sandboxPath(projectPath, in.Path)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), true, nil
	}

	const maxBytes = 256 * 1024
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), true, nil
	}

	// Register the read. EditTool checks this set before allowing a
	// modification, so the model can't edit files it never inspected.
	// Partial reads (offset/limit) still count — matching Claude
	// Code's behaviour, the precondition is "has ever been read",
	// not "has been read in full".
	sess.MarkRead(abs)

	// Apply offset/limit in line space if requested.
	if in.Offset > 0 || in.Limit > 0 {
		lines := strings.Split(string(data), "\n")
		start := in.Offset - 1
		if start < 0 {
			start = 0
		}
		if start > len(lines) {
			start = len(lines)
		}
		end := len(lines)
		if in.Limit > 0 && start+in.Limit < end {
			end = start + in.Limit
		}
		slice := strings.Join(lines[start:end], "\n")
		if len(slice) > maxBytes {
			slice = slice[:maxBytes] + "\n… (truncated; response exceeded 256 KiB)"
		}
		return slice, false, nil
	}

	if len(data) > maxBytes {
		return string(data[:maxBytes]) + "\n… (truncated; file exceeds 256 KiB — use offset/limit or grep)", false, nil
	}
	return string(data), false, nil
}

// ---- glob tool ------------------------------------------------------

// GlobTool lists files matching a pattern inside the project. Uses
// Go's filepath.Glob semantics (not shell recursion) extended with a
// simple `**` handling for recursive walks.
type GlobTool struct{}

func (GlobTool) Name() string { return "glob" }

// IsConcurrencySafe: pure directory walk, no mutation.
func (GlobTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (GlobTool) Description() string {
	return "Find files matching a glob pattern. Supports '*' (within a path segment), '?' (single char), and '**' (any number of directories). Returns newline-separated paths relative to project root; at most 500 results."
}

func (GlobTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Glob pattern, e.g. '**/*.go' or 'src/*.ts'.",
			},
		},
		"required": []string{"pattern"},
	}
}

type globInput struct {
	Pattern string `json:"pattern"`
}

func (GlobTool) Execute(ctx context.Context, sess *RunnerSession, raw json.RawMessage) (string, bool, error) {
	var in globInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Sprintf("Error: invalid arguments: %v", err), true, nil
	}
	if in.Pattern == "" {
		return "Error: pattern is required", true, nil
	}
	projectPath := ""
	if sess.AgentSession != nil && sess.AgentSession.Context != nil {
		projectPath = sess.AgentSession.Context.ProjectPath
	}
	if projectPath == "" {
		return "Error: project path not set on session", true, nil
	}
	absRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return fmt.Sprintf("Error: bad project root: %v", err), true, nil
	}

	// filepath.Glob doesn't understand `**`. Walk + match is correct
	// and cheap for the project sizes we deal with (≤100k files).
	// We split the pattern on `**` and do a prefix walk + suffix match.
	hasDoubleStar := strings.Contains(in.Pattern, "**")

	matches := make([]string, 0, 64)
	err = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries rather than aborting
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			// Skip vendored + bulky dirs that would dominate output.
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".idea" || name == ".vscode" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(absRoot, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel) // normalise on Windows

		var matched bool
		if hasDoubleStar {
			matched = matchDoubleStar(in.Pattern, rel)
		} else {
			m, _ := filepath.Match(in.Pattern, rel)
			matched = m
		}
		if matched {
			matches = append(matches, rel)
			if len(matches) >= 500 {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && ctx.Err() == nil {
		return fmt.Sprintf("Error: walk: %v", err), true, nil
	}
	if len(matches) == 0 {
		return "(no matches)", false, nil
	}
	return strings.Join(matches, "\n"), false, nil
}

// matchDoubleStar implements the minimal `**` extension to glob.
// Splits the pattern on `**/` and checks that each segment appears
// in order in the target path. More permissive than true ant-style
// globbing, but sufficient for everyday cases like `**/*.go`.
func matchDoubleStar(pattern, path string) bool {
	parts := strings.Split(pattern, "**/")
	cursor := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i == len(parts)-1 {
			// Last piece: pattern-match against the tail.
			// If the tail contains a `/`, match the full trailing
			// path; otherwise match just the filename.
			if strings.Contains(part, "/") {
				m, _ := filepath.Match(part, path[cursor:])
				return m
			}
			base := filepath.Base(path)
			m, _ := filepath.Match(part, base)
			return m
		}
		idx := strings.Index(path[cursor:], part)
		if idx == -1 {
			return false
		}
		cursor += idx + len(part)
	}
	return true
}
