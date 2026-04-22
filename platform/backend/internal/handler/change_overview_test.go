package handler

import (
	"strings"
	"testing"

	"github.com/a3c/platform/internal/model"
)

// TestCheckOverviewStale covers the boundary behaviour of the OVERVIEW.md
// "nag" that lives on change_submit. Must stay deterministic: no DB, no
// LLM, no randomness. Every case here is reachable from a real
// change_submit call — no synthetic shapes.
func TestCheckOverviewStale(t *testing.T) {
	tests := []struct {
		name    string
		writes  []model.ChangeFileEntry
		deletes []string
		// wantReminder: true means we expect a non-empty reminder string;
		// the exact wording is checked separately.
		wantReminder bool
	}{
		{
			name: "single Go file modified — below threshold, no nag",
			writes: []model.ChangeFileEntry{
				{Path: "internal/service/foo.go"},
			},
			wantReminder: false,
		},
		{
			name: "three Go files modified, no OVERVIEW update — nag",
			writes: []model.ChangeFileEntry{
				{Path: "internal/service/a.go"},
				{Path: "internal/service/b.go"},
				{Path: "internal/service/c.go"},
			},
			wantReminder: true,
		},
		{
			name: "three Go files + OVERVIEW.md updated — no nag",
			writes: []model.ChangeFileEntry{
				{Path: "internal/service/a.go"},
				{Path: "internal/service/b.go"},
				{Path: "internal/service/c.go"},
				{Path: "OVERVIEW.md"},
			},
			wantReminder: false,
		},
		{
			name: "three _test.go files — tests don't count",
			writes: []model.ChangeFileEntry{
				{Path: "internal/service/a_test.go"},
				{Path: "internal/service/b_test.go"},
				{Path: "internal/service/c_test.go"},
			},
			wantReminder: false,
		},
		{
			name: "mixed: 3 source + 2 tests = 3 structural, nag",
			writes: []model.ChangeFileEntry{
				{Path: "src/a.go"},
				{Path: "src/b.go"},
				{Path: "src/c.go"},
				{Path: "src/a_test.go"},
				{Path: "src/b_test.go"},
			},
			wantReminder: true,
		},
		{
			name: "TypeScript .spec.ts filtered out",
			writes: []model.ChangeFileEntry{
				{Path: "web/a.spec.ts"},
				{Path: "web/b.spec.ts"},
				{Path: "web/c.spec.ts"},
			},
			wantReminder: false,
		},
		{
			name: "three .tsx files — counts",
			writes: []model.ChangeFileEntry{
				{Path: "web/A.tsx"},
				{Path: "web/B.tsx"},
				{Path: "web/C.tsx"},
			},
			wantReminder: true,
		},
		{
			name: "docs-only changes — never nag",
			writes: []model.ChangeFileEntry{
				{Path: "docs/a.md"},
				{Path: "docs/b.md"},
				{Path: "docs/c.md"},
				{Path: "README.md"},
			},
			wantReminder: false,
		},
		{
			name: "deletes count as structural",
			writes: []model.ChangeFileEntry{
				{Path: "src/keep.go"},
			},
			deletes:      []string{"src/removed1.go", "src/removed2.go"},
			wantReminder: true,
		},
		{
			name:    "deleting OVERVIEW.md — suppresses nag (explicit rearrangement)",
			writes: []model.ChangeFileEntry{
				{Path: "src/a.go"},
				{Path: "src/b.go"},
				{Path: "src/c.go"},
			},
			deletes:      []string{"OVERVIEW.md"},
			wantReminder: false,
		},
		{
			name: "generated .pb.go files — filtered out",
			writes: []model.ChangeFileEntry{
				{Path: "proto/a.pb.go"},
				{Path: "proto/b.pb.go"},
				{Path: "proto/c.pb.go"},
			},
			wantReminder: false,
		},
		{
			name: "python test_ prefix — filtered out",
			writes: []model.ChangeFileEntry{
				{Path: "pkg/test_a.py"},
				{Path: "pkg/test_b.py"},
				{Path: "pkg/test_c.py"},
			},
			wantReminder: false,
		},
		{
			name: "testdata dir — filtered out",
			writes: []model.ChangeFileEntry{
				{Path: "pkg/testdata/fixture1.go"},
				{Path: "pkg/testdata/fixture2.go"},
				{Path: "pkg/testdata/fixture3.go"},
			},
			wantReminder: false,
		},
		{
			name:         "empty change — no nag, no panic",
			writes:       nil,
			deletes:      nil,
			wantReminder: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := checkOverviewStale(tc.writes, tc.deletes)
			hasReminder := got != ""
			if hasReminder != tc.wantReminder {
				t.Fatalf("checkOverviewStale wantReminder=%v got=%q", tc.wantReminder, got)
			}
			if hasReminder && !strings.Contains(got, "OVERVIEW.md") {
				t.Fatalf("reminder should mention OVERVIEW.md; got %q", got)
			}
		})
	}
}

// TestIsOverviewPath covers the path-matching edge cases the Windows/Unix
// path mixing can produce. Case sensitivity matters because MCP clients
// on Windows may send "overview.md"; we treat it as the same file.
func TestIsOverviewPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"OVERVIEW.md", true},
		{"./OVERVIEW.md", true},
		{"overview.md", true}, // case-insensitive
		{"Overview.md", true},
		{"docs/OVERVIEW.md", false}, // nested — does not satisfy the root protocol
		{"src/OVERVIEW.md", false},
		{"OVERVIEW", false},
		{"OVERVIEW.txt", false},
		{"", false},
	}
	for _, c := range cases {
		got := isOverviewPath(c.path)
		if got != c.want {
			t.Errorf("isOverviewPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestIsStructuralSourceFile documents the heuristic: what we consider
// production source vs tests/fixtures/generated. Errs toward false
// (fewer nags) which is the safe direction — a missed nag costs less
// than a spurious one that trains the agent to ignore the signal.
func TestIsStructuralSourceFile(t *testing.T) {
	structural := []string{
		"main.go",
		"src/app.ts",
		"src/App.tsx",
		"pkg/foo.py",
		"lib/util.rs",
		"api/handler.java",
	}
	nonStructural := []string{
		// tests
		"foo_test.go",
		"src/app.test.ts",
		"src/app.spec.tsx",
		"pkg/test_foo.py",
		"api/__tests__/handler.ts",
		// fixtures / generated
		"pkg/testdata/x.go",
		"proto/api.pb.go",
		"gen/types.gen.go",
		"gen/types_generated.go",
		// non-source
		"README.md",
		"config.yaml",
		"image.png",
		"",
	}
	for _, p := range structural {
		if !isStructuralSourceFile(p) {
			t.Errorf("expected %q to be structural", p)
		}
	}
	for _, p := range nonStructural {
		if isStructuralSourceFile(p) {
			t.Errorf("expected %q to NOT be structural", p)
		}
	}
}
