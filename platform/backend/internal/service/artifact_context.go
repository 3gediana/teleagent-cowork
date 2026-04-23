package service

// Artifact injection context — the single entry point every prompt
// builder (Chief, Analyze, future MCP hints) uses to pick which
// KnowledgeArtifacts to inject into a session.
//
// Four component scores feed a Reciprocal Rank Fusion (RRF) combiner:
//
//   semantic_similarity   (query vs artifact vector)
//   tag_overlap           (task tags + file categories vs artifact text)
//   importance            (logistic on success/failure counts)
//   recency               (exponential decay, 30d half-life)
//
// Each signal is dense-ranked independently, then combined as
//
//   score = sum_over_signals  1 / (rrfK + rank_in_signal)
//
// RRF is scale-invariant (semantic scores in [0,1] and importance
// scores in [0,1] no longer need hand-tuned weights), tolerates
// missing signals (a zero-variance signal contributes equally to
// every candidate and washes out of the ordering), and penalises
// low-ranked candidates smoothly rather than linearly. The magic
// constant 60 matches the Cormack et al. 2009 recommendation; the
// Phase 2.5 L1 auto-tuning work will later learn per-project values.
//
// A session-diversity cap (max 2 artifacts from the same root source
// cluster) is applied before the per-kind budgeting so a single
// high-signal session can't monopolise top-K.
//
// Per-artifact `Reason` records the raw component scores so the
// feedback loop can later compute "success rate by injection reason"
// — i.e. learn whether semantic matches outperform tag matches,
// globally or per project.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/a3c/platform/internal/model"
)

// rrfK is the RRF smoothing constant. k=60 is the literature default
// (Cormack, Clarke & Büttcher 2009); it balances "rank-1 gets a big
// bump" against "rank-50 still contributes something". Phase 2.5's L1
// auto-tuning will learn per-project values; for now a single
// hardcoded value is the right default — changing it after we have
// training data is trivial.
const rrfK = 60.0

// maxArtifactsPerSourceCluster caps how many artifacts derived from
// the same root source (session / episode) can survive into the
// top-K. Prevents a single high-signal session from monopolising the
// prompt when several of its derivatives happen to match the query.
const maxArtifactsPerSourceCluster = 2

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

	// 3. Compute raw component scores for every candidate. These are
	//    the same four signals as before; only the *combining* step
	//    has changed. We keep the raw floats around for formatReason
	//    so the Reason string still tells operators what actually
	//    fired, not just the final RRF magnitude.
	now := time.Now()
	rawSem := make([]float64, len(candidates))
	rawTag := make([]float64, len(candidates))
	rawImp := make([]float64, len(candidates))
	rawRec := make([]float64, len(candidates))
	for i, a := range candidates {
		rawSem[i] = semanticScore(queryVec, a.Embedding)
		rawTag[i] = tagScore(tags, q.FileCategories, a)
		rawImp[i] = importanceScore(a)
		rawRec[i] = recencyScore(now, a)
	}

	// 4. Dense-rank each signal separately. Dense ranking means ties
	//    share the same rank (1, 2, 2, 3 — no gaps) which is important:
	//    when a signal is zero-variance across all candidates (e.g.
	//    no query embedding provided → all semantic=0) every candidate
	//    gets rank 1 and the signal contributes equally to everyone's
	//    RRF sum, effectively washing out of the ordering. That's the
	//    graceful-degradation story.
	semRanks := denseRank(rawSem)
	tagRanks := denseRank(rawTag)
	impRanks := denseRank(rawImp)
	recRanks := denseRank(rawRec)

	// 5. RRF combine: each signal contributes 1/(k+rank). Sum across
	//    signals. Higher total = better overall rank. The magic k=60
	//    balances "rank-1 gets a big bump" against "rank-50 still
	//    contributes something" and is the Cormack 2009 default.
	scored := make([]InjectedArtifact, 0, len(candidates))
	for i, a := range candidates {
		rrfScore := 1.0/(rrfK+float64(semRanks[i])) +
			1.0/(rrfK+float64(tagRanks[i])) +
			1.0/(rrfK+float64(impRanks[i])) +
			1.0/(rrfK+float64(recRanks[i]))
		scored = append(scored, InjectedArtifact{
			Artifact: a,
			Reason:   formatReason(rawSem[i], rawTag[i], rawImp[i], rawRec[i]),
			Score:    rrfScore,
		})
	}

	// 6. Sort by RRF score. Stable sort so equally-scored artifacts
	//    preserve DB order, which keeps the output deterministic.
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	// 7. Apply session-diversity cap BEFORE the per-kind budget.
	//    Rationale: if we let budgeting run first, a kind might be
	//    filled entirely from one session's derivatives; diversifying
	//    first ensures each kind sees artifacts from multiple root
	//    sessions before the budget clips them.
	scored = applySessionDiversity(scored, maxArtifactsPerSourceCluster)

	// 8. Apply per-kind budgets.
	return applyBudget(scored, q.Budget)
}

