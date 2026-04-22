package refinery

import (
	"encoding/json"
	"log"
	"time"

	"github.com/a3c/platform/internal/model"
)

// sourceSnapshot is what we persist per contributing project-scoped artifact
// inside a global artifact's SourceEvents JSON blob so future promotions can
// compute deltas instead of re-adding the full source totals.
type sourceSnapshot struct {
	UsageCount   int `json:"usage_count"`
	SuccessCount int `json:"success_count"`
	FailureCount int `json:"failure_count"`
}

// decodeProvenance parses a global artifact's SourceEvents which, for
// promoted artifacts, is a JSON object keyed by source artifact ID. If the
// blob was produced by another pass (raw ID array) we return an empty map
// — the next merge starts fresh.
func decodeProvenance(raw string) map[string]sourceSnapshot {
	out := map[string]sourceSnapshot{}
	if raw == "" || raw == "[]" || raw == "null" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// GlobalPromoter copies strongly-validated project-scoped KnowledgeArtifacts
// into the global pool (project_id=""). A global artifact is available to
// every project, giving new projects useful defaults on day one.
//
// Promotion criteria (conservative — we only promote what's earned trust):
//   - status=active
//   - usage_count ≥ 20
//   - success_rate ≥ 0.8
//   - kind in {pattern, tool_recipe, anti_pattern}
//
// Anti-patterns have a looser bar (usage_count ≥ 5) because a dangerous
// sequence observed in ONE project is probably dangerous in every project.
//
// This pass only runs when ctx.ProjectID == "" (global mode) so a normal
// per-project refinery run doesn't touch the global pool.
type GlobalPromoter struct{}

func (GlobalPromoter) Name() string       { return "global_promoter/v1" }
func (GlobalPromoter) Produces() []string { return []string{"pattern", "anti_pattern", "tool_recipe"} }
func (GlobalPromoter) Requires() []string { return nil }

func (GlobalPromoter) Run(ctx *Context) (Stats, error) {
	// Only run in cross-project / global mode.
	if ctx.ProjectID != "" {
		return Stats{"skipped": true, "reason": "project-scoped run"}, nil
	}

	// Candidate: any active project-scoped artifact meeting the bar.
	var candidates []model.KnowledgeArtifact
	model.DB.Where("status = 'active' AND project_id != '' AND kind IN ?",
		[]string{"pattern", "tool_recipe", "anti_pattern"}).Find(&candidates)

	promoted := 0
	merged := 0
	for _, c := range candidates {
		minUsage := 20
		if c.Kind == "anti_pattern" {
			minUsage = 5
		}
		if c.UsageCount < minUsage {
			continue
		}
		successRate := 0.0
		if c.UsageCount > 0 {
			successRate = float64(c.SuccessCount) / float64(c.UsageCount)
		}
		// Anti-patterns are validated by the inverse: a high failure count
		// means the anti-pattern is real. For anti-patterns we skip the
		// success-rate bar.
		if c.Kind != "anti_pattern" && successRate < 0.8 {
			continue
		}

		// Check for an existing global twin by (kind, name).
		var existing model.KnowledgeArtifact
		err := model.DB.Where("project_id = '' AND kind = ? AND name = ?", c.Kind, c.Name).First(&existing).Error
		if err == nil {
			// Already globally known — merge counts via delta vs. last
			// snapshot of this source. This prevents the weekly scheduler
			// from re-adding the source's full totals on every run.
			snapshots := decodeProvenance(existing.SourceEvents)
			prev := snapshots[c.ID] // zero value if new contributor
			dUsage := c.UsageCount - prev.UsageCount
			dSuccess := c.SuccessCount - prev.SuccessCount
			dFailure := c.FailureCount - prev.FailureCount
			if dUsage == 0 && dSuccess == 0 && dFailure == 0 {
				continue // no new evidence since last merge
			}
			snapshots[c.ID] = sourceSnapshot{
				UsageCount:   c.UsageCount,
				SuccessCount: c.SuccessCount,
				FailureCount: c.FailureCount,
			}
			snapJSON, _ := json.Marshal(snapshots)
			model.DB.Model(&model.KnowledgeArtifact{}).Where("id = ?", existing.ID).
				Updates(map[string]interface{}{
					"usage_count":   existing.UsageCount + dUsage,
					"success_count": existing.SuccessCount + dSuccess,
					"failure_count": existing.FailureCount + dFailure,
					"source_events": string(snapJSON),
					"updated_at":    time.Now(),
				})
			merged++
			continue
		}

		// No global twin yet — create one, seed provenance with this source's
		// snapshot so a future run can compute deltas.
		snapshots := map[string]sourceSnapshot{
			c.ID: {
				UsageCount:   c.UsageCount,
				SuccessCount: c.SuccessCount,
				FailureCount: c.FailureCount,
			},
		}
		snapJSON, _ := json.Marshal(snapshots)

		global := &model.KnowledgeArtifact{
			ProjectID:    "",
			Kind:         c.Kind,
			Name:         c.Name,
			Summary:      "[global] " + c.Summary,
			Payload:      c.Payload,
			ProducedBy:   GlobalPromoter{}.Name(),
			SourceEvents: string(snapJSON),
			Confidence:   c.Confidence,
			Status:       "active",
			UsageCount:   c.UsageCount,
			SuccessCount: c.SuccessCount,
			FailureCount: c.FailureCount,
		}
		// Directly create to avoid upsertArtifact clobbering the counter
		// seed (its update branch preserves counters but we need the create
		// path here — not the update path). Embedding is done here too
		// since we bypass upsertArtifact: best-effort, failure is non-fatal.
		if global.ID == "" {
			global.ID = model.GenerateID("ka")
		}
		global.CreatedAt = time.Now()
		global.UpdatedAt = time.Now()
		global.Version = 1
		if blob, dim, at := embedArtifactBestEffort(global.Kind, global.Name, global.Summary); blob != nil {
			global.Embedding = blob
			global.EmbeddingDim = dim
			global.EmbeddedAt = at
		}
		if err := model.DB.Create(global).Error; err != nil {
			continue
		}
		promoted++
		log.Printf("[Refinery] Promoted artifact %s (%s) to global pool", c.Name, c.Kind)
	}

	return Stats{
		"candidates_considered": len(candidates),
		"promoted_to_global":    promoted,
		"merged_into_global":    merged,
	}, nil
}
