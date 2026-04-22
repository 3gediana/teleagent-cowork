package refinery

// Topic context extraction
// ========================
//
// Refinery artifacts carry embeddings. But embeddings are only as useful
// as the text they're computed from — and our default artifact summaries
// were too formulaic ("Tool sequence X on go files observed in Y episodes,
// Z success rate"). bge looks at that and sees all patterns as roughly
// the same topic ("go-tooling activity"), so cosine similarity against
// a query like "修复登录 401" lands every artifact in a narrow 0.74-0.78
// band.
//
// The fix: before we build each artifact's summary, pull topic keywords
// from its source episodes — file-path segments + associated task names.
// Those words carry the actual project vocabulary ("auth", "middleware",
// "i18n", "user-schema", 以及中文任务标题), so the encoded summary lands
// in a much more discriminative region of the semantic space.
//
// Two complementary signals:
//
//   1. Path keywords   — from Episode.FilesTouched, split on / and .
//                        and filtered against a stop-list of generic
//                        project chrome (internal, src, pkg, ...).
//                        Stable across runs; deterministic.
//
//   2. Task context    — from Task.Name via Episode.TaskID join. Most
//                        informative signal (human-written natural
//                        language). Optional: only surfaces when the
//                        episode is linked to a task.
//
// Both are summarised into a short English phrase ("— seen while
// working on middleware/auth, handler/auth; tasks like 修复 401") so
// the embedder has real text to encode instead of sequence metadata.

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/a3c/platform/internal/model"
)

// Path segments that carry no discriminative signal. Extending this list
// costs nothing; the cost of being too eager (removing a real keyword) is
// worse than leaving a mildly-generic term in.
var pathStopWords = map[string]bool{
	"": true, ".": true, "..": true,
	// language extensions
	"go": true, "ts": true, "tsx": true, "js": true, "jsx": true, "py": true,
	"md": true, "json": true, "yaml": true, "yml": true, "sql": true, "sh": true,
	// project chrome — appears in virtually every path in an A3C-style tree
	"internal": true, "src": true, "pkg": true, "cmd": true, "lib": true,
	"app": true, "test": true, "tests": true, "spec": true, "main": true,
	// very common top-level dirs
	"node_modules": true, "build": true, "dist": true, "out": true,
	"public": true, "static": true, "assets": true, "vendor": true,
	"models": true, "utils": true, "util": true, "helpers": true,
	"types": true, "interfaces": true,
}

// topicSources bundles the inputs we need per artifact scope. Passed
// around instead of re-querying the DB inside each formatter.
type topicSources struct {
	episodes []model.Episode // the source episodes for this artifact
}

// resolveEpisodes maps a slice of episode IDs to their full model
// records using a pre-built index. Silently drops IDs that aren't
// present (can happen in legacy/trim scenarios where an ID survives on
// the artifact but the episode got pruned).
func resolveEpisodes(ids []string, idx map[string]*model.Episode) []model.Episode {
	out := make([]model.Episode, 0, len(ids))
	for _, id := range ids {
		if ep, ok := idx[id]; ok && ep != nil {
			out = append(out, *ep)
		}
	}
	return out
}

// extractTopicContext produces a short human-readable phrase that can be
// appended to artifact summaries. Empty string when no signals are
// available (brand-new project, or episodes with no file / task info);
// callers safely skip it in that case.
func extractTopicContext(src topicSources) string {
	keywords := collectPathKeywords(src.episodes, 4)
	taskNames := collectTaskNames(src.episodes, 2)

	var parts []string
	if len(keywords) > 0 {
		parts = append(parts, "touches "+strings.Join(keywords, ", "))
	}
	if len(taskNames) > 0 {
		parts = append(parts, "used for tasks like "+strings.Join(taskNames, " / "))
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, "; ")
}

// collectPathKeywords walks every FilesTouched entry across the source
// episodes and returns the top-K distinct path segments by frequency.
// Ties broken alphabetically so output is deterministic across runs.
func collectPathKeywords(eps []model.Episode, topK int) []string {
	counts := map[string]int{}
	for _, ep := range eps {
		if ep.FilesTouched == "" {
			continue
		}
		var files []string
		// FilesTouched is either a bare JSON array of strings OR an
		// object with a `files` array (matches the two shapes that
		// extractFiles() emits upstream).
		if err := json.Unmarshal([]byte(ep.FilesTouched), &files); err != nil {
			var obj struct {
				Files []string `json:"files"`
			}
			if err := json.Unmarshal([]byte(ep.FilesTouched), &obj); err == nil {
				files = obj.Files
			}
		}
		for _, path := range files {
			for _, seg := range splitPathSegments(path) {
				if pathStopWords[seg] {
					continue
				}
				if len(seg) < 3 {
					continue
				}
				counts[seg]++
			}
		}
	}
	return topKByFreq(counts, topK)
}

// splitPathSegments breaks a file path into its constituent words.
// "internal/middleware/auth.go" → ["internal","middleware","auth","go"]
// "src/components/LangSwitch.tsx" → ["src","components","LangSwitch","tsx"]
// The extension pieces get filtered by the stop-list above.
func splitPathSegments(path string) []string {
	// Normalise separators so Windows + POSIX both work.
	path = strings.ReplaceAll(path, "\\", "/")
	parts := strings.Split(path, "/")
	segments := make([]string, 0, len(parts)*2)
	for _, p := range parts {
		// Strip extensions; keep the base name as a segment.
		if i := strings.LastIndex(p, "."); i > 0 {
			segments = append(segments, strings.ToLower(p[:i]))
			segments = append(segments, strings.ToLower(p[i+1:]))
		} else {
			segments = append(segments, strings.ToLower(p))
		}
	}
	return segments
}

// collectTaskNames joins episodes → tasks and returns the top-K most
// common task names. Helpful because task names are human-written
// natural language ("修复用户登录 401"), so they carry strong topic
// signal that file paths alone can't.
func collectTaskNames(eps []model.Episode, topK int) []string {
	taskIDs := make([]string, 0, len(eps))
	seen := map[string]bool{}
	for _, ep := range eps {
		if ep.TaskID == "" || seen[ep.TaskID] {
			continue
		}
		seen[ep.TaskID] = true
		taskIDs = append(taskIDs, ep.TaskID)
	}
	if len(taskIDs) == 0 {
		return nil
	}
	var tasks []model.Task
	model.DB.Where("id IN ?", taskIDs).Find(&tasks)

	// Order by name length asc — shorter task names are more likely to
	// be canonical labels, longer ones may be noisy full sentences.
	// We cap each name at 50 rune chars regardless.
	sort.Slice(tasks, func(i, j int) bool {
		return len([]rune(tasks[i].Name)) < len([]rune(tasks[j].Name))
	})
	out := make([]string, 0, topK)
	for _, t := range tasks {
		if len(out) >= topK {
			break
		}
		if t.Name != "" {
			out = append(out, truncateName(t.Name, 50))
		}
	}
	return out
}

func truncateName(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// topKByFreq selects the top-k most common keys from a counts map,
// breaking ties alphabetically for determinism.
func topKByFreq(counts map[string]int, k int) []string {
	type kv struct {
		key string
		n   int
	}
	items := make([]kv, 0, len(counts))
	for key, n := range counts {
		items = append(items, kv{key, n})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].n != items[j].n {
			return items[i].n > items[j].n
		}
		return items[i].key < items[j].key
	})
	if len(items) > k {
		items = items[:k]
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.key
	}
	return out
}