// denseRank assigns dense ranks (1 = highest value, ties share the
// same rank, no gaps) to a slice of scores. Returns a parallel slice
// where index i holds the rank of values[i].
//
// Examples:
//   [0.9, 0.5, 0.5, 0.1] → [1, 2, 2, 3]
//   [0.0, 0.0, 0.0]       → [1, 1, 1]   (all tied at the top)
//   []                    → []
//
// Dense rather than ordinal (1,2,3,4) on purpose: under RRF a
// signal with no variance (every candidate has score 0) should
// contribute equally to every candidate's total — not arbitrarily
// order them by input index. Dense ranking gives every tied value
// the same rank, so their RRF contributions cancel out of the final
// ordering.
func denseRank(values []float64) []int {
	n := len(values)
	if n == 0 {
		return nil
	}
	type idxVal struct {
		idx int
		val float64
	}
	sorted := make([]idxVal, n)
	for i, v := range values {
		sorted[i] = idxVal{idx: i, val: v}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].val > sorted[j].val })
	ranks := make([]int, n)
	rank := 0
	prev := math.Inf(1)
	for _, e := range sorted {
		if e.val < prev {
			rank++
			prev = e.val
		}
		ranks[e.idx] = rank
	}
	return ranks
}

// applySessionDiversity drops artifacts whose source cluster already
// has `maxPerCluster` entries ahead in the ranked list. Prevents a
// single high-signal session from dominating the final top-K when
// several of its derivative artifacts happen to match the query.
//
// Artifacts with no parseable source cluster (empty SourceEvents
// or malformed JSON) are treated as singletons — they never count
// toward anyone's cap and are always kept. This is the right default
// because historical artifacts pre-dating SourceEvents tracking
// should not be unfairly penalised.
func applySessionDiversity(scored []InjectedArtifact, maxPerCluster int) []InjectedArtifact {
	if maxPerCluster <= 0 || len(scored) == 0 {
		return scored
	}
	seen := map[string]int{}
	filtered := make([]InjectedArtifact, 0, len(scored))
	for _, ia := range scored {
		key := clusterKey(ia.Artifact)
		if key == "" {
			filtered = append(filtered, ia)
			continue
		}
		if seen[key] >= maxPerCluster {
			continue
		}
		seen[key]++
		filtered = append(filtered, ia)
	}
	return filtered
}

// clusterKey extracts the first source-event ID from an artifact's
// SourceEvents JSON array. This is conventionally the root episode /
// session ID the artifact was derived from, so artifacts sharing a
// first element are "from the same cluster" for diversity purposes.
// Returns empty string on empty / malformed / zero-length input.
func clusterKey(a model.KnowledgeArtifact) string {
	if a.SourceEvents == "" {
		return ""
	}
	var ids []string
	if err := json.Unmarshal([]byte(a.SourceEvents), &ids); err != nil || len(ids) == 0 {
		return ""
	}
	return ids[0]
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
