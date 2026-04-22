package service

// Task claim hints
// ================
//
// When an MCP client agent calls `task.claim`, the backend returns not
// only the task itself but a compact bundle of injectable experience
// ("hints") drawn from the refinery's KnowledgeArtifacts. This is the
// client-agent half of the injection story: Chief/Analyze already get
// their share via SelectArtifactsForInjection; the coder role on the
// MCP side previously got nothing.
//
// The hints are grouped by kind (recipes / patterns / anti-patterns)
// because the client agent uses them differently:
//
//   - tool_recipe   → "try these steps in this order"
//   - pattern       → "here's what usually works in this situation"
//   - anti_pattern  → "avoid these combinations, known failure modes"
//
// Each hint carries the artifact's summary plus a `score` and `reason`
// so the agent (or its upstream operator) can calibrate trust. Server
// also returns the list of artifact IDs that made the cut so the feedback
// loop can bump success/failure on those specific artifacts when the
// resulting Change is audited (L0/L1/L2).

import (
	"context"
	"time"

	"github.com/a3c/platform/internal/model"
)

// TaskClaimHints is the serializable shape returned to the MCP client.
// Kept JSON-stable: existing clients that ignore hints keep working,
// and new clients can opt in.
type TaskClaimHints struct {
	// IDs of every injected artifact, in score order. The client ships
	// this list back on `change.submit` so we can bump counters on the
	// exact artifacts that informed the work. Flat id array is retained
	// for backward compatibility with clients that don't parse the
	// richer `injected_refs` field below.
	InjectedIDs []string `json:"injected_ids"`

	// InjectedRefs carries the same items as InjectedIDs but also
	// preserves the SELECTOR's reasoning — the per-artifact `reason`
	// string (e.g. "semantic=0.81;importance=0.34") and `score`. When
	// a client echoes this back on change.submit, HandleChangeAudit can
	// log per-reason success rates: we finally learn whether semantic
	// retrieval outperforms importance-weighted fallback for this
	// specific client agent on this project.
	InjectedRefs []InjectedRef `json:"injected_refs"`

	// Kind-grouped items, each entry enriched for display.
	Recipes      []HintItem `json:"recipes"`
	Patterns     []HintItem `json:"patterns"`
	AntiPatterns []HintItem `json:"anti_patterns"`

	// Metadata about HOW this batch was selected — useful for the
	// client agent's operator to debug "why did I get this recipe?"
	Meta HintMeta `json:"meta"`
}

// InjectedRef is the minimal shape we persist (and the client echoes
// back) for each injected artifact. Keeping it terse — name/summary
// are available via the HintItem groups above; this one is for the
// feedback loop, not for agent display.
type InjectedRef struct {
	ID     string  `json:"id"`
	Reason string  `json:"reason"`
	Score  float64 `json:"score"`
}

// HintItem is one artifact rendered for the agent. We surface the
// `summary` rather than the raw payload so the agent gets a natural-
// language hint, not a JSON blob to parse.
type HintItem struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Summary string  `json:"summary"`
	Score   float64 `json:"score"`
	Reason  string  `json:"reason"` // "semantic=0.81;importance=0.34;..."
}

// HintMeta lets the operator see why nothing came back on an empty
// response (no artifacts yet? no task embedding? sidecar down?) and
// helps debug mis-matches.
type HintMeta struct {
	QueryHadEmbedding bool      `json:"query_had_embedding"`
	CandidatePool     int       `json:"candidate_pool"` // how many artifacts fit the coarse filter
	Selected          int       `json:"selected"`
	BuiltAt           time.Time `json:"built_at"`
}

// BuildTaskClaimHints is the public entry point. Given a task ID, it
// computes the best artifact subset for a coder-audience agent claiming
// it. Safe to call even for tasks created before the embedding work
// landed (graceful degradation: no query embedding → importance +
// recency carry the ranking).
//
// Performance note: this does one DB read for the task + one coarse
// candidate fetch + in-memory scoring. No live sidecar call when the
// task already has a cached DescriptionEmbedding (production path —
// creation triggered EmbedTaskAsync). Worst case (task has no embedding
// yet) we pay one sidecar round-trip, still sub-100ms on a warm model.
func BuildTaskClaimHints(ctx context.Context, taskID string) (*TaskClaimHints, error) {
	var task model.Task
	if err := model.DB.Where("id = ?", taskID).First(&task).Error; err != nil {
		return nil, err
	}

	// Prefer the cached embedding. If unavailable, fall back to live
	// text embedding — this also handles tasks created before the
	// embedding feature shipped.
	query := ArtifactQuery{
		ProjectID:      task.ProjectID,
		Audience:       AudienceCoder,
		QueryText:      taskEmbeddingText(task.Name, task.Description),
		QueryEmbedding: UnmarshalEmbedding(task.DescriptionEmbedding),
		// Hook in the tag lifecycle — selector auto-loads confirmed +
		// proposed TaskTag rows and weights them in tagScore so the
		// client gets hints that actually match the task's topic.
		TaskID: task.ID,
	}
	hadEmbedding := len(query.QueryEmbedding) > 0

	// Count candidate pool size (before budgeting / scoring) so the
	// operator can tell "we had nothing to choose from" apart from
	// "the selector rejected everything".
	pool := countCandidates(task.ProjectID, AudienceCoder)

	results := SelectArtifactsForInjection(ctx, query)

	hints := &TaskClaimHints{
		InjectedIDs:  make([]string, 0, len(results)),
		InjectedRefs: make([]InjectedRef, 0, len(results)),
		Recipes:      make([]HintItem, 0),
		Patterns:     make([]HintItem, 0),
		AntiPatterns: make([]HintItem, 0),
		Meta: HintMeta{
			QueryHadEmbedding: hadEmbedding,
			CandidatePool:     pool,
			Selected:          len(results),
			BuiltAt:           time.Now(),
		},
	}
	for _, ia := range results {
		item := HintItem{
			ID:      ia.Artifact.ID,
			Name:    ia.Artifact.Name,
			Summary: ia.Artifact.Summary,
			Score:   ia.Score,
			Reason:  ia.Reason,
		}
		hints.InjectedIDs = append(hints.InjectedIDs, ia.Artifact.ID)
		hints.InjectedRefs = append(hints.InjectedRefs, InjectedRef{
			ID:     ia.Artifact.ID,
			Reason: ia.Reason,
			Score:  ia.Score,
		})
		switch ia.Artifact.Kind {
		case "tool_recipe":
			hints.Recipes = append(hints.Recipes, item)
		case "pattern":
			hints.Patterns = append(hints.Patterns, item)
		case "anti_pattern":
			hints.AntiPatterns = append(hints.AntiPatterns, item)
		}
	}
	return hints, nil
}

func countCandidates(projectID string, audience Audience) int {
	kinds := kindsForAudience(audience)
	var n int64
	model.DB.Model(&model.KnowledgeArtifact{}).
		Where("(project_id = ? OR project_id = '') AND status IN ? AND kind IN ?",
			projectID, []string{"active", "candidate"}, kinds).
		Count(&n)
	return int(n)
}
