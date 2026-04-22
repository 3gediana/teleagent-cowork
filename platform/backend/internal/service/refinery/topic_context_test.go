package refinery

// Unit tests for topic_context.go — the extractor that turns source
// Episodes into human-readable topic phrases for artifact summaries.
// Pure functions except for collectTaskNames (queries the DB), which we
// cover in a separate sqlite-backed test.

import (
	"reflect"
	"sort"
	"testing"

	"github.com/a3c/platform/internal/model"
)

func TestSplitPathSegments_NormalCases(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"internal/middleware/auth.go", []string{"internal", "middleware", "auth", "go"}},
		{"src/components/LangSwitch.tsx", []string{"src", "components", "langswitch", "tsx"}},
		{"", []string{""}},                                              // empty path → single empty segment (filtered by stop list)
		{"README.md", []string{"readme", "md"}},                        // extension split even on no-slash paths
		{`internal\handler\auth.go`, []string{"internal", "handler", "auth", "go"}}, // Windows separator
	}
	for _, c := range cases {
		got := splitPathSegments(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitPathSegments(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCollectPathKeywords_FiltersStopWords(t *testing.T) {
	eps := []model.Episode{
		{FilesTouched: `["internal/middleware/auth.go"]`},
		{FilesTouched: `["internal/handler/auth.go"]`},
		{FilesTouched: `["internal/middleware/auth.go"]`},
	}
	got := collectPathKeywords(eps, 5)
	// "internal" and "go" are stop-listed. "auth" appears 3x, "middleware" 2x,
	// "handler" 1x → rank: auth, middleware, handler.
	want := []string{"auth", "middleware", "handler"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCollectPathKeywords_HonoursTopK(t *testing.T) {
	eps := []model.Episode{
		{FilesTouched: `["src/a/alpha.ts","src/b/beta.ts","src/c/gamma.ts"]`},
	}
	got := collectPathKeywords(eps, 2)
	if len(got) != 2 {
		t.Errorf("topK=2 but got %d items: %v", len(got), got)
	}
}

func TestCollectPathKeywords_HandlesObjectJSON(t *testing.T) {
	// Some producers emit `{"files":[...]}` instead of a bare array.
	// The extractor must accept both shapes.
	eps := []model.Episode{
		{FilesTouched: `{"files":["internal/auth/jwt.go"]}`},
	}
	got := collectPathKeywords(eps, 5)
	sort.Strings(got) // deterministic compare
	want := []string{"auth", "jwt"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (object-wrapped files shape should work)", got, want)
	}
}

func TestCollectPathKeywords_SkipsShortOrEmpty(t *testing.T) {
	eps := []model.Episode{
		{FilesTouched: `["a/b/c.go"]`}, // single-char segments filtered (len < 3)
	}
	got := collectPathKeywords(eps, 5)
	if len(got) != 0 {
		t.Errorf("expected empty result for all-short segments, got %v", got)
	}
}

func TestExtractTopicContext_EmptyWhenNothingUseful(t *testing.T) {
	// No files, no task — nothing to say, return empty string so the
	// caller's Sprintf doesn't end in a dangling " — ".
	got := extractTopicContext(topicSources{episodes: []model.Episode{
		{ID: "e1"},
		{ID: "e2"},
	}})
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractTopicContext_IncludesKeywordsPrefix(t *testing.T) {
	got := extractTopicContext(topicSources{episodes: []model.Episode{
		{FilesTouched: `["internal/middleware/auth.go"]`},
		{FilesTouched: `["internal/middleware/auth.go"]`},
	}})
	// Must start with " — " and mention "auth".
	if len(got) == 0 || got[0] != ' ' {
		t.Errorf("expected leading separator, got %q", got)
	}
	if !containsSubstr(got, "auth") {
		t.Errorf("expected keyword 'auth' in phrase, got %q", got)
	}
}

func TestResolveEpisodes_DropsMissingIDs(t *testing.T) {
	a := model.Episode{ID: "a"}
	b := model.Episode{ID: "b"}
	idx := map[string]*model.Episode{"a": &a, "b": &b}
	got := resolveEpisodes([]string{"a", "missing", "b"}, idx)
	if len(got) != 2 {
		t.Fatalf("expected 2 resolved, got %d", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("order should be preserved, got %+v", got)
	}
}

// Tiny helper to avoid a full strings import for a one-liner.
func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
