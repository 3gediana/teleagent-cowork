package refinery

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/a3c/platform/internal/model"
)

// PatternExtractor mines recurring tool-call n-grams from successful
// Episodes. An n-gram with high `support` (seen in many episodes) and
// high `confidence` (most episodes containing it succeeded) becomes a
// KnowledgeArtifact of kind=pattern.
//
// Purely deterministic; no LLM calls. Output is reproducible given the
// same Episode set.
type PatternExtractor struct{}

func (PatternExtractor) Name() string      { return "pattern_extractor/v1" }
func (PatternExtractor) Produces() []string { return []string{"pattern"} }
func (PatternExtractor) Requires() []string { return []string{"episode"} }

// Tunables. Chosen to be conservative: we'd rather surface a few strong
// patterns than drown the operator in weak ones. These can migrate into
// project config later once we have baseline numbers.
const (
	patternMinN          = 2
	patternMaxN          = 4
	patternMinSupport    = 3    // must appear in ≥ N episodes (distinct sessions)
	patternMinConfidence = 0.70 // ≥70% of episodes containing this n-gram must have succeeded
)

func (PatternExtractor) Run(ctx *Context) (Stats, error) {
	episodes, err := loadEpisodes(ctx)
	if err != nil {
		return nil, err
	}
	if len(episodes) == 0 {
		return Stats{"episodes_seen": 0, "patterns_upserted": 0}, nil
	}

	// Count n-gram occurrences across episodes, keyed by (gram, file_category).
	// Keying on file category lets us distinguish "grep→read→edit on code"
	// from "grep→read→edit on docs", which typically have different success
	// profiles. Episodes with no file signal fall into the "any" bucket.
	type ngramStats struct {
		supportSuccess int
		supportAny     int
		gram           string
		fileCategory   string
		sourceIDs      []string
	}
	stats := map[string]*ngramStats{} // key: "gram|fileCategory"

	for _, ep := range episodes {
		seq := strings.Fields(ep.ToolSequence)
		if len(seq) < patternMinN {
			continue
		}
		fileCategory := inferFileCategory(ep.FilesTouched)
		seen := map[string]bool{}
		for n := patternMinN; n <= patternMaxN; n++ {
			for i := 0; i+n <= len(seq); i++ {
				gram := strings.Join(seq[i:i+n], " ")
				key := gram + "|" + fileCategory
				if seen[key] {
					continue
				}
				seen[key] = true
				s, ok := stats[key]
				if !ok {
					s = &ngramStats{gram: gram, fileCategory: fileCategory}
					stats[key] = s
				}
				s.supportAny++
				if ep.Outcome == "success" {
					s.supportSuccess++
				}
				if len(s.sourceIDs) < 20 {
					s.sourceIDs = append(s.sourceIDs, ep.ID)
				}
			}
		}
	}

	// Filter + rank.
	type keeper struct {
		gram         string
		fileCategory string
		support      int
		confidence   float64
		sources      []string
	}
	var keepers []keeper
	for _, s := range stats {
		if s.supportAny < patternMinSupport {
			continue
		}
		conf := float64(s.supportSuccess) / float64(s.supportAny)
		if conf < patternMinConfidence {
			continue
		}
		keepers = append(keepers, keeper{s.gram, s.fileCategory, s.supportAny, conf, s.sourceIDs})
	}
	// Highest support first, break ties by confidence. Makes the dashboard
	// story cleaner: the most re-usable pattern shows up at the top.
	sort.Slice(keepers, func(i, j int) bool {
		if keepers[i].support != keepers[j].support {
			return keepers[i].support > keepers[j].support
		}
		return keepers[i].confidence > keepers[j].confidence
	})

	// Build a lookup so each keeper can resolve its source episodes in
	// O(1) for topic extraction. Same slice `episodes` already in
	// memory from the load at top of Run — no extra DB hits.
	epIndex := make(map[string]*model.Episode, len(episodes))
	for i := range episodes {
		epIndex[episodes[i].ID] = &episodes[i]
	}

	upserted := 0
	for _, k := range keepers {
		tools := strings.Fields(k.gram)
		payload, _ := json.Marshal(map[string]any{
			"tool_sequence": tools,
			"n":             len(tools),
			"support":       k.support,
			"confidence":    k.confidence,
			"file_category": k.fileCategory,
		})
		// Derive topic context (keywords + task names) so the artifact
		// summary carries the actual project vocabulary. Without this,
		// all pattern summaries read the same to bge and land in a
		// narrow semantic band regardless of query.
		topic := extractTopicContext(topicSources{
			episodes: resolveEpisodes(k.sources, epIndex),
		})
		summary := fmt.Sprintf(
			"Tool sequence %q on %s files observed in %d successful episodes (%.0f%% success rate)%s",
			k.gram, k.fileCategory, k.support, k.confidence*100, topic,
		)
		ka := &model.KnowledgeArtifact{
			ProjectID:  ctx.ProjectID,
			Kind:       "pattern",
			Name:       patternNameWithCategory(tools, k.fileCategory),
			Summary:    summary,
			Payload:    string(payload),
			ProducedBy: PatternExtractor{}.Name(),
			SourceEvents: sourceEventsJSON(k.sources),
			Confidence: k.confidence,
		}
		if err := upsertArtifact(ka); err != nil {
			continue
		}
		upserted++
	}

	return Stats{
		"episodes_seen":     len(episodes),
		"ngrams_considered": len(stats),
		"ngrams_kept":       len(keepers),
		"patterns_upserted": upserted,
		"cutoff_support":    patternMinSupport,
		"cutoff_confidence": patternMinConfidence,
	}, nil
}

