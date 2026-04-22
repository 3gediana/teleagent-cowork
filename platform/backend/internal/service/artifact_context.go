package service

// Artifact injection context — the single entry point every prompt
// builder (Chief, Analyze, future MCP hints) uses to pick which
// KnowledgeArtifacts to inject into a session.
//
// Replaces the old "top-20 by confidence" blunt filter with a scoped,
// multi-signal selector:
//
//   score =  0.55 * semantic_similarity        (query vs artifact vector)
//          + 0.20 * tag_overlap                (placeholder today — the
//                                               multi-source Tag lifecycle
//                                               PR will fill this in)
//          + 0.15 * importance                 (usage/success/failure)
//          + 0.10 * recency                    (exponential decay, 30d)
//
// Each factor is normalised to [0, 1]. Missing signals (no embedding,
// no tags yet, cold artifact) degrade gracefully to 0 so the selector
// never crashes on partial data.
//
// Per-artifact `Reason` is recorded so the feedback loop can later
// compute "success rate by injection reason" — i.e. learn whether
// semantic matches outperform tag matches, globally or per project.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/a3c/platform/internal/model"
)

// Audience tells the selector which subset of artifacts is relevant.
// Anti-patterns go to auditors; tool recipes to coders. One artifact
// can still serve multiple audiences via its kind.
type Audience string

const (
	AudienceCoder     Audience = "coder"     // MCP client agents claiming tasks
	AudienceAuditor   Audience = "auditor"   // Audit1 / Fix / Audit2
	AudienceReviewer  Audience = "reviewer"  // Evaluate / Merge
	AudienceCommander Audience = "commander" // Chief Agent
	AudienceAnalyzer  Audience = "analyzer"  // Analyze Agent (full-width view)
)

// kindsForAudience decides which artifact kinds are relevant per role.
// Rationale: don't inject PR-focused recipes into Audit prompts; don't
// inject audit anti-patterns into a recipe-hungry coder's toolbelt.
func kindsForAudience(a Audience) []string {
	switch a {
	case AudienceCoder:
		return []string{"tool_recipe", "pattern", "anti_pattern"}
	case AudienceAuditor:
		return []string{"anti_pattern"}
	case AudienceReviewer:
		return []string{"anti_pattern", "tool_recipe"}
	case AudienceCommander:
		return []string{"pattern", "anti_pattern", "tool_recipe"}
	case AudienceAnalyzer:
		return []string{"pattern", "anti_pattern", "tool_recipe"}
	default:
		return []string{"pattern", "anti_pattern", "tool_recipe"}
	}
}

// Budget controls how many artifacts of each kind we'll keep. The
// audience-specific defaults stop prompts from being dominated by any
// single signal type.
type Budget struct {
	MaxPerKind map[string]int // empty → defaults for the audience
	MaxTotal   int            // hard cap across all kinds
}

func defaultBudget(a Audience) Budget {
	switch a {
	case AudienceCoder:
		return Budget{MaxPerKind: map[string]int{"tool_recipe": 3, "pattern": 3, "anti_pattern": 4}, MaxTotal: 10}
	case AudienceAuditor:
		return Budget{MaxPerKind: map[string]int{"anti_pattern": 6}, MaxTotal: 6}
	case AudienceReviewer:
		return Budget{MaxPerKind: map[string]int{"anti_pattern": 5, "tool_recipe": 2}, MaxTotal: 7}
	case AudienceCommander:
		return Budget{MaxPerKind: map[string]int{"pattern": 2, "anti_pattern": 3, "tool_recipe": 2}, MaxTotal: 7}
	case AudienceAnalyzer:
		return Budget{MaxPerKind: map[string]int{"pattern": 10, "anti_pattern": 10, "tool_recipe": 10}, MaxTotal: 30}
	default:
		return Budget{MaxPerKind: map[string]int{"pattern": 4, "anti_pattern": 4, "tool_recipe": 2}, MaxTotal: 10}
	}
}

