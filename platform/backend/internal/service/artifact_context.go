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

// ClusterCooldownDecay is the multiplicative penalty per recent
// occurrence applied to RRF score when ArtifactQuery.RecentTopClusters
// is set. 0.85 means: cluster appearing 1 time in window → score
// × 0.85, 3 times → × 0.61, 5 times → × 0.44. Calibrated so a
// genuinely-best artifact still wins after one repeat (sem-rank-1
// vs sem-rank-2 RRF margin is wide enough to absorb a single 0.85
// hit) but loses to a closely-ranked competitor after the same
// cluster monopolises three or more rounds. Var (not const) so the
// L1 weight tuner can override at runtime.
var ClusterCooldownDecay = 0.85

// rrfK is the RRF smoothing constant. k=60 is the literature default
// (Cormack, Clarke & Büttcher 2009); it balances "rank-1 gets a big
// bump" against "rank-50 still contributes something". Phase 2.5's L1
// auto-tuning will learn per-project values; for now a single
// default is the right choice — changing it after we have training
// data is trivial.
//
// Declared as a var (not const) so benchmarks and Phase 2.5 tuning
// tools can override it via SetRRFK without a rebuild. Production
// start-up leaves it at 60; evobench sweeps 30/60/90 to characterise
// the signal-weight tradeoff.
var rrfK = 60.0

// SetRRFK overrides the RRF smoothing constant. Intended for
// benchmarks and L1 auto-tuning; not called from normal request
// paths. Values ≤ 0 are rejected (would divide by zero downstream).
func SetRRFK(k float64) {
	if k > 0 {
		rrfK = k
	}
}

// RRFK returns the currently-active RRF constant. Useful for tests
// and tooling that want to restore the default after a sweep.
func RRFK() float64 { return rrfK }

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

	// RecentTopClusters: cluster IDs (matching clusterKey output) that
	// recently held top-1 in this project, in any order. Each occurrence
	// multiplies that cluster's RRF score by ClusterCooldownDecay before
	// sorting, so two consecutive top-1s in the same cluster get
	// progressively penalised. Empty (the default) disables the
	// cross-round diversity mechanism entirely.
	//
	// Why this is opt-in instead of automatic: dispatchers that do
	// rapid-fire claims for the same project genuinely benefit, but a
	// one-shot probe (Chief context build, Analyze pass) doesn't have
	// any "recent" history and a stateless selector keeps unit tests
	// trivial. The caller — typically the task dispatcher — owns the
	// rolling window.
	RecentTopClusters []string

	// TrustedClusters: cluster IDs flagged as legitimately
	// high-quality by an independent grader (LLM judge cumulative
	// agreement, ops review, hand-curated allowlist). Artifacts in
	// these clusters are EXEMPT from RecentTopClusters cooldown
	// even if they appear in the recent window — the exemption is
	// the whole point of having this field. Empty (the default)
	// means "no trust signal available, apply cooldown uniformly".
	//
	// Why this is opt-in: cluster trust requires an external signal
	// source. evobench wires it from cumulative judge agreement
	// (see cluster_trust.go); production paths leave it empty
	// until an offline judging job exists to populate the trust
	// counter persistently. See cluster_trust.go header for the
	// full design rationale.
	TrustedClusters []string

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

	// Signals exposes the four raw component scores and their dense
	// ranks within this call. Populated by SelectArtifactsForInjection
	// so downstream tools (L1 weight tuner, evobench trainer) can
	// train on features without re-running the scoring pipeline.
	// All fields are zero for historical callers that don't observe
	// them.
	Signals RRFSignals
}

