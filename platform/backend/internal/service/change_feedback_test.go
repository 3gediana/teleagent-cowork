package service

// Integration tests for HandleChangeAudit — the closing hook of the
// client-agent self-evolution loop.
//
// Scenarios:
//   * L0 → success_count++ on each injected artifact
//   * L2 → failure_count++ on each injected artifact
//   * L1 → neither counter moves, but FeedbackApplied becomes true
//   * Second call with same changeID → no-op (idempotency)
//   * Change without injected_artifacts → no-op, no panic
//   * Unknown change → no-op, no panic
//   * Malformed injected_artifacts JSON → no-op, no panic

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/a3c/platform/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var changeFBDBCounter int64

func setupChangeFBDB(t *testing.T) func() {
	t.Helper()
	prev := model.DB
	n := atomic.AddInt64(&changeFBDBCounter, 1)
	dsn := fmt.Sprintf("file:changefb_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.KnowledgeArtifact{}, &model.Change{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	model.DB = db
	return func() { model.DB = prev }
}

// seedChangeWithArtifacts creates a Change referencing `artifactIDs` and
// the underlying artifact rows. Each artifact starts with usage_count=1
// (mimicking the claim-time bump) so we can see success/failure layer
// cleanly on top.
func seedChangeWithArtifacts(t *testing.T, changeID string, artifactIDs []string) {
	t.Helper()
	injected := "[]"
	if len(artifactIDs) > 0 {
		parts := make([]byte, 0, 32)
		parts = append(parts, '[')
		for i, id := range artifactIDs {
			if i > 0 {
				parts = append(parts, ',')
			}
			parts = append(parts, '"')
			parts = append(parts, []byte(id)...)
			parts = append(parts, '"')
		}
		parts = append(parts, ']')
		injected = string(parts)
	}
	if err := model.DB.Create(&model.Change{
		ID: changeID, ProjectID: "p1", AgentID: "a1",
		Version: "v1", InjectedArtifacts: injected,
	}).Error; err != nil {
		t.Fatalf("seed change: %v", err)
	}
	for _, id := range artifactIDs {
		if err := model.DB.Create(&model.KnowledgeArtifact{
			ID: id, ProjectID: "p1", Kind: "pattern", Name: id,
			Status: "active", Version: 1, UsageCount: 1,
		}).Error; err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
	}
}

func TestHandleChangeAudit_L0_BumpsSuccess(t *testing.T) {
	defer setupChangeFBDB(t)()
	seedChangeWithArtifacts(t, "chg1", []string{"a1", "a2", "a3"})

	HandleChangeAudit("chg1", "L0")

	var arts []model.KnowledgeArtifact
	model.DB.Where("id IN ?", []string{"a1", "a2", "a3"}).Find(&arts)
	for _, a := range arts {
		if a.SuccessCount != 1 {
			t.Errorf("%s: expected success_count=1, got %d", a.ID, a.SuccessCount)
		}
		if a.FailureCount != 0 {
			t.Errorf("%s: expected failure_count=0, got %d", a.ID, a.FailureCount)
		}
	}

	var ch model.Change
	model.DB.Where("id = ?", "chg1").First(&ch)
	if !ch.FeedbackApplied {
		t.Error("FeedbackApplied should be true after L0 verdict")
	}
}

func TestHandleChangeAudit_L2_BumpsFailure(t *testing.T) {
	defer setupChangeFBDB(t)()
	seedChangeWithArtifacts(t, "chg1", []string{"a1", "a2"})

	HandleChangeAudit("chg1", "L2")

	var arts []model.KnowledgeArtifact
	model.DB.Where("id IN ?", []string{"a1", "a2"}).Find(&arts)
	for _, a := range arts {
		if a.FailureCount != 1 {
			t.Errorf("%s: expected failure_count=1, got %d", a.ID, a.FailureCount)
		}
		if a.SuccessCount != 0 {
			t.Errorf("%s: expected success_count=0, got %d", a.ID, a.SuccessCount)
		}
	}
}

func TestHandleChangeAudit_L1_NeitherCounterBumps(t *testing.T) {
	defer setupChangeFBDB(t)()
	seedChangeWithArtifacts(t, "chg1", []string{"a1"})

	HandleChangeAudit("chg1", "L1")

	var a model.KnowledgeArtifact
	model.DB.Where("id = ?", "a1").First(&a)
	if a.SuccessCount != 0 || a.FailureCount != 0 {
		t.Errorf("L1 should leave both counters at 0; got success=%d failure=%d",
			a.SuccessCount, a.FailureCount)
	}

	// But FeedbackApplied still flips — without that a followup L0/L2
	// for the same change (e.g. from a retry path) would double-count.
	var ch model.Change
	model.DB.Where("id = ?", "chg1").First(&ch)
	if !ch.FeedbackApplied {
		t.Error("L1 should still flip FeedbackApplied to guard against double-fire")
	}
}

func TestHandleChangeAudit_Idempotent(t *testing.T) {
	defer setupChangeFBDB(t)()
	seedChangeWithArtifacts(t, "chg1", []string{"a1"})

	HandleChangeAudit("chg1", "L0")
	HandleChangeAudit("chg1", "L0")
	HandleChangeAudit("chg1", "L2") // even a different verdict on re-fire

	var a model.KnowledgeArtifact
	model.DB.Where("id = ?", "a1").First(&a)
	if a.SuccessCount != 1 {
		t.Errorf("expected success_count=1 after 3 calls; got %d (double-firing!)", a.SuccessCount)
	}
	if a.FailureCount != 0 {
		t.Errorf("expected failure_count=0 (idempotency should block second verdict)", )
	}
}

func TestHandleChangeAudit_NoInjectedArtifacts_IsNoOp(t *testing.T) {
	defer setupChangeFBDB(t)()
	// Empty list → still create the Change but with no artifacts.
	if err := model.DB.Create(&model.Change{
		ID: "chg1", ProjectID: "p1", AgentID: "a1", Version: "v1",
		InjectedArtifacts: "",
	}).Error; err != nil {
		t.Fatal(err)
	}
	// No panic, no spurious artifacts created.
	HandleChangeAudit("chg1", "L0")

	var n int64
	model.DB.Model(&model.KnowledgeArtifact{}).Count(&n)
	if n != 0 {
		t.Errorf("expected 0 artifacts (none should be touched), got %d", n)
	}
}

func TestHandleChangeAudit_UnknownChange_IsNoOp(t *testing.T) {
	defer setupChangeFBDB(t)()
	// No change row at all — function must not panic.
	HandleChangeAudit("nonexistent", "L0")
	// Simply succeed.
}

func TestHandleChangeAudit_MalformedJSON_IsNoOp(t *testing.T) {
	defer setupChangeFBDB(t)()
	if err := model.DB.Create(&model.Change{
		ID: "chg1", ProjectID: "p1", AgentID: "a1", Version: "v1",
		InjectedArtifacts: "{not valid json",
	}).Error; err != nil {
		t.Fatal(err)
	}
	HandleChangeAudit("chg1", "L0") // must not panic
	var ch model.Change
	model.DB.Where("id = ?", "chg1").First(&ch)
	if !ch.FeedbackApplied {
		t.Error("malformed JSON should still flip FeedbackApplied to prevent re-scan loops")
	}
}

func TestHandleChangeAudit_UnknownLevel_IsNoOp(t *testing.T) {
	defer setupChangeFBDB(t)()
	seedChangeWithArtifacts(t, "chg1", []string{"a1"})

	HandleChangeAudit("chg1", "L99") // invented level

	var a model.KnowledgeArtifact
	model.DB.Where("id = ?", "a1").First(&a)
	if a.SuccessCount != 0 || a.FailureCount != 0 {
		t.Error("unknown level should not bump counters")
	}
}
