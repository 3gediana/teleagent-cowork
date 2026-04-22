package runner

// Unit tests for the builtin file tools. No DB, no LLM — each test
// sets up a project-path dir in t.TempDir() and drives Execute
// directly. Exercise the sandbox guard first, then happy paths.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/a3c/platform/internal/agent"
)

// newSession returns a RunnerSession anchored at projectPath. The
// shape mirrors what the dispatcher hands the runner in production.
func newSession(projectPath string) *RunnerSession {
	return &RunnerSession{
		AgentSession: &agent.Session{
			Context: &agent.SessionContext{ProjectPath: projectPath},
		},
	}
}

// ---- sandbox -------------------------------------------------------

func TestSandboxPath_ResolvesRelativeWithinRoot(t *testing.T) {
	root := t.TempDir()
	got, err := sandboxPath(root, "src/main.go")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := filepath.Join(root, "src", "main.go")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSandboxPath_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../etc/passwd", "src/../../escape.txt"} {
		if _, err := sandboxPath(root, bad); err == nil {
			t.Errorf("path %q should be rejected", bad)
		}
	}
}

func TestSandboxPath_RejectsAbsoluteOutsideRoot(t *testing.T) {
	root := t.TempDir()
	var outside string
	if runtime.GOOS == "windows" {
		outside = `C:\Windows\System32\cmd.exe`
	} else {
		outside = "/etc/passwd"
	}
	if _, err := sandboxPath(root, outside); err == nil {
		t.Errorf("absolute %q should be rejected", outside)
	}
}

func TestSandboxPath_EmptyProjectPathRefuses(t *testing.T) {
	if _, err := sandboxPath("", "anything"); err == nil {
		t.Error("empty project path should error")
	}
}

// ---- ReadTool ------------------------------------------------------

func TestRead_HappyPath(t *testing.T) {
	dir := t.TempDir()
	body := "line one\nline two\nline three\n"
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := newSession(dir)
	result, isErr, fatal := ReadTool{}.Execute(context.Background(), sess,
		json.RawMessage(`{"path":"f.txt"}`))
	if fatal != nil || isErr {
		t.Fatalf("isErr=%v fatal=%v result=%q", isErr, fatal, result)
	}
	if result != body {
		t.Errorf("content mismatch: got %q", result)
	}
}

func TestRead_OffsetLimit(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nb\nc\nd\ne\n"), 0o644)
	result, _, _ := ReadTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"path":"f.txt","offset":2,"limit":2}`))
	if result != "b\nc" {
		t.Errorf("offset/limit window wrong: %q", result)
	}
}

func TestRead_RejectsMissingFile(t *testing.T) {
	result, isErr, _ := ReadTool{}.Execute(context.Background(), newSession(t.TempDir()),
		json.RawMessage(`{"path":"nope.txt"}`))
	if !isErr {
		t.Errorf("missing file should set is_error=true; got result=%q", result)
	}
}

func TestRead_RejectsTraversal(t *testing.T) {
	result, isErr, _ := ReadTool{}.Execute(context.Background(), newSession(t.TempDir()),
		json.RawMessage(`{"path":"../escape.txt"}`))
	if !isErr || !strings.Contains(result, "outside project root") {
		t.Errorf("traversal not rejected: isErr=%v result=%q", isErr, result)
	}
}

func TestRead_TruncatesLarge(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 300*1024)
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o644)
	result, _, _ := ReadTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"path":"big.txt"}`))
	if !strings.Contains(result, "truncated") {
		t.Error("large file should be truncated with a notice")
	}
	if len(result) > 257*1024 {
		t.Errorf("truncation ineffective: result is %d bytes", len(result))
	}
}

// ---- GlobTool ------------------------------------------------------

func TestGlob_SimplePattern(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "b.go"), []byte("package b"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644)
	result, _, _ := GlobTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"pattern":"src/*.go"}`))
	if !strings.Contains(result, "src/a.go") || !strings.Contains(result, "src/b.go") {
		t.Errorf("expected src/*.go matches, got %q", result)
	}
	if strings.Contains(result, "README") {
		t.Error("non-matching file leaked into result")
	}
}

func TestGlob_DoubleStar(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "b", "c"), 0o755)
	os.WriteFile(filepath.Join(dir, "a", "b", "c", "deep.go"), []byte("deep"), 0o644)
	os.WriteFile(filepath.Join(dir, "top.go"), []byte("top"), 0o644)
	result, _, _ := GlobTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"pattern":"**/*.go"}`))
	if !strings.Contains(result, "deep.go") || !strings.Contains(result, "top.go") {
		t.Errorf("recursive ** should match nested files; got %q", result)
	}
}

func TestGlob_IgnoresVendoredDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules", "foo"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "foo", "index.js"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "real.js"), []byte("y"), 0o644)
	result, _, _ := GlobTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"pattern":"**/*.js"}`))
	if strings.Contains(result, "node_modules") {
		t.Errorf("node_modules should be ignored; got %q", result)
	}
	if !strings.Contains(result, "real.js") {
		t.Errorf("real file missing; got %q", result)
	}
}

// ---- GrepTool ------------------------------------------------------

func TestGrep_BasicMatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nfunc Foo() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\nfunc Bar() {}\n"), 0o644)
	result, _, _ := GrepTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"pattern":"func Foo"}`))
	if !strings.Contains(result, "a.go:2:func Foo() {}") {
		t.Errorf("expected a.go:2 match, got %q", result)
	}
	if strings.Contains(result, "b.go") {
		t.Error("b.go should not appear in match")
	}
}