// ArtifactQuery describes the context an injection is being built for.
// All fields except ProjectID and Audience are optional; richer data
// yields better ranking but the selector always returns sensible
// results on minimal input.
type ArtifactQuery struct {
	ProjectID string   // scope: artifacts for this project + globals
	Audience  Audience // who is this prompt for

	// Semantic query signals. Either provide the embedding directly
	// (cheapest — task embeddings are already cached on the Task row),
	// or the selector will embed the QueryText on the fly.
	QueryEmbedding []float32
	QueryText      string

	// TaskID — when set, the selector will auto-load the task's
	// WeightedTags from the TaskTag table if the caller didn't fill
	// WeightedTags explicitly. Cheapest way to benefit from the tag
	// lifecycle (PR 6) without every caller duplicating the lookup.
	TaskID string

	// WeightedTags — (tag, weight) pairs driving the tag-overlap score.
	// Typically derived from the task's confirmed + proposed TaskTag
	// rows via LoadTaskTagsForSelector. Confirmed tags carry full
	// weight; proposed ones a fraction of it so bad rule fires don't
	// hijack the ranking.
	WeightedTags   []WeightedTag
	FileCategories []string

	Budget Budget // zero value → defaults for the audience
}

// WeightedTag is one signal driving tagScore. Weight is a multiplier in
// [0, 1]: 1.0 for confirmed tags, ~0.4 for proposed, 0 for rejected /
// superseded (normally filtered out before construction).
type WeightedTag struct {
	Tag    string
	Weight float64
}

// InjectedArtifact is a ranked selection result. `Reason` records
// *why* this artifact was picked so the feedback loop can later
// evaluate selector quality per-reason-class.
type InjectedArtifact struct {
	Artifact model.KnowledgeArtifact
	Reason   string  // e.g. "semantic=0.81;tag=bugfix"
	Score    float64 // final weighted score
}

// SelectArtifactsForInjection is the single entry point. Returns up to
// Budget.MaxTotal artifacts, already sorted by score (highest first).
// Returns nil (not error) when the project has no eligible artifacts —
// callers should treat "no relevant knowledge" as a normal state.
func SelectArtifactsForInjection(ctx context.Context, q ArtifactQuery) []InjectedArtifact {
	if q.ProjectID == "" {
		return nil
	}
	if q.Audience == "" {
		q.Audience = AudienceCommander
	}
	if q.Budget.MaxTotal == 0 && q.Budget.MaxPerKind == nil {
		q.Budget = defaultBudget(q.Audience)
	}

	// 1. Coarse recall: project + global, active/candidate, kinds for
	//    this audience. Load a generous candidate pool (up to 200) so
	//    the ranking step has real competition to choose from.
	candidates := fetchInjectionCandidates(q.ProjectID, q.Audience, 200)
	if len(candidates) == 0 {
		return nil
	}

	// 2. Resolve query embedding (caller-provided beats live embed).
	queryVec := q.QueryEmbedding
	if len(queryVec) == 0 && q.QueryText != "" {
		vec, err := DefaultEmbeddingClient().EmbedQuery(ctx, q.QueryText)
		if err == nil {
			queryVec = vec
		}
		// On error we continue — the selector still works with
		// semantic=0 and falls back to importance + recency + tags.
	}

	// 2b. Auto-load task tags when only TaskID was provided. Lets every
	//     call site benefit from the tag lifecycle without each one
	//     re-implementing the confirmed/proposed distinction.
	tags := q.WeightedTags
	if len(tags) == 0 && q.TaskID != "" {
		tags = LoadTaskTagsForSelector(q.TaskID)
	}

	// 3. Score every candidate.
	now := time.Now()
	scored := make([]InjectedArtifact, 0, len(candidates))
	for _, a := range candidates {
		sem := semanticScore(queryVec, a.Embedding)
		tag := tagScore(tags, q.FileCategories, a)
		imp := importanceScore(a)
		rec := recencyScore(now, a)

		final := 0.55*sem + 0.20*tag + 0.15*imp + 0.10*rec
		scored = append(scored, InjectedArtifact{
			Artifact: a,
			Reason:   formatReason(sem, tag, imp, rec),
			Score:    final,
		})
	}

	// 4. Sort by final score, then apply per-kind budgets.
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	return applyBudget(scored, q.Budget)
}

