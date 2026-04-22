package refinery

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/a3c/platform/internal/model"
)

// MetaPass observes the outputs of previous passes and writes a single
// "pass_report" KnowledgeArtifact summarising each producer's real-world
// effectiveness. The report is itself just metadata — it doesn't change
// thresholds directly, but operators (and future auto-tuning logic) can
// read it to see which passes are pulling their weight.
//
// Effectiveness per producer = Σ(success) / Σ(usage) across its artifacts,
// plus the share of its candidates that got promoted to active.
//
// Idempotent: overwrites its own single artifact per project on each run.
type MetaPass struct{}

func (MetaPass) Name() string       { return "meta_pass/v1" }
func (MetaPass) Produces() []string { return []string{"pass_report"} }

// MetaPass tolerates an empty artifact set (it simply writes an empty
// report), so it has no hard Requires. It should still run LAST — the
// refinery executes passes in registration order, which is sufficient
// until we introduce real topological ordering.
func (MetaPass) Requires() []string { return nil }

func (MetaPass) Run(ctx *Context) (Stats, error) {
	var artifacts []model.KnowledgeArtifact
	q := model.DB.Model(&model.KnowledgeArtifact{}).Where("kind != ?", "pass_report")
	if ctx.ProjectID != "" {
		q = q.Where("project_id = ?", ctx.ProjectID)
	}
	// Limit to artifacts created in the lookback window so the report
	// reflects "recent performance" rather than all-time accumulation.
	if ctx.LookbackHours > 0 {
		since := ctx.Now.Add(-time.Duration(ctx.LookbackHours) * time.Hour)
		q = q.Where("created_at >= ?", since)
	}
	q.Find(&artifacts)

	type perProducer struct {
		Count         int     `json:"count"`
		Active        int     `json:"active"`
		Candidate     int     `json:"candidate"`
		Deprecated    int     `json:"deprecated"`
		TotalUsage    int     `json:"total_usage"`
		TotalSuccess  int     `json:"total_success"`
		TotalFailure  int     `json:"total_failure"`
		SuccessRate   float64 `json:"success_rate"`
		PromotionRate float64 `json:"promotion_rate"`
	}
	byProducer := map[string]*perProducer{}

	for _, a := range artifacts {
		p, ok := byProducer[a.ProducedBy]
		if !ok {
			p = &perProducer{}
			byProducer[a.ProducedBy] = p
		}
		p.Count++
		switch a.Status {
		case "active":
			p.Active++
		case "candidate":
			p.Candidate++
		case "deprecated":
			p.Deprecated++
		}
		p.TotalUsage += a.UsageCount
		p.TotalSuccess += a.SuccessCount
		p.TotalFailure += a.FailureCount
	}

	for _, p := range byProducer {
		if p.TotalUsage > 0 {
			p.SuccessRate = float64(p.TotalSuccess) / float64(p.TotalUsage)
		}
		if p.Count > 0 {
			p.PromotionRate = float64(p.Active) / float64(p.Count)
		}
	}

	payload, _ := json.Marshal(map[string]any{
		"generated_at":   ctx.Now.UTC().Format(time.RFC3339),
		"lookback_hours": ctx.LookbackHours,
		"by_producer":    byProducer,
	})

	summary := "Meta report:"
	for name, p := range byProducer {
		summary += fmt.Sprintf(" [%s count=%d active=%d succ=%.0f%%]",
			name, p.Count, p.Active, p.SuccessRate*100)
	}
	if len(byProducer) == 0 {
		summary = "Meta report: no artifacts in window"
	}

	ka := &model.KnowledgeArtifact{
		ProjectID:  ctx.ProjectID,
		Kind:       "pass_report",
		Name:       "meta: pass effectiveness",
		Summary:    summary,
		Payload:    string(payload),
		ProducedBy: MetaPass{}.Name(),
		Confidence: 1.0,
		Status:     "active", // report is always "published"
	}
	if err := upsertArtifact(ka); err != nil {
		return nil, err
	}

	return Stats{
		"artifacts_analyzed": len(artifacts),
		"producers":          len(byProducer),
	}, nil
}
