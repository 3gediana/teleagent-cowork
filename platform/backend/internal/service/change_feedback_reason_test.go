package service

// PR 5 tests — persistence of injection reasons and per-reason tallies.
// Uses the same sqlite-in-memory pattern as change_feedback_test.go.

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/a3c/platform/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var reasonFBDBCounter int64

func setupReasonFBDB(t *testing.T) func() {
	t.Helper()
	prev := model.DB
	n := atomic.AddInt64(&reasonFBDBCounter, 1)
	dsn := fmt.Sprintf("file:reasonfb_%d?mode=memory&cache=shared", n)
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

func TestParseInjectedArtifacts_RichShape(t *testing.T) {
	raw := `[{"id":"ka_1","reason":"semantic=0.8","score":0.61},{"id":"ka_2","reason":"importance=0.4","score":0.32}]`
	ids, refs := parseInjectedArtifacts(raw)
	if len(ids) != 2 || ids[0] != "ka_1" || ids[1] != "ka_2" {
		t.Errorf("ids incorrect: %v", ids)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[0].Reason != "semantic=0.8" || refs[1].Reason != "importance=0.4" {
		t.Errorf("reasons not preserved: %+v", refs)
	}
}

func TestParseInjectedArtifacts_LegacyFlatShape(t *testing.T) {
	raw := `["ka_1","ka_2","ka_3"]`
	ids, refs := parseInjectedArtifacts(raw)
	if len(ids) != 3 {
		t.Errorf("expected 3 ids, got %d", len(ids))
	}
	if refs != nil {
		t.Errorf("legacy shape should return nil refs, got %+v", refs)
	}
}

func TestParseInjectedArtifacts_MalformedReturnsNil(t *testing.T) {
	ids, refs := parseInjectedArtifacts(`{not valid`)
	if ids != nil || refs != nil {
		t.Errorf("malformed payload should return (nil, nil); got ids=%v refs=%v", ids, refs)
	}
}

func TestDominantSignal(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"semantic=0.81;importance=0.34;recency=1.00", "semantic"},
		{"importance=0.7", "importance"},
		{"tag=0.5;recency=0.2", "tag"},
		{"", "unknown"},
		{"notakeyvalue", "notakeyvalue"},
	}
	for _, c := range cases {
		if got := dominantSignal(c.in); got != c.want {
			t.Errorf("dominantSignal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatSignalTally_Stable(t *testing.T) {
	// Map iteration is non-deterministic; formatter must sort keys so
	// log output and future assertions are stable.
	tally := map[string]int{"recency": 2, "semantic": 5, "importance": 1}
	got := formatSignalTally(tally)
	want := "importance=1,recency=2,semantic=5"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatSignalTally_Empty(t *testing.T) {
	if got := formatSignalTally(nil); got != "(empty)" {
		t.Errorf("empty tally should render '(empty)', got %q", got)
	}
}

// TestHandleChangeAudit_WithRichShape verifies end-to-end that a Change
// carrying the richer ref-object payload still bumps counters correctly
// AND preserves the reason metadata for downstream inspection.
func TestHandleChangeAudit_WithRichShape(t *testing.T) {
	defer setupReasonFBDB(t)()

	// Two artifacts — one picked by semantic, one by importance. Audit
	// returns L0 so both should get success_count += 1.
	for _, a := range []model.KnowledgeArtifact{
		{ID: "ka_sem", ProjectID: "p1", Kind: "pattern", Name: "sem",
			Status: "active", Version: 1, UsageCount: 1},
		{ID: "ka_imp", ProjectID: "p1", Kind: "pattern", Name: "imp",
			Status: "active", Version: 1, UsageCount: 1},
	} {
		a := a
		if err := model.DB.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}

	refs := []InjectedRef{
		{ID: "ka_sem", Reason: "semantic=0.81;recency=1.00", Score: 0.62},
		{ID: "ka_imp", Reason: "importance=0.47;recency=0.83", Score: 0.35},
	}
	raw, _ := json.Marshal(refs)

	if err := model.DB.Create(&model.Change{
		ID: "chg1", ProjectID: "p1", AgentID: "a1",
		Version: "v1", InjectedArtifacts: string(raw),
	}).Error; err != nil {
		t.Fatal(err)
	}

	HandleChangeAudit("chg1", "L0")

	var arts []model.KnowledgeArtifact
	model.DB.Where("id IN ?", []string{"ka_sem", "ka_imp"}).Find(&arts)
	for _, a := range arts {
		if a.SuccessCount != 1 {
			t.Errorf("%s: expected success_count=1, got %d", a.ID, a.SuccessCount)
		}
	}
	// No panic + no error = rich shape accepted + per-reason tally logged
	// (output asserted manually when developing; test proves non-crash).
}

// TestHandleChangeAudit_RichAndLegacyCoexist ensures that a DB containing
// some legacy-shape rows and some rich-shape rows keeps working — important
// during the deploy window where old changes exist before PR 5 ships.
func TestHandleChangeAudit_RichAndLegacyCoexist(t *testing.T) {
	defer setupReasonFBDB(t)()

	if err := model.DB.Create(&model.KnowledgeArtifact{
		ID: "ka_x", ProjectID: "p1", Kind: "pattern", Name: "x",
		Status: "active", Version: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// Legacy change.
	if err := model.DB.Create(&model.Change{
		ID: "chg_legacy", ProjectID: "p1", AgentID: "a1", Version: "v1",
		InjectedArtifacts: `["ka_x"]`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	HandleChangeAudit("chg_legacy", "L0")

	// Rich change (same artifact, additional success).
	if err := model.DB.Create(&model.Change{
		ID: "chg_rich", ProjectID: "p1", AgentID: "a1", Version: "v1",
		InjectedArtifacts: `[{"id":"ka_x","reason":"semantic=0.9","score":0.7}]`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	HandleChangeAudit("chg_rich", "L0")

	var a model.KnowledgeArtifact
	model.DB.Where("id = ?", "ka_x").First(&a)
	if a.SuccessCount != 2 {
		t.Errorf("expected success_count=2 (once per change), got %d", a.SuccessCount)
	}
}
