package service

// PR 9 — metrics derived from change-audit feedback.

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/a3c/platform/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var metricsDBCounter int64

func setupMetricsDB(t *testing.T) func() {
	t.Helper()
	prev := model.DB
	n := atomic.AddInt64(&metricsDBCounter, 1)
	dsn := fmt.Sprintf("file:metrics_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Change{}, &model.KnowledgeArtifact{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	model.DB = db
	return func() { model.DB = prev }
}

// seedFeedbackChange creates a feedback-applied Change with the given
// audit level and injected-refs payload for metrics to chew on.
func seedFeedbackChange(t *testing.T, id, projectID, auditLevel, refsJSON string) {
	t.Helper()
	lvl := auditLevel
	if err := model.DB.Create(&model.Change{
		ID: id, ProjectID: projectID, AgentID: "a1", Version: "v1",
		AuditLevel:        &lvl,
		InjectedArtifacts: refsJSON,
		FeedbackApplied:   true,
	}).Error; err != nil {
		t.Fatalf("seed change %s: %v", id, err)
	}
}

func TestComputeInjectionMetrics_SplitsSuccessFailureByDominantSignal(t *testing.T) {
	defer setupMetricsDB(t)()

	// Two L0 (success) changes, each citing a semantic-picked artifact.
	seedFeedbackChange(t, "c1", "p1", "L0",
		`[{"id":"ka_a","reason":"semantic=0.81;importance=0.30","score":0.61}]`)
	seedFeedbackChange(t, "c2", "p1", "L0",
		`[{"id":"ka_b","reason":"semantic=0.75","score":0.55}]`)
	// One L2 (failure) change citing an importance-picked artifact.
	seedFeedbackChange(t, "c3", "p1", "L2",
		`[{"id":"ka_c","reason":"importance=0.47","score":0.35}]`)

	m := ComputeInjectionMetrics("p1", 0)
	if m.TotalChanges != 3 {
		t.Errorf("expected 3 changes inspected, got %d", m.TotalChanges)
	}
	sem := m.Signals["semantic"]
	if sem == nil || sem.Success != 2 || sem.Failure != 0 {
		t.Errorf("semantic bucket wrong: %+v", sem)
	}
	imp := m.Signals["importance"]
	if imp == nil || imp.Success != 0 || imp.Failure != 1 {
		t.Errorf("importance bucket wrong: %+v", imp)
	}
	if sem.Rate != 1.0 {
		t.Errorf("expected semantic rate=1.0, got %.2f", sem.Rate)
	}
	if imp.Rate != 0.0 {
		t.Errorf("expected importance rate=0.0, got %.2f", imp.Rate)
	}
}

func TestComputeInjectionMetrics_LegacyPayloadFallsBackToUnknown(t *testing.T) {
	defer setupMetricsDB(t)()

	// Legacy flat-id payload has no reason info. Metrics should still
	// count the change under "unknown" rather than drop it.
	seedFeedbackChange(t, "c1", "p1", "L0", `["ka_a","ka_b"]`)

	m := ComputeInjectionMetrics("p1", 0)
	unknown := m.Signals["unknown"]
	if unknown == nil || unknown.Success != 1 {
		t.Errorf("expected unknown success=1, got %+v", unknown)
	}
}

func TestComputeInjectionMetrics_L1DoesNotMoveCounters(t *testing.T) {
	defer setupMetricsDB(t)()
	// L1 is the "fixable, partial" verdict — neither success nor
	// failure, so it must not pollute rates.
	seedFeedbackChange(t, "c1", "p1", "L1",
		`[{"id":"ka_a","reason":"semantic=0.8"}]`)

	m := ComputeInjectionMetrics("p1", 0)
	if m.TotalChanges != 1 {
		t.Errorf("expected 1 change inspected, got %d", m.TotalChanges)
	}
	sem := m.Signals["semantic"]
	// Bucket exists (bump was called) but both counters stay zero;
	// rate is 0 because denominator is 0.
	if sem == nil || sem.Success != 0 || sem.Failure != 0 {
		t.Errorf("L1 must not move counters; got %+v", sem)
	}
}

func TestComputeInjectionMetrics_IgnoresChangesMissingFeedback(t *testing.T) {
	defer setupMetricsDB(t)()

	// A change with an audit level but feedback_applied=false must
	// not show up — feedback hasn't actually been accounted yet.
	lvl := "L0"
	model.DB.Create(&model.Change{
		ID: "c_pending", ProjectID: "p1", AgentID: "a1", Version: "v1",
		AuditLevel:        &lvl,
		InjectedArtifacts: `[{"id":"ka_a","reason":"semantic=0.8"}]`,
		FeedbackApplied:   false,
	})

	m := ComputeInjectionMetrics("p1", 0)
	if m.TotalChanges != 0 {
		t.Errorf("expected to skip un-applied feedback; got TotalChanges=%d", m.TotalChanges)
	}
}

func TestComputeInjectionMetrics_ProjectScoped(t *testing.T) {
	defer setupMetricsDB(t)()

	seedFeedbackChange(t, "c1", "p1", "L0",
		`[{"id":"ka_a","reason":"semantic=0.8"}]`)
	seedFeedbackChange(t, "c2", "p2", "L0",
		`[{"id":"ka_b","reason":"importance=0.5"}]`)

	m1 := ComputeInjectionMetrics("p1", 0)
	if m1.Signals["semantic"] == nil || m1.Signals["semantic"].Success != 1 {
		t.Errorf("p1 should count its own change: %+v", m1.Signals)
	}
	if m1.Signals["importance"] != nil {
		t.Errorf("p1 must NOT include p2's importance bucket")
	}

	m2 := ComputeInjectionMetrics("p2", 0)
	if m2.Signals["importance"] == nil || m2.Signals["importance"].Success != 1 {
		t.Errorf("p2 should count its own change: %+v", m2.Signals)
	}
}