func TestGrep_RegexCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("FOO\nBar\nfoo\n"), 0o644)
	result, _, _ := GrepTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"pattern":"(?i)^foo$"}`))
	if strings.Count(result, "a.go") != 2 {
		t.Errorf("case-insensitive should match both FOO and foo; got %q", result)
	}
}

func TestGrep_GlobFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("match me"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("match me"), 0o644)
	result, _, _ := GrepTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"pattern":"match","glob":"*.go"}`))
	if !strings.Contains(result, "a.go") {
		t.Errorf("glob should include a.go; got %q", result)
	}
	if strings.Contains(result, "b.txt") {
		t.Error("glob should exclude b.txt")
	}
}

func TestGrep_NoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("nothing here"), 0o644)
	result, isErr, _ := GrepTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"pattern":"xyzzy"}`))
	if isErr {
		t.Error("no-match is not an error")
	}
	if result != "(no matches)" {
		t.Errorf("empty sentinel expected; got %q", result)
	}
}

func TestGrep_SkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	// File containing a NUL byte — heuristic: skip.
	os.WriteFile(filepath.Join(dir, "bin"), []byte("text\x00match"), 0o644)
	os.WriteFile(filepath.Join(dir, "ok.go"), []byte("match"), 0o644)
	result, _, _ := GrepTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"pattern":"match"}`))
	if strings.Contains(result, "bin:") {
		t.Errorf("binary file leaked into results: %q", result)
	}
	if !strings.Contains(result, "ok.go:1:match") {
		t.Errorf("expected ok.go match; got %q", result)
	}
}

// ---- EditTool ------------------------------------------------------

func TestEdit_SingleReplacement(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("alpha\nbeta\ngamma\n"), 0o644)
	result, isErr, _ := EditTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"path":"f.txt","old_text":"beta","new_text":"BETA"}`))
	if isErr {
		t.Fatalf("unexpected error: %s", result)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "alpha\nBETA\ngamma\n" {
		t.Errorf("file content: got %q", got)
	}
	if !strings.Contains(result, "Edited") {
		t.Errorf("result summary missing 'Edited': %q", result)
	}
}

func TestEdit_RejectsAmbiguous(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("same\nsame\nsame\n"), 0o644)
	result, isErr, _ := EditTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"path":"f.txt","old_text":"same","new_text":"SAME"}`))
	if !isErr {
		t.Error("ambiguous edit should fail without replace_all")
	}
	if !strings.Contains(result, "appears 3 times") {
		t.Errorf("error message not informative: %q", result)
	}
}

func TestEdit_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("foo bar foo baz foo"), 0o644)
	result, isErr, _ := EditTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"path":"f.txt","old_text":"foo","new_text":"FOO","replace_all":true}`))
	if isErr {
		t.Fatalf("unexpected error: %s", result)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "FOO bar FOO baz FOO" {
		t.Errorf("content: %q", got)
	}
}

func TestEdit_RejectsMissingOldText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("hello"), 0o644)
	result, isErr, _ := EditTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"path":"f.txt","old_text":"nonexistent","new_text":"x"}`))
	if !isErr {
		t.Error("missing old_text should fail")
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("error message unclear: %q", result)
	}
}

func TestEdit_CreateNewFile(t *testing.T) {
	dir := t.TempDir()
	result, isErr, _ := EditTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"path":"sub/new.txt","old_text":"","new_text":"fresh contents"}`))
	if isErr {
		t.Fatalf("create failed: %s", result)
	}
	got, err := os.ReadFile(filepath.Join(dir, "sub", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh contents" {
		t.Errorf("content: %q", got)
	}
}

func TestEdit_RejectsOverwriteOnCreate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "exists.txt")
	os.WriteFile(p, []byte("original"), 0o644)
	result, isErr, _ := EditTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"path":"exists.txt","old_text":"","new_text":"overwrite"}`))
	if !isErr {
		t.Error("create should refuse to overwrite an existing file")
	}
	got, _ := os.ReadFile(p)
	if string(got) != "original" {
		t.Errorf("original should be preserved, got %q", got)
	}
	_ = result
}

func TestEdit_RejectsNoOpEdit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("same content"), 0o644)
	result, isErr, _ := EditTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"path":"f.txt","old_text":"same","new_text":"same"}`))
	if !isErr || !strings.Contains(result, "no-op") {
		t.Errorf("no-op should fail clearly; got isErr=%v result=%q", isErr, result)
	}
}

func TestEdit_AtomicWriteSurvivesCrash(t *testing.T) {
	// Sanity: after successful edit, no .edit-* temp file remains.
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("hello"), 0o644)
	_, _, _ = EditTool{}.Execute(context.Background(), newSession(dir),
		json.RawMessage(`{"path":"f.txt","old_text":"hello","new_text":"world"}`))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".edit-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