// RRFSignals captures the four component scores and their dense
// ranks produced by the selector. `Rank*` fields are 1-indexed.
// Exposed publicly because the L1 auto-tuning pipeline needs these
// as training features; internal ranking code should prefer the
// score fields.
type RRFSignals struct {
	Semantic   float64
	Tag        float64
	Importance float64
	Recency    float64

	RankSemantic   int
	RankTag        int
	RankImportance int
	RankRecency    int
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
			Signals: RRFSignals{
				Semantic:       rawSem[i],
				Tag:            rawTag[i],
				Importance:     rawImp[i],
				Recency:        rawRec[i],
				RankSemantic:   semRanks[i],
				RankTag:        tagRanks[i],
				RankImportance: impRanks[i],
				RankRecency:    recRanks[i],
			},
		})
	}

	// 5b. Apply optional cross-round cluster cooldown. evobench
	//     measurements showed a single "gold" cluster (the one
	//     happening to hold high-success artifacts at simulation
	//     start) monopolises top-3 across the entire run because
	//     every successful inject reinforces its importance score,
	//     which feeds back into RRF, which keeps it at top-1, etc.
	//     The fix is to let dispatchers (the only callers with a
	//     natural notion of "recent rounds") pass in a sliding
	//     window of recent top-1 cluster IDs; we multiply the
	//     offending cluster's RRF score down before sort. Caller
	//     opts in by populating q.RecentTopClusters; empty = no-op.
	//
	//     Magnitude-aware: a flat decay punishes legitimate dominance.
	//     evobench seed=42 showed ep_H sitting on 96/100 top-1 because
	//     its RRF score *legitimately* dwarfed everyone else's (gold
	//     cluster), and a flat 0.85 cooldown was forcing the wrong
	//     answer to surface. We scale the decay by how much the
	//     recent cluster dominates non-recent alternatives — strong
	//     dominance = soft penalty (gold), tied scores = full penalty
	//     (self-reinforcement). See cooldownDecayFactor.
	//
	//     Practical limit (evobench 2026-04): RRF dense-rank fusion
	//     compresses cluster-level score gaps to ~1-2% even when the
	//     gold cluster wins rank 1 on every signal. Any decay strong
	//     enough to break a self-reinforced monopoly (≥ 5%) also
	//     flips a gold cluster of typical margin. Magnitude-aware
	//     therefore behaves like flat decay in the common case and
	//     only abstains when RRF gaps are unusually wide (e.g. the
	//     runner-up is many ranks behind on multiple signals at
	//     once). To genuinely preserve gold clusters we need a
	//     non-RRF trust signal — e.g. cumulative judge-agreement
	//     per cluster, hand-curated allowlist, or smoothed
	//     success-count weighting fed in via ArtifactQuery. Tracked
	//     in backlog as "Cluster trust signal for cooldown gating".
	if len(q.RecentTopClusters) > 0 {
		counts := map[string]int{}
		for _, c := range q.RecentTopClusters {
			if c != "" {
				counts[c]++
			}
		}
		// Trust set: clusters that have been independently
		// corroborated by a grader and therefore should NOT be
		// penalised even if they appear in the recent window.
		// Empty by default (production path); evobench fills this
		// from cumulative judge agreement.
		trusted := map[string]bool{}
		for _, c := range q.TrustedClusters {
			if c != "" {
				trusted[c] = true
			}
		}
		if len(counts) > 0 {
			// First pass: gather max RRF score per cluster across all
			// scored candidates. Used to gauge how decisively each
			// recent cluster outranks the non-recent alternatives.
			clusterMax := map[string]float64{}
			for i := range scored {
				c := clusterKey(scored[i].Artifact)
				if c == "" {
					continue
				}
				if scored[i].Score > clusterMax[c] {
					clusterMax[c] = scored[i].Score
				}
			}
			// Runner-up: the highest RRF score among clusters NOT in
			// the recent window. If no such cluster exists (every
			// cluster in the candidate pool is recent), runnerUp
			// stays 0, which makes cooldownDecayFactor abstain — we
			// can't tell gold from self-reinforcement without a
			// reference point.
			var runnerUp float64
			for c, score := range clusterMax {
				if _, recent := counts[c]; recent {
					continue
				}
				if score > runnerUp {
					runnerUp = score
				}
			}
			// Second pass: apply magnitude-aware decay per artifact,
			// skipping artifacts whose cluster is in the trust set.
			// The skip is the entire point of TrustedClusters —
			// see cluster_trust.go for the design rationale and
			// the path by which this field gets populated.
			for i := range scored {
				c := clusterKey(scored[i].Artifact)
				n := counts[c]
				if n == 0 {
					continue
				}
				if trusted[c] {
					continue
				}
				scored[i].Score *= cooldownDecayFactor(clusterMax[c], runnerUp, n, ClusterCooldownDecay)
			}
		}
	}

	// 6. Sort by RRF score. When scores tie (which is common when every
	//    component rank ties — e.g. no query embedding + no tags + no
	//    importance variance), break the tie by artifact ID so two calls
	//    with the same input always produce the same output. Stable sort
	//    alone wasn't enough: it preserves the candidate-pool order, but
	//    that order itself was non-deterministic before we added id ASC
	//    to fetchInjectionCandidates above. Belt-and-braces: keep both.
	//
	// Recency note (evobench finding, 2026-04): across 4 random seeds the
	// recency signal won 0% of top-1 selections in normal mode and 0%
	// in degraded mode. Mathematics: with rrfK=60 and dense ranks 1…R,
	// the marginal RRF contribution from rank-1 vs rank-2 is identical
	// for every signal (≈1/3782). Once the candidate pool has any
	// semantic spread, sem rank-1 vs rank-3 swamps any single rec
	// rank-1 vs rank-2 differential. Don't trust this means recency
	// is useless in production — the synthetic fixture deliberately
	// gives every topic a clean unit-vector basis, which separates
	// sem ranks much more cleanly than real artifact summaries do.
	// L1 weight tuning (TODO) on real Change feedback should answer
	// whether to keep recency, drop it, or boost it.
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Artifact.ID < scored[j].Artifact.ID
	})

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