// patternName gives each n-gram a human-readable stable ID usable as the
// upsert key. Format: "pat: grep→read→edit".
func patternName(tools []string) string {
	return "pat: " + strings.Join(tools, "→")
}

// patternNameWithCategory is the v2 name that also includes the file
// category so the same tool-sequence on different file types becomes
// distinct artifacts. Format: "pat[go]: grep→read→edit".
func patternNameWithCategory(tools []string, category string) string {
	if category == "" || category == "any" {
		return patternName(tools)
	}
	return "pat[" + category + "]: " + strings.Join(tools, "→")
}

// inferFileCategory reduces a JSON array of file paths to a single
// category tag used for pattern bucketing. Returns "any" if no files.
// Strategy: pick the most common category among the touched files.
func inferFileCategory(filesJSON string) string {
	if filesJSON == "" || filesJSON == "null" || filesJSON == "[]" {
		return "any"
	}
	var files []string
	if err := json.Unmarshal([]byte(filesJSON), &files); err != nil || len(files) == 0 {
		return "any"
	}
	counts := map[string]int{}
	for _, f := range files {
		counts[categorizeFile(f)]++
	}
	best := "any"
	bestN := 0
	for cat, n := range counts {
		if n > bestN {
			best = cat
			bestN = n
		}
	}
	return best
}

// categorizeFile maps a single file path to a coarse category based on
// its extension and common directory hints.
func categorizeFile(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".js", ".jsx":
		return "web"
	case ".py":
		return "py"
	case ".md", ".rst", ".txt":
		return "docs"
	case ".json", ".yaml", ".yml", ".toml":
		return "config"
	case ".sql":
		return "sql"
	case ".sh", ".bat", ".ps1":
		return "script"
	case ".html", ".css", ".scss":
		return "web"
	}
	lower := strings.ToLower(path)
	if strings.Contains(lower, "test") || strings.HasSuffix(lower, "_test.go") {
		return "test"
	}
	return "other"
}

// loadEpisodes is shared by passes that read Episodes.
func loadEpisodes(ctx *Context) ([]model.Episode, error) {
	q := model.DB.Model(&model.Episode{})
	if ctx.ProjectID != "" {
		q = q.Where("project_id = ?", ctx.ProjectID)
	}
	if ctx.LookbackHours > 0 {
		q = q.Where("created_at >= ?", ctx.Now.Add(-time.Duration(ctx.LookbackHours)*time.Hour))
	}
	var eps []model.Episode
	if err := q.Find(&eps).Error; err != nil {
		return nil, err
	}
	return eps, nil
}
