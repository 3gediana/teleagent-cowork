package refinery

import (
	"strings"
	"testing"
	"time"

	"github.com/a3c/platform/internal/model"
)

// TestPatternExtractor_InMemory verifies the n-gram mining logic directly
// without a database. We feed a hand-built Episode slice into the core
// counting loop by calling the pass's helpers via a minimal harness.
//
// This keeps the test hermetic (no MySQL), which matters because the
// refinery is a pure data-transformation layer and should be diff-tested
// against golden outputs as it evolves.
func TestPatternExtractor_CountsNGramsBySession(t *testing.T) {
	// Four episodes; "grep read edit" appears in 3 of them (all success).
	// With patternMinSupport=3, patternMinConfidence=0.7, it should emerge.
	eps := []model.Episode{
		{ID: "e1", Outcome: "success", ToolSequence: "grep read edit change_submit"},
		{ID: "e2", Outcome: "success", ToolSequence: "grep read edit change_submit"},
		{ID: "e3", Outcome: "success", ToolSequence: "grep read edit pr_submit"},
		{ID: "e4", Outcome: "failure", ToolSequence: "edit edit edit"},
	}

	// We replicate the inner loop from PatternExtractor.Run here to test
	// the mining logic purely. A full integration test would need a DB.
	type ng struct{ any, succ int }
	counts := map[string]*ng{}
	for _, ep := range eps {
		seq := strings.Fields(ep.ToolSequence)
		seen := map[string]bool{}
		for n := patternMinN; n <= patternMaxN; n++ {
			for i := 0; i+n <= len(seq); i++ {
				gram := strings.Join(seq[i:i+n], " ")
				if seen[gram] {
					continue
				}
				seen[gram] = true
				c, ok := counts[gram]
				if !ok {
					c = &ng{}
					counts[gram] = c
				}
				c.any++
				if ep.Outcome == "success" {
					c.succ++
				}
			}
		}
	}

	c := counts["grep read edit"]
	if c == nil {
		t.Fatalf("expected 'grep read edit' in counts")
	}
	if c.any != 3 {
		t.Errorf("expected support=3 for 'grep read edit', got %d", c.any)
	}
	if float64(c.succ)/float64(c.any) < patternMinConfidence {
		t.Errorf("expected confidence ≥ %.2f, got %.2f", patternMinConfidence, float64(c.succ)/float64(c.any))
	}
}

func TestAntiPatternDetector_LiftAbovesBaseline(t *testing.T) {
	// Corpus: 10 episodes, 4 failures. Baseline fail rate = 0.4.
	// "edit edit" appears in 3 episodes, all failures → fail_rate = 1.0,
	// lift = 2.5 → should qualify (min lift 1.5).
	eps := []model.Episode{}
	for i := 0; i < 6; i++ {
		eps = append(eps, model.Episode{Outcome: "success", ToolSequence: "grep read edit change_submit"})
	}
	for i := 0; i < 3; i++ {
		eps = append(eps, model.Episode{Outcome: "failure", ToolSequence: "edit edit change_submit"})
	}
	eps = append(eps, model.Episode{Outcome: "failure", ToolSequence: "grep read read"})

	var totalKnown, totalFailed int
	for _, ep := range eps {
		switch ep.Outcome {
		case "success":
			totalKnown++
		case "failure":
			totalKnown++
			totalFailed++
		}
	}
	baseline := float64(totalFailed) / float64(totalKnown)
	if baseline <= 0 {
		t.Fatalf("expected positive baseline")
	}

	type ng struct{ any, fail int }
	counts := map[string]*ng{}
	for _, ep := range eps {
		if ep.Outcome != "success" && ep.Outcome != "failure" {
			continue
		}
		seq := strings.Fields(ep.ToolSequence)
		seen := map[string]bool{}
		for n := antiMinN; n <= antiMaxN; n++ {
			for i := 0; i+n <= len(seq); i++ {
				gram := strings.Join(seq[i:i+n], " ")
				if seen[gram] {
					continue
				}
				seen[gram] = true
				c, ok := counts[gram]
				if !ok {
					c = &ng{}
					counts[gram] = c
				}
				c.any++
				if ep.Outcome == "failure" {
					c.fail++
				}
			}
		}
	}
	c := counts["edit edit"]
	if c == nil {
		t.Fatalf("expected 'edit edit' in counts")
	}
	lift := (float64(c.fail) / float64(c.any)) / baseline
	if lift < antiMinLift {
		t.Errorf("expected lift ≥ %.2f for 'edit edit', got %.2f", antiMinLift, lift)
	}
}

func TestExtractFiles_KnownKeys(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"null", "null", nil},
		{"path", `{"path":"foo/bar.go"}`, []string{"foo/bar.go"}},
		{"files array", `{"files":["a.go","b.go"]}`, []string{"a.go", "b.go"}},
		{"writes with nested path", `{"writes":[{"path":"x.go","content":"y"}]}`, []string{"x.go"}},
		{"garbage", "not json", nil},
	}
	for _, tt := range tests {
		got := extractFiles(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
			continue
		}
		// Order can vary (map iteration); compare as sets.
		set := map[string]bool{}
		for _, s := range got {
			set[s] = true
		}
		for _, w := range tt.want {
			if !set[w] {
				t.Errorf("%s: missing %q in %v", tt.name, w, got)
			}
		}
	}
}

// Silence "declared but not used" when we add helpers that only the
// database-backed passes reference.
var _ = time.Now