// cooldownDecayFactor returns the multiplicative penalty to apply
// to an artifact whose cluster appeared `n` times in the recent
// window. The decay scales with how decisively the cluster
// dominates non-recent alternatives:
//
//	dominance := clamp((myMax - runnerUpScore) / myMax, 0, 1)
//	effective := baseDecay + (1 - baseDecay) * dominance
//	factor    := effective ^ n
//
// At dominance=0 (recent cluster's score ties or barely beats the
// runner-up): full baseDecay applies — we read this as
// self-reinforcement and want to break the monopoly.
//
// At dominance=1 (recent cluster utterly dwarfs everyone else):
// effective collapses to 1.0, no penalty — we read this as a
// "gold cluster" that legitimately deserves to keep winning.
//
// Edge cases:
//   - n <= 0 or myMax <= 0: identity (1.0). The caller has no
//     reason to penalise this cluster.
//   - runnerUpScore == 0 with myMax > 0: every cluster in the
//     candidate pool is in the recent window. We have no reference
//     point to gauge dominance, so we abstain (return 1.0). Better
//     to leave ranking untouched than to make a uniformly wrong
//     decision; the caller's window will eventually slide and
//     restore comparability.
func cooldownDecayFactor(myMax, runnerUpScore float64, n int, baseDecay float64) float64 {
	if n <= 0 || myMax <= 0 {
		return 1.0
	}
	dominance := 1.0
	if runnerUpScore > 0 {
		dominance = (myMax - runnerUpScore) / myMax
		if dominance < 0 {
			dominance = 0
		}
		if dominance > 1 {
			dominance = 1
		}
	}
	effective := baseDecay + (1.0-baseDecay)*dominance
	return math.Pow(effective, float64(n))
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
	//
	// id ASC is the final tie-breaker — without it, two artifacts with
	// identical (confidence, updated_at) come back in driver-dependent
	// order (SQLite rowid vs. MySQL row layout), and that propagates all
	// the way through to the caller because the RRF score can also tie
	// when every component rank ties. Sorting by a unique key at the
	// bottom guarantees the candidate pool itself is deterministic.
	model.DB.
		Where("(project_id = ? OR project_id = '') AND status IN ? AND kind IN ?",
			projectID, []string{"active", "candidate"}, kinds).
		Order("confidence DESC, updated_at DESC, id ASC").
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