// fetchInjectionCandidates pulls a pre-filtered candidate pool scoped to
// the project (+ globals), status ∈ {active, candidate}, and audience-
// appropriate kinds.
func fetchInjectionCandidates(projectID string, audience Audience, limit int) []model.KnowledgeArtifact {
	kinds := kindsForAudience(audience)
	var arts []model.KnowledgeArtifact
	// Ordering by confidence here is just to bias the 200-row cut in
	// our favour if the project has a huge backlog; the real ordering
	// happens in scoring below.
	model.DB.
		Where("(project_id = ? OR project_id = '') AND status IN ? AND kind IN ?",
			projectID, []string{"active", "candidate"}, kinds).
		Order("confidence DESC, updated_at DESC").
		Limit(limit).
		Find(&arts)
	return arts
}

// -- Scoring components ---------------------------------------------------

// semanticScore: cosine similarity in [-1, 1], remapped to [0, 1] by
// (x+1)/2 so negative similarities (very dissimilar) still contribute a
// tiny amount rather than subtracting from the total.
func semanticScore(query []float32, artifactBlob []byte) float64 {
	if len(query) == 0 || len(artifactBlob) == 0 {
		return 0
	}
	artifactVec := UnmarshalEmbedding(artifactBlob)
	if len(artifactVec) != len(query) {
		return 0
	}
	sim := CosineSimilarity(query, artifactVec)
	return float64((sim + 1.0) / 2.0)
}

// tagScore computes a weighted tag-overlap signal between the task's
// WeightedTags + file categories and tokens present in the artifact's
// payload / name / summary. Artifact-side tagging doesn't have its own
// column yet; instead we rely on the payload already containing
// discriminative tokens (tool_recipe has "task_tag":"bugfix" verbatim;
// pattern/anti_pattern carries "file_category":"go").
//
// Formula: sum the weight of every matched tag (plus a small constant
// for each matched file category), capped at 1.0.
//
//   - No tags, no file cats → 0 (silent, not negative)
//   - One proposed match @ 0.4 → 0.4
//   - One confirmed match @ 1.0 → 1.0 (confirmed > proposed by design)
//   - Multiple proposed matches can layer up to 1.0
//
// Using capped sum (not normalized ratio) is deliberate: normalization
// would make "1 of 1 confirmed tag matched" and "1 of 1 proposed tag
// matched" score identically, which defeats the confirmed/proposed
// weighting entirely. Capping at 1.0 keeps the signal bounded for the
// final weighted combination.
//
// Substring matching is coarse but good-enough for the current payload
// shapes; false positives are acceptable because tagScore only carries
// 20% of the final injection weight.
func tagScore(tags []WeightedTag, fileCats []string, a model.KnowledgeArtifact) float64 {
	if len(tags) == 0 && len(fileCats) == 0 {
		return 0
	}
	haystack := strings.ToLower(a.Payload + " " + a.Name + " " + a.Summary)
	if haystack == "" {
		return 0
	}

	var hitWeight float64
	for _, t := range tags {
		if t.Weight <= 0 || t.Tag == "" {
			continue
		}
		if strings.Contains(haystack, strings.ToLower(t.Tag)) {
			hitWeight += t.Weight
		}
	}
	// File categories contribute a small fixed bump (0.25 each) on
	// match, never enough on their own to dominate but helpful when
	// they reinforce a tag signal.
	const fileCatBump = 0.25
	for _, fc := range fileCats {
		if fc == "" {
			continue
		}
		if strings.Contains(haystack, strings.ToLower(fc)) {
			hitWeight += fileCatBump
		}
	}
	if hitWeight > 1.0 {
		return 1.0
	}
	return hitWeight
}

