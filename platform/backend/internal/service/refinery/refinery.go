// Package refinery turns raw agent activity (AgentSessions + ToolCallTraces +
// Experiences) into typed, attributable knowledge artifacts through a chain
// of deterministic passes.
//
// Each Pass is a small function with a single, auditable job — e.g. "group
// tool calls into episodes", "extract frequent success patterns", "detect
// anti-patterns in L2-rejected sessions". Passes declare what Kinds they
// Require and Produce, and the runner orders them by that dependency graph.
//
// Design goals:
//   - Deterministic first: v1 passes use frequency mining, not LLMs, so
//     results are reproducible bit-for-bit and cheap to re-run.
//   - Provenance everywhere: every KnowledgeArtifact records ProducedBy and
//     SourceEvents so we can always answer "where did this come from?".
//   - Additive: existing Analyze Agent flow keeps working; the refinery
//     feeds the same SkillCandidate/Policy tables as one additional source.
package refinery

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/a3c/platform/internal/model"
)

// Pass is one stage in the refinery. Implementations must be safe to run
// repeatedly over the same data: Run() should be idempotent given the same
// ProjectID and lookback window, relying on dedup indexes or upserts.
type Pass interface {
	// Name uniquely identifies this pass version, e.g. "pattern_extractor/v1".
	Name() string
	// Produces returns the KnowledgeArtifact.Kind values this pass outputs.
	// Empty means "no artifacts" (e.g. EpisodeGrouper writes to its own table).
	Produces() []string
	// Requires returns artifact kinds (or special tokens like "episode",
	// "experience") this pass depends on. Used for topological ordering.
	Requires() []string
	// Run executes the pass. It must return a human-readable stats map and
	// only an error if the pass itself is broken — "no new data" is not an
	// error, it's a stat.
	Run(ctx *Context) (Stats, error)
}

// Context carries shared state for one Refinery run.
type Context struct {
	ProjectID     string
	LookbackHours int
	RunID         string
	Now           time.Time
}

// Stats is a small key→value map returned by each pass. Persisted verbatim
// onto RefineryRun.PassStats as JSON.
type Stats map[string]any

// Refinery holds the registered passes and orchestrates their execution.
type Refinery struct {
	passes []Pass
}

// New constructs a Refinery with the default set of v1 passes.
func New() *Refinery {
	return &Refinery{
		passes: []Pass{
			&EpisodeGrouper{},
			&PatternExtractor{},
			&AntiPatternDetector{},
			&ToolRecipeMiner{},
			&MetaPass{},
		},
	}
}

// NewGlobalOnly constructs a Refinery that only runs the global promotion
// pass. Intended for cross-project consolidation runs where the target
// project_id is "".
func NewGlobalOnly() *Refinery {
	return &Refinery{
		passes: []Pass{
			&GlobalPromoter{},
		},
	}
}

// Passes exposes the registered passes (mostly for introspection).
func (r *Refinery) Passes() []Pass { return r.passes }

// Run executes every pass in registration order and persists the RefineryRun
// record. It does NOT topo-sort yet — registration order is already valid
// for the current pass set. We can introduce ordering once passes grow.
func (r *Refinery) Run(projectID string, lookbackHours int, trigger string) (*model.RefineryRun, error) {
	return r.RunWithID("", projectID, lookbackHours, trigger)
}

// RunWithID is like Run but reuses an existing RefineryRun row identified
// by runID (if provided and already present in DB). The handler uses this
// to pre-create a "pending" stub that the client can poll immediately,
// without producing a duplicate row in the database.
func (r *Refinery) RunWithID(runID, projectID string, lookbackHours int, trigger string) (*model.RefineryRun, error) {
	var run *model.RefineryRun
	if runID != "" {
		var existing model.RefineryRun
		if err := model.DB.Where("id = ?", runID).First(&existing).Error; err == nil {
			existing.Status = "running"
			existing.StartedAt = time.Now()
			model.DB.Save(&existing)
			run = &existing
		}
	}
	if run == nil {
		run = &model.RefineryRun{
			ID:        model.GenerateID("rrun"),
			ProjectID: projectID,
			Trigger:   trigger,
			StartedAt: time.Now(),
			Status:    "running",
			PassStats: "{}",
		}
		if err := model.DB.Create(run).Error; err != nil {
			return nil, fmt.Errorf("create refinery_run: %w", err)
		}
	}

	ctx := &Context{
		ProjectID:     projectID,
		LookbackHours: lookbackHours,
		RunID:         run.ID,
		Now:           run.StartedAt,
	}

	allStats := map[string]Stats{}
	hadError := false
	var lastErr error

	for _, p := range r.passes {
		start := time.Now()
		stats, err := p.Run(ctx)
		if stats == nil {
			stats = Stats{}
		}
		stats["duration_ms"] = time.Since(start).Milliseconds()
		if err != nil {
			stats["error"] = err.Error()
			hadError = true
			lastErr = err
			log.Printf("[Refinery] pass %s failed: %v", p.Name(), err)
		} else {
			log.Printf("[Refinery] pass %s ok: %v", p.Name(), stats)
		}
		allStats[p.Name()] = stats
	}

	run.EndedAt = time.Now()
	run.DurationMs = int(run.EndedAt.Sub(run.StartedAt).Milliseconds())
	if hadError {
		run.Status = "partial"
		if lastErr != nil {
			run.Error = lastErr.Error()
		}
	} else {
		run.Status = "ok"
	}
	if b, err := json.Marshal(allStats); err == nil {
		run.PassStats = string(b)
	}
	model.DB.Save(run)

	// Apply artifact lifecycle rules after all passes complete
	promoted, deprecated, _ := PromoteAndDeprecateArtifacts(projectID)
	if promoted > 0 || deprecated > 0 {
		allStats["_lifecycle"] = Stats{"promoted": promoted, "deprecated": deprecated}
		if b, err := json.Marshal(allStats); err == nil {
			run.PassStats = string(b)
		}
		model.DB.Save(run)
	}

	return run, lastErr
}

