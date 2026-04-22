package service

// Injection signal metrics (PR 9)
// ===============================
//
// Once per-reason feedback is being persisted on Change.injected_artifacts
// (PR 5), we can actually answer "does semantic retrieval outperform the
// importance fallback?" at the aggregate level. This file is the
// read-only analytics side — it scans feedback-applied changes, decodes
// each change's injected refs, and tallies success vs failure per
// dominant signal ("semantic" / "importance" / "tag" / "recency" /
// "unknown" for legacy data).
//
// The output is a compact JSON shape suitable for the Knowledge
// dashboard to render as a small bar chart:
//
//   {
//     "total_changes": 42,
//     "signals": {
//       "semantic":   {"success": 18, "failure": 3, "rate": 0.86},
//       "importance": {"success":  5, "failure": 7, "rate": 0.42},
//       "recency":    {"success":  0, "failure": 2, "rate": 0.00},
//       ...
//     }
//   }
//
// No persistence — we recompute on every API call. The query scans a
// bounded recent window (the last `limit` audited changes) so the cost
// stays sub-100ms even with thousands of historical rows.

import (
	"time"

	"github.com/a3c/platform/internal/model"
)

// SignalTally is the per-signal rollup surfaced by ComputeInjectionMetrics.
type SignalTally struct {
	Success int     `json:"success"`
	Failure int     `json:"failure"`
	Rate    float64 `json:"rate"` // success / (success + failure); 0 when denominator is 0
}

// InjectionMetrics is the top-level shape. Change-count is the number of
// feedback-applied Changes inspected; it's distinct from the sum of
// signal counts because a single change contributes its injected_refs
// to *each* signal that appears in the refs.
type InjectionMetrics struct {
	TotalChanges int                     `json:"total_changes"`
	Signals      map[string]*SignalTally `json:"signals"`
	GeneratedAt  time.Time               `json:"generated_at"`
}

// ComputeInjectionMetrics scans up to `limit` recent changes (newest
// first) that have `feedback_applied=true` and an audit_level set, and
// aggregates their injected_refs by dominant signal.
//
// projectID filters; pass empty string for platform-wide aggregation.
func ComputeInjectionMetrics(projectID string, limit int) *InjectionMetrics {
	if limit <= 0 {
		limit = 500
	}

	var changes []model.Change
	q := model.DB.Model(&model.Change{}).
		Where("feedback_applied = ? AND audit_level IS NOT NULL AND injected_artifacts <> ?", true, "")
	if projectID != "" {
		q = q.Where("project_id = ?", projectID)
	}
	q.Order("reviewed_at DESC").Limit(limit).Find(&changes)

	out := &InjectionMetrics{
		TotalChanges: len(changes),
		Signals:      map[string]*SignalTally{},
		GeneratedAt:  time.Now(),
	}
	if len(changes) == 0 {
		return out
	}

	for _, ch := range changes {
		_, refs := parseInjectedArtifacts(ch.InjectedArtifacts)
		if len(refs) == 0 {
			// Legacy flat-id changes don't carry reason data; they
			// still informed a verdict, so we bucket them under
			// "unknown" to stay honest about coverage.
			bump(out, "unknown", ch.AuditLevel)
			continue
		}
		for _, r := range refs {
			bump(out, dominantSignal(r.Reason), ch.AuditLevel)
		}
	}

	// Compute rates now that totals are final.
	for _, t := range out.Signals {
		denom := t.Success + t.Failure
		if denom > 0 {
			t.Rate = float64(t.Success) / float64(denom)
		}
	}
	return out
}

// bump converts an audit level into a success/failure increment for a
// signal bucket. L1 counts as neither (matches HandleChangeAudit), so
// we quietly drop it to keep the rate denominator honest.
func bump(m *InjectionMetrics, signal string, auditLevel *string) {
	if m.Signals[signal] == nil {
		m.Signals[signal] = &SignalTally{}
	}
	if auditLevel == nil {
		return
	}
	switch *auditLevel {
	case "L0":
		m.Signals[signal].Success++
	case "L2":
		m.Signals[signal].Failure++
	}
}