// LoadTaskTagsForSelector returns the weighted tag list we feed into
// tagScore for a given task. Rejected / superseded tags are filtered
// out here; confirmed tags carry their full confidence, proposed tags
// a fraction (their raw confidence, which the rule engine deliberately
// caps below 0.5).
//
// Returning a nil slice when there are no useful tags is fine — the
// selector degrades to semantic + importance + recency gracefully.
func LoadTaskTagsForSelector(taskID string) []WeightedTag {
	if taskID == "" {
		return nil
	}
	var tags []model.TaskTag
	model.DB.Where("task_id = ? AND status IN ?", taskID,
		[]string{"confirmed", "proposed"}).Find(&tags)
	if len(tags) == 0 {
		return nil
	}
	out := make([]WeightedTag, 0, len(tags))
	for _, t := range tags {
		weight := 0.0
		switch t.Status {
		case "confirmed":
			weight = 1.0
		case "proposed":
			// Use the producer's own confidence as the weight.
			// Rule engine emits 0.3-0.5; humans would be at 1.0
			// but by definition aren't in the "proposed" bucket.
			if t.Confidence > 0 {
				weight = t.Confidence
			} else {
				weight = 0.3
			}
		}
		if weight > 0 {
			out = append(out, WeightedTag{Tag: t.Tag, Weight: weight})
		}
	}
	return out
}

// importanceScore: ExpeL-inspired — more successful uses = higher
// importance, failures subtract. Mapped to [0, 1] via a logistic so
// early hits (+2) already give a small signal and runaway hits don't
// dominate.
func importanceScore(a model.KnowledgeArtifact) float64 {
	// Raw "importance points":
	//   +2 per success, -3 per failure, +0 for candidate vs active
	//   base (we keep confidence as a separate signal rather than
	//   conflating it in here).
	raw := 2*float64(a.SuccessCount) - 3*float64(a.FailureCount)
	// Logistic with midpoint at 5 points, slope ~0.3 → saturates around
	// raw=20 (≈ "proven pattern"), still discriminates near zero.
	return 1.0 / (1.0 + math.Exp(-(raw-5.0)*0.3))
}

// recencyScore: exp(-age × ln2 / half_life). Half-life = 30 days so a
// fresh artifact roughly doubles the score of a month-old one, all else
// equal. Clamped to [0, 1] even for negative ages (clock skew).
func recencyScore(now time.Time, a model.KnowledgeArtifact) float64 {
	ts := a.UpdatedAt
	if ts.IsZero() {
		ts = a.CreatedAt
	}
	if ts.IsZero() {
		return 0
	}
	ageDays := now.Sub(ts).Hours() / 24.0
	if ageDays < 0 {
		return 1
	}
	const halfLifeDays = 30.0
	return math.Exp(-ageDays * math.Ln2 / halfLifeDays)
}

// -- Budget application ---------------------------------------------------

// applyBudget walks the sorted list and emits artifacts until we hit a
// per-kind cap or the total cap. Sorted input guarantees the highest-
// scoring artifact of each kind wins any cap competition.
func applyBudget(scored []InjectedArtifact, b Budget) []InjectedArtifact {
	if b.MaxTotal <= 0 {
		return scored
	}
	perKind := make(map[string]int, len(b.MaxPerKind))
	out := make([]InjectedArtifact, 0, b.MaxTotal)
	for _, ia := range scored {
		if len(out) >= b.MaxTotal {
			break
		}
		cap := b.MaxPerKind[ia.Artifact.Kind]
		if cap == 0 {
			// Kind not budgeted → allow but count toward total.
		} else if perKind[ia.Artifact.Kind] >= cap {
			continue
		}
		perKind[ia.Artifact.Kind]++
		out = append(out, ia)
	}
	return out
}

// -- Reason formatting ----------------------------------------------------

func formatReason(sem, tag, imp, rec float64) string {
	// Only emit the parts that actually contributed, so low-signal
	// components don't pollute the reason string. Order mirrors the
	// scoring weights.
	parts := []string{}
	if sem > 0 {
		parts = append(parts, fmt.Sprintf("semantic=%.2f", sem))
	}
	if tag > 0 {
		parts = append(parts, fmt.Sprintf("tag=%.2f", tag))
	}
	if imp > 0 {
		parts = append(parts, fmt.Sprintf("importance=%.2f", imp))
	}
	if rec > 0 {
		parts = append(parts, fmt.Sprintf("recency=%.2f", rec))
	}
	if len(parts) == 0 {
		return "fallback"
	}
	return strings.Join(parts, ";")
}
