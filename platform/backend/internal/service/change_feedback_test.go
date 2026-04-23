package service

// Integration tests for HandleChangeAudit — the closing hook of the
// client-agent self-evolution loop.
//
// Scenarios:
//   * L0 → success_count++ on the top-K attributed artifacts
//   * L1 → success_count++ on the top-1 attributed (partial credit)
//   * L2 → failure_count++ on the top-K attributed artifacts
//   * Second call with same changeID → no-op (idempotency)
//   * Change without injected_artifacts → no-op, no panic
//   * Unknown change → no-op, no panic
//   * Malformed injected_artifacts JSON → no-op, no panic
//   * With InjectedRefs carrying Score → top-by-score credited, not first-K
//   * Many artifacts injected → attribution cap (3) enforced

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
	// 3 injected → attributionK=2 → top-2 get credit, tail stays at 0.
	// Without refs the fallback picks the first two (selector already
	// stored them in rank order), so a1/a2 get +1 and a3 stays 0.
	seedChangeWithArtifacts(t, "chg1", []string{"a1", "a2", "a3"})

	HandleChangeAudit("chg1", "L0")

	var a1, a2, a3 model.KnowledgeArtifact
	model.DB.Where("id = ?", "a1").First(&a1)
	model.DB.Where("id = ?", "a2").First(&a2)
	model.DB.Where("id = ?", "a3").First(&a3)
	if a1.SuccessCount != 1 || a2.SuccessCount != 1 {
		t.Errorf("top-2 should be credited: got a1=%d a2=%d", a1.SuccessCount, a2.SuccessCount)
	}
	if a3.SuccessCount != 0 {
		t.Errorf("tail artifact (a3) should NOT be credited under rank-based attribution, got %d", a3.SuccessCount)
	}
	if a1.FailureCount != 0 || a2.FailureCount != 0 || a3.FailureCount != 0 {
		t.Error("L0 must not touch failure_count")
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

func TestHandleChangeAudit_L1_GivesPartialCredit(t *testing.T) {
	defer setupChangeFBDB(t)()
	// 3 injected, L1 → only top-1 should get success_count=1. Partial
	// credit reflects "direction was right" without over-rewarding the
	// tail. Previously L1 dropped the signal entirely.
	seedChangeWithArtifacts(t, "chg1", []string{"a1", "a2", "a3"})

	HandleChangeAudit("chg1", "L1")

	var a1, a2, a3 model.KnowledgeArtifact
	model.DB.Where("id = ?", "a1").First(&a1)
	model.DB.Where("id = ?", "a2").First(&a2)
	model.DB.Where("id = ?", "a3").First(&a3)
	if a1.SuccessCount != 1 {
		t.Errorf("top-1 (a1) should get partial credit on L1, got success=%d", a1.SuccessCount)
	}
	if a2.SuccessCount != 0 || a3.SuccessCount != 0 {
		t.Errorf("only top-1 gets L1 partial credit; got a2=%d a3=%d", a2.SuccessCount, a3.SuccessCount)
	}
	if a1.FailureCount+a2.FailureCount+a3.FailureCount != 0 {
		t.Error("L1 must not touch failure_count")
	}

	var ch model.Change
	model.DB.Where("id = ?", "chg1").First(&ch)
	if !ch.FeedbackApplied {
		t.Error("L1 should flip FeedbackApplied")
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
		t.Errorf("expected failure_count=0 (idempotency should block second verdict)")
	}
}

// TestHandleChangeAudit_L0_WithRefs_UsesScoreRanking verifies that when
// the Change row stores the rich InjectedRefs payload with scores, the
// attribution picks the top-by-Score artifacts instead of the first-K
// by list order. Seeds scores inversely to list order so the test
// fails if the ranking falls through to the fallback.
func TestHandleChangeAudit_L0_WithRefs_UsesScoreRanking(t *testing.T) {
	defer setupChangeFBDB(t)()

	// Seed artifacts a1..a3; inject with refs where a3 > a2 > a1 by score.
	// Expectation: attributionK(3)=2, top-2 by score = a3, a2. a1 stays 0.
	seedArtifactsOnly(t, []string{"a1", "a2", "a3"})
	richRefs := `[{"id":"a1","reason":"semantic=0.20","score":0.20},` +
		`{"id":"a2","reason":"semantic=0.50","score":0.50},` +
		`{"id":"a3","reason":"semantic=0.90","score":0.90}]`
	if err := model.DB.Create(&model.Change{
		ID: "chg1", ProjectID: "p1", AgentID: "a1", Version: "v1",
		InjectedArtifacts: richRefs,
	}).Error; err != nil {
		t.Fatal(err)
	}

	HandleChangeAudit("chg1", "L0")

	var a1, a2, a3 model.KnowledgeArtifact
	model.DB.Where("id = ?", "a1").First(&a1)
	model.DB.Where("id = ?", "a2").First(&a2)
	model.DB.Where("id = ?", "a3").First(&a3)
	if a2.SuccessCount != 1 || a3.SuccessCount != 1 {
		t.Errorf("top-2 by Score (a2, a3) should be credited; got a2=%d a3=%d", a2.SuccessCount, a3.SuccessCount)
	}
	if a1.SuccessCount != 0 {
		t.Errorf("lowest-scoring artifact (a1) should NOT be credited; got %d", a1.SuccessCount)
	}
}

// TestHandleChangeAudit_ManyInjected_CapsAt3 verifies attributionK caps
// at 3 even when the injection pool is larger. With 6 injected the old
// "bump all" behaviour would credit 6; the new policy credits only 3.
func TestHandleChangeAudit_ManyInjected_CapsAt3(t *testing.T) {
	defer setupChangeFBDB(t)()
	ids := []string{"a1", "a2", "a3", "a4", "a5", "a6"}
	seedChangeWithArtifacts(t, "chg1", ids)

	HandleChangeAudit("chg1", "L0")

	var arts []model.KnowledgeArtifact
	model.DB.Where("id IN ?", ids).Find(&arts)
	credited := 0
	for _, a := range arts {
		if a.SuccessCount == 1 {
			credited++
		}
	}
	if credited != 3 {
		t.Errorf("attributionK cap=3; expected 3 artifacts credited out of 6, got %d", credited)
	}
}

// seedArtifactsOnly seeds the artifact rows without creating a Change
// — used when the test needs to craft a custom InjectedArtifacts
// payload instead of the default flat-id list.
func seedArtifactsOnly(t *testing.T, ids []string) {
	t.Helper()
	for _, id := range ids {
		if err := model.DB.Create(&model.KnowledgeArtifact{
			ID: id, ProjectID: "p1", Kind: "pattern", Name: id,
			Status: "active", Version: 1, UsageCount: 1,
		}).Error; err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
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
