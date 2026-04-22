package refinery

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/a3c/platform/internal/model"
)

// AntiPatternDetector is the dual of PatternExtractor: it mines n-grams
// that are over-represented in FAILED episodes compared to the baseline
// failure rate.
//
// We use lift = P(failure | contains n-gram) / P(failure overall). An
// n-gram with lift > 1.5 and sufficient support is flagged as an
// anti-pattern. This is a classic association-rule mining signal adapted
// to episode data.
type AntiPatternDetector struct{}

func (AntiPatternDetector) Name() string      { return "antipattern_detector/v1" }
func (AntiPatternDetector) Produces() []string { return []string{"anti_pattern"} }
func (AntiPatternDetector) Requires() []string { return []string{"episode"} }

const (
	antiMinN       = 2
	antiMaxN       = 4
	antiMinSupport = 2
	antiMinLift    = 1.5
	// Safety: skip pass entirely if corpus is too small to tell signal
	// from noise.
	antiMinEpisodesForStats = 10
)

func (AntiPatternDetector) Run(ctx *Context) (Stats, error) {
	episodes, err := loadEpisodes(ctx)
	if err != nil {
		return nil, err
	}
	if len(episodes) < antiMinEpisodesForStats {
		return Stats{
			"episodes_seen":       len(episodes),
			"skipped":             true,
			"reason":              "corpus too small for lift calculation",
			"anti_patterns_upserted": 0,
		}, nil
	}

	// Baseline failure rate across all episodes (excluding unknown outcomes
	// — we don't want "no data" to dilute either numerator or denominator).
	var totalKnown, totalFailed int
	for _, ep := range episodes {
		switch ep.Outcome {
		case "success":
			totalKnown++
		case "failure":
			totalKnown++
			totalFailed++
		case "partial":
			// Count partial as half-failure to be conservative; anti-patterns
			// should only flag clearly bad sequences.
			totalKnown++
		}
	}
	if totalKnown == 0 || totalFailed == 0 {
		return Stats{
			"episodes_seen":           len(episodes),
			"skipped":                 true,
			"reason":                  "no failures in corpus",
			"anti_patterns_upserted":  0,
		}, nil
	}
	baseFailRate := float64(totalFailed) / float64(totalKnown)

	type ngramStats struct {
		containingEpisodes int
		failedEpisodes     int
		l2Episodes         int // strong failure: auditor rejected
		sourceIDs          []string
	}
	stats := map[string]*ngramStats{}

	for _, ep := range episodes {
		if ep.Outcome != "success" && ep.Outcome != "failure" {
			continue // skip partial/unknown for n-gram attribution
		}
		seq := strings.Fields(ep.ToolSequence)
		if len(seq) < antiMinN {
			continue
		}
		seen := map[string]bool{}
		for n := antiMinN; n <= antiMaxN; n++ {
			for i := 0; i+n <= len(seq); i++ {
				gram := strings.Join(seq[i:i+n], " ")
				if seen[gram] {
					continue
				}
				seen[gram] = true
				s, ok := stats[gram]
				if !ok {
					s = &ngramStats{}
					stats[gram] = s
				}
				s.containingEpisodes++
				if ep.Outcome == "failure" {
					s.failedEpisodes++
					if ep.AuditLevel == "L2" {
						s.l2Episodes++
					}
					if len(s.sourceIDs) < 20 {
						s.sourceIDs = append(s.sourceIDs, ep.ID)
					}
				}
			}
		}
	}

	type keeper struct {
		gram      string
		support   int
		failRate  float64
		lift      float64
		l2Count   int
		sources   []string
	}
	var keepers []keeper
	for gram, s := range stats {
		if s.containingEpisodes < antiMinSupport {
			continue
		}
		fr := float64(s.failedEpisodes) / float64(s.containingEpisodes)
		lift := fr / baseFailRate
		// Lower bar for patterns that include L2 (auditor-rejected) failures —
		// those are the strongest signal we have that a sequence is dangerous.
		effectiveMinLift := antiMinLift
		if s.l2Episodes > 0 {
			effectiveMinLift = 1.2
		}
		if lift < effectiveMinLift {
			continue
		}
		keepers = append(keepers, keeper{gram, s.containingEpisodes, fr, lift, s.l2Episodes, s.sourceIDs})
	}
	sort.Slice(keepers, func(i, j int) bool {
		if keepers[i].lift != keepers[j].lift {
			return keepers[i].lift > keepers[j].lift
		}
		return keepers[i].support > keepers[j].support
	})

	// Index episodes for O(1) resolution when building each keeper's
	// topic context — same pattern as PatternExtractor.
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
			"fail_rate":     k.failRate,
			"lift":          k.lift,
			"baseline":      baseFailRate,
			"l2_count":      k.l2Count,
		})
		summary := fmt.Sprintf("Tool sequence %q failed %.0f%% of the time (%.1fx baseline, %d observations)",
			k.gram, k.failRate*100, k.lift, k.support)
		if k.l2Count > 0 {
			summary += fmt.Sprintf("; %d were auditor-rejected (L2)", k.l2Count)
		}
		// Anti-patterns benefit most from topic context — "avoid edit→edit"
		// is vague, "avoid edit→edit when touching middleware/auth" is
		// immediately actionable.
		summary += extractTopicContext(topicSources{
			episodes: resolveEpisodes(k.sources, epIndex),
		})
		// Boost confidence when L2 observations are present — these are the
		// strongest real-world signal of a genuinely bad sequence. The boost
		// is monotone non-decreasing in l2 share: at l2_count=0 we keep the
		// raw fail_rate; as L2 share grows, confidence moves toward 1.0.
		conf := k.failRate
		if k.l2Count > 0 && k.support > 0 {
			l2Share := float64(k.l2Count) / float64(k.support)
			conf = k.failRate + (1-k.failRate)*l2Share*0.5
			if conf > 1.0 {
				conf = 1.0
			}
		}
		ka := &model.KnowledgeArtifact{
			ProjectID:    ctx.ProjectID,
			Kind:         "anti_pattern",
			Name:         antiPatternName(tools),
			Summary:      summary,
			Payload:      string(payload),
			ProducedBy:   AntiPatternDetector{}.Name(),
			SourceEvents: sourceEventsJSON(k.sources),
			Confidence:   conf,
		}
		if err := upsertArtifact(ka); err != nil {
			continue
		}
		upserted++
	}

	return Stats{
		"episodes_seen":          len(episodes),
		"baseline_fail_rate":     baseFailRate,
		"ngrams_considered":      len(stats),
		"anti_patterns_upserted": upserted,
	}, nil
}

func antiPatternName(tools []string) string {
	return "anti: " + strings.Join(tools, "→")
}