// upsertArtifact writes (or updates) a KnowledgeArtifact identified by
// (ProjectID, Kind, Name). Callers pre-populate everything except the
// bookkeeping fields (ID, timestamps) and the semantic embedding — which
// this function obtains from the registered Embedder (if any).
//
// Embedding behaviour:
//   - Create path: always try to embed.
//   - Update path: re-embed only if Name or Summary changed — that's
//     the only text that feeds into the embedding. Payload changes
//     alone don't invalidate the vector.
//   - Embedder unavailable / errored: artifact is still persisted with
//     Embedding=nil. A future reconciler can back-fill.
func upsertArtifact(ka *model.KnowledgeArtifact) error {
	var existing model.KnowledgeArtifact
	err := model.DB.Where("project_id = ? AND kind = ? AND name = ?", ka.ProjectID, ka.Kind, ka.Name).First(&existing).Error
	if err == nil {
		// Update in place; preserve effectiveness counters.
		textChanged := existing.Summary != ka.Summary || existing.Name != ka.Name
		existing.Summary = ka.Summary
		existing.Payload = ka.Payload
		existing.ProducedBy = ka.ProducedBy
		existing.SourceEvents = ka.SourceEvents
		existing.Confidence = ka.Confidence
		existing.Version = existing.Version + 1
		existing.UpdatedAt = time.Now()

		if textChanged {
			if blob, dim, at := embedArtifactBestEffort(existing.Kind, existing.Name, existing.Summary); blob != nil {
				existing.Embedding = blob
				existing.EmbeddingDim = dim
				existing.EmbeddedAt = at
			}
		}
		return model.DB.Save(&existing).Error
	}
	if ka.ID == "" {
		ka.ID = model.GenerateID("ka")
	}
	ka.CreatedAt = time.Now()
	ka.UpdatedAt = time.Now()
	if ka.Status == "" {
		ka.Status = "candidate"
	}
	if ka.Version == 0 {
		ka.Version = 1
	}
	// Embed on the create path — don't overwrite if the caller already
	// supplied an embedding (future use: bulk backfill preserves caller
	// intent).
	if ka.Embedding == nil {
		if blob, dim, at := embedArtifactBestEffort(ka.Kind, ka.Name, ka.Summary); blob != nil {
			ka.Embedding = blob
			ka.EmbeddingDim = dim
			ka.EmbeddedAt = at
		}
	}
	return model.DB.Create(ka).Error
}

// PromoteAndDeprecateArtifacts applies lifecycle rules to knowledge artifacts:
//
//   - candidate → active: confidence ≥ 0.7 AND (not yet used OR success_rate ≥ 0.5)
//   - active → deprecated: usage_count ≥ 10 AND success_rate < 0.3
//
// Returns (promoted, deprecated, error).
func PromoteAndDeprecateArtifacts(projectID string) (int, int, error) {
	promoted := 0
	deprecated := 0

	// Promote high-confidence candidates that have proven themselves (or are
	// brand-new and deserve a chance).
	var candidates []model.KnowledgeArtifact
	model.DB.Where("project_id = ? AND status = ?", projectID, "candidate").Find(&candidates)
	for _, ka := range candidates {
		shouldPromote := false
		if ka.Confidence >= 0.7 {
			if ka.UsageCount == 0 {
				// Brand-new, never used yet — give it a chance
				shouldPromote = true
			} else if ka.SuccessCount > 0 && float64(ka.SuccessCount)/float64(ka.UsageCount) >= 0.5 {
				shouldPromote = true
			}
		}
		if shouldPromote {
			model.DB.Model(&model.KnowledgeArtifact{}).Where("id = ?", ka.ID).
				Updates(map[string]interface{}{"status": "active", "updated_at": time.Now()})
			promoted++
		}
	}

	// Deprecate low-effectiveness active artifacts
	var active []model.KnowledgeArtifact
	model.DB.Where("project_id = ? AND status = ? AND usage_count >= ?", projectID, "active", 10).Find(&active)
	for _, ka := range active {
		successRate := float64(ka.SuccessCount) / float64(ka.UsageCount)
		if successRate < 0.3 {
			model.DB.Model(&model.KnowledgeArtifact{}).Where("id = ?", ka.ID).
				Updates(map[string]interface{}{"status": "deprecated", "updated_at": time.Now()})
			deprecated++
		}
	}

	if promoted > 0 || deprecated > 0 {
		log.Printf("[Refinery] Lifecycle: promoted=%d deprecated=%d project=%s", promoted, deprecated, projectID)
	}
	return promoted, deprecated, nil
}

// sourceEventsJSON serialises a slice of IDs into the canonical form used
// by KnowledgeArtifact.SourceEvents.
func sourceEventsJSON(ids []string) string {
	if len(ids) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ids)
	return string(b)
}
