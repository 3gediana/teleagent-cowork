package runner

// Builtin file tools (Phase 1 cont'd): grep + edit.
// Pair to builtin_read.go; shares sandboxPath() defined there.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ---- grep tool ------------------------------------------------------

// GrepTool searches for a regex inside project files. Honours the same
// ignore-list as GlobTool (.git, node_modules, vendor, etc.) to keep
// results focused on the actual codebase.
type GrepTool struct{}

func (GrepTool) Name() string { return "grep" }

// IsConcurrencySafe: read-only regex scan across files.
func (GrepTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (GrepTool) Description() string {
	return "Search project files for a regex pattern. Returns up to 200 matches as 'path:line:text' lines. Binary files and common junk dirs (.git, node_modules, vendor) are skipped."
}

func (GrepTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regular expression (Go RE2 syntax). Use (?i) for case-insensitive.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional: search within this subdirectory instead of the project root.",
			},
			"glob": map[string]any{
				"type":        "string",
				"description": "Optional: only search files whose path matches this glob (e.g. '*.go').",
			},
		},
		"required": []string{"pattern"},
	}
}

type grepInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Glob    string `json:"glob,omitempty"`
}

func (GrepTool) Execute(ctx context.Context, sess *RunnerSession, raw json.RawMessage) (string, bool, error) {
	var in grepInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Sprintf("Error: invalid arguments: %v", err), true, nil
	}
	if in.Pattern == "" {
		return "Error: pattern is required", true, nil
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return fmt.Sprintf("Error: bad regex: %v", err), true, nil
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

	// Honour optional subpath.
	searchRoot := absRoot
	if in.Path != "" {
		sp, spErr := sandboxPath(projectPath, in.Path)
		if spErr != nil {
			return fmt.Sprintf("Error: %v", spErr), true, nil
		}
		searchRoot = sp
	}

	const maxMatches = 200
	const maxLineLen = 500 // clamp super-long lines so one minified file can't swamp output

	var out []string
	walkErr := filepath.WalkDir(searchRoot, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
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
		rel = filepath.ToSlash(rel)

		if in.Glob != "" {
			var matched bool
			if strings.Contains(in.Glob, "**") {
				matched = matchDoubleStar(in.Glob, rel)
			} else {
				m, _ := filepath.Match(in.Glob, rel)
				if !m {
					// also try matching just the basename for patterns like "*.go"
					m2, _ := filepath.Match(in.Glob, filepath.Base(rel))
					m = m2
				}
				matched = m
			}
			if !matched {
				return nil
			}
		}

		f, ferr := os.Open(p)
		if ferr != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		// Bigger line buffer for files with no newlines (JSON, minified JS).
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			// Skip binary-looking content (heuristic: any NUL byte in
			// the first matched line). Bail out of the whole file.
			if strings.Contains(line, "\x00") {
				return nil
			}
			if re.MatchString(line) {
				if len(line) > maxLineLen {
					line = line[:maxLineLen] + "…"
				}
				out = append(out, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
				if len(out) >= maxMatches {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil && ctx.Err() == nil {
		return fmt.Sprintf("Error: walk: %v", walkErr), true, nil
	}
	if len(out) == 0 {
		return "(no matches)", false, nil
	}
	if len(out) >= maxMatches {
		out = append(out, fmt.Sprintf("… (stopped at %d matches; narrow pattern or add glob=\"*.ext\")", maxMatches))
	}
	return strings.Join(out, "\n"), false, nil
}

// ---- edit tool ------------------------------------------------------

// EditTool performs a single string-level replacement inside a file.
// Chose this surface over "write the whole file" because it's far
// safer in practice — large files already blow the context window,
// and the model doing a round-trip diff invites regressions. The
// deterministic "one exact replacement" rule also makes edits easy to
// audit in the journal.
//
// For creating a new file, use an empty OldText and pass the full
// desired contents as NewText.
type EditTool struct{}

func (EditTool) Name() string { return "edit" }

// IsConcurrencySafe: NEVER. Edits the filesystem. Two concurrent
// edits to the same file would race (last writer wins). Even edits to
// different files in parallel are serialised because the filesystem
// as a whole is shared state the runner can't guard.
func (EditTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (EditTool) Description() string {
	return "Make a targeted edit to a file. Requires old_text that appears EXACTLY ONCE in the file (for safe replacement), or leave old_text empty to create a new file with the given new_text. PRECONDITION: you must call 'read' on the file first in this session before editing it — edits to un-read files are refused (prevents guessing at contents). New-file creation (empty old_text) is exempt."
}

func (EditTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path (relative to project root or absolute within it).",
			},
			"old_text": map[string]any{
				"type":        "string",
				"description": "Exact text to replace. MUST appear exactly once in the file. Empty = create new file.",
			},
			"new_text": map[string]any{
				"type":        "string",
				"description": "Replacement text. For file creation, this is the full contents.",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "Optional: allow replacing every occurrence of old_text (default false). Use only when renaming something like a variable.",
			},
		},
		"required": []string{"path", "new_text"},
	}
}

type editInput struct {
	Path       string `json:"path"`
	OldText    string `json:"old_text"`
	NewText    string `json:"new_text"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func (EditTool) Execute(ctx context.Context, sess *RunnerSession, raw json.RawMessage) (string, bool, error) {
	var in editInput
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

	// Create-new-file path: OldText empty means "write fresh file".
	// Refuse if the file already exists — a silent overwrite has
	// been the source of too many incident post-mortems elsewhere.
	if in.OldText == "" {
		if _, statErr := os.Stat(abs); statErr == nil {
			return fmt.Sprintf("Error: %s already exists; pass a non-empty old_text to modify an existing file", in.Path), true, nil
		}
		if mkErr := os.MkdirAll(filepath.Dir(abs), 0o755); mkErr != nil {
			return fmt.Sprintf("Error: mkdir: %v", mkErr), true, nil
		}
		if wErr := writeAtomic(abs, []byte(in.NewText)); wErr != nil {
			return fmt.Sprintf("Error: write: %v", wErr), true, nil
		}
		return fmt.Sprintf("Created %s (%d bytes)", in.Path, len(in.NewText)), false, nil
	}

	// Modify-existing-file path.
	//
	// Precondition: the file must have been read in this session.
	// Copied from Claude Code — stops the model from overwriting files
	// based on hallucinated content. A read teaches it what's actually
	// there; without one, an edit is a guess. New-file creation
	// (OldText=="" above) is naturally exempt.
	if !sess.HasRead(abs) {
		return fmt.Sprintf(
			"Error: cannot edit %s — you must read it first. Call the 'read' tool on %s, then retry this edit. This prevents edits based on unverified assumptions about file contents.",
			in.Path, in.Path,
		), true, nil
	}

	orig, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), true, nil
	}
	content := string(orig)
	count := strings.Count(content, in.OldText)
	switch {
	case count == 0:
		return fmt.Sprintf("Error: old_text not found in %s (check whitespace, the content must match EXACTLY including indentation)", in.Path), true, nil
	case count > 1 && !in.ReplaceAll:
		return fmt.Sprintf("Error: old_text appears %d times in %s — include more surrounding context to make it unique, or pass replace_all=true to intentionally replace every occurrence", count, in.Path), true, nil
	}

	var updated string
	if in.ReplaceAll {
		updated = strings.ReplaceAll(content, in.OldText, in.NewText)
	} else {
		updated = strings.Replace(content, in.OldText, in.NewText, 1)
	}
	if updated == content {
		return fmt.Sprintf("Error: no-op edit (old_text and new_text produce identical file content)"), true, nil
	}
	if err := writeAtomic(abs, []byte(updated)); err != nil {
		return fmt.Sprintf("Error: write: %v", err), true, nil
	}

	// Keep the success message compact: path + change stats +
	// first N chars of diff context. The full diff lives in the
	// journal for post-run inspection.
	delta := len(updated) - len(orig)
	sign := "+"
	if delta < 0 {
		sign = ""
	}
	occurrences := count
	if !in.ReplaceAll {
		occurrences = 1
	}
	return fmt.Sprintf("Edited %s (%s%d bytes, %d occurrence(s) replaced)", in.Path, sign, delta, occurrences), false, nil
}

// writeAtomic writes data to path via a temp-file + rename dance so a
// partial write can't corrupt the original on crash.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".edit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup if rename failed.
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
