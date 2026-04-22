package service

// PR 6 — tag rule engine, lifecycle transitions, and tagScore integration.
// Uses the sqlite-in-memory pattern established earlier.

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/a3c/platform/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var tagTestDBCounter int64

func setupTagLifecycleDB(t *testing.T) func() {
	t.Helper()
	prev := model.DB
	n := atomic.AddInt64(&tagTestDBCounter, 1)
	dsn := fmt.Sprintf("file:taglc_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskTag{}, &model.KnowledgeArtifact{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	model.DB = db
	return func() { model.DB = prev }
}

// -- rule engine ---------------------------------------------------------

func TestProposeTags_FiresOnKeywordMatches(t *testing.T) {
	proposals := ProposeTagsFromText("修复 JWT 鉴权 bug", "生产环境 401 故障")
	// Expect at least bugfix (category) and security or backend (layer)
	seen := map[string]bool{}
	for _, p := range proposals {
		seen[p.Dimension+"/"+p.Tag] = true
	}
	if !seen["category/bugfix"] {
		t.Errorf("expected category/bugfix; got %v", seen)
	}
	if !seen["category/security"] && !seen["layer/backend"] {
		t.Errorf("expected security or backend tag; got %v", seen)
	}
}

func TestProposeTags_EmptyTextEmitsNothing(t *testing.T) {
	proposals := ProposeTagsFromText("", "")
	if len(proposals) != 0 {
		t.Errorf("expected 0 proposals on empty input, got %d", len(proposals))
	}
}

func TestProposeTags_DeduplicatesOnDimensionTag(t *testing.T) {
	// "fix" primary + "修复" primary should both fire the same rule;
	// dedupeProposals should keep only one row.
	proposals := ProposeTagsFromText("fix 修复 bug", "")
	count := 0
	for _, p := range proposals {
		if p.Dimension == "category" && p.Tag == "bugfix" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("dedup failed: %d 'bugfix' proposals, want 1", count)
	}
}

func TestProposeTags_HighConfidenceOnCoOccurrence(t *testing.T) {
	// Multiple keywords in bugfix rule → confidence should hit 0.5 cap.
	proposals := ProposeTagsFromText("fix bug crash", "")
	var got float64
	for _, p := range proposals {
		if p.Dimension == "category" && p.Tag == "bugfix" {
			got = p.Confidence
		}
	}
	if got < 0.4 {
		t.Errorf("co-occurring keywords should score ≥ 0.4, got %.2f", got)
	}
}

// -- persistence ---------------------------------------------------------

func TestProposeAndPersist_CreatesProposedRows(t *testing.T) {
	defer setupTagLifecycleDB(t)()
	if err := model.DB.Create(&model.Task{
		ID: "t1", ProjectID: "p1", Name: "修复 bug", CreatedBy: "h1",
	}).Error; err != nil {
		t.Fatal(err)
	}

	ProposeAndPersistTagsForTask("t1", "修复 bug", "crash")

	var tags []model.TaskTag
	model.DB.Where("task_id = ?", "t1").Find(&tags)
	if len(tags) == 0 {
		t.Fatal("expected at least one persisted tag")
	}
	for _, tg := range tags {
		if tg.Status != "proposed" {
			t.Errorf("%s: expected status=proposed, got %s", tg.Tag, tg.Status)
		}
		if tg.Source != "auto_kw" {
			t.Errorf("%s: expected source=auto_kw, got %s", tg.Tag, tg.Source)
		}
		if tg.Evidence == "" {
			t.Errorf("%s: evidence missing", tg.Tag)
		}
	}
}

func TestProposeAndPersist_IdempotentSkipsExisting(t *testing.T) {
	defer setupTagLifecycleDB(t)()
	model.DB.Create(&model.Task{
		ID: "t1", ProjectID: "p1", Name: "修复 bug", CreatedBy: "h1",
	})

	// First run populates.
	ProposeAndPersistTagsForTask("t1", "修复 bug", "crash")
	var first int64
	model.DB.Model(&model.TaskTag{}).Where("task_id = ?", "t1").Count(&first)

	// Second run must not add duplicates.
	ProposeAndPersistTagsForTask("t1", "修复 bug", "crash")
	var second int64
	model.DB.Model(&model.TaskTag{}).Where("task_id = ?", "t1").Count(&second)

	if first != second {
		t.Errorf("second propose added duplicates: %d → %d", first, second)
	}
}

func TestProposeAndPersist_RejectedTagBlocksReproposal(t *testing.T) {
	defer setupTagLifecycleDB(t)()
	model.DB.Create(&model.Task{
		ID: "t1", ProjectID: "p1", Name: "修复 bug", CreatedBy: "h1",
	})

	// Seed a rejected bugfix tag.
	model.DB.Create(&model.TaskTag{
		ID: "seed1", TaskID: "t1", Tag: "bugfix", Dimension: "category",
		Status: "rejected", Source: "human", Confidence: 0,
	})

	ProposeAndPersistTagsForTask("t1", "修复 bug", "crash")

	// Still exactly 1 bugfix/category row, still rejected.
	var tags []model.TaskTag
	model.DB.Where("task_id = ? AND dimension = ? AND tag = ?", "t1", "category", "bugfix").Find(&tags)
	if len(tags) != 1 {
		t.Errorf("expected rejected tag to block repropose; got %d bugfix rows", len(tags))
	}
	if tags[0].Status != "rejected" {
		t.Errorf("rejected tag must stay rejected, got %s", tags[0].Status)
	}
}

// -- lifecycle transitions ----------------------------------------------

func TestConfirmTag_FlipsStatusAndFullConfidence(t *testing.T) {
	defer setupTagLifecycleDB(t)()
	model.DB.Create(&model.TaskTag{
		ID: "tg1", TaskID: "t1", Tag: "bugfix", Dimension: "category",
		Status: "proposed", Confidence: 0.4, Source: "auto_kw",
	})

	if err := ConfirmTag("tg1", "alice", "looks right"); err != nil {
		t.Fatalf("ConfirmTag: %v", err)
	}
	var got model.TaskTag
	model.DB.Where("id = ?", "tg1").First(&got)
	if got.Status != "confirmed" {
		t.Errorf("expected confirmed, got %s", got.Status)
	}
	if got.Confidence != 1.0 {
		t.Errorf("expected confidence=1.0, got %.2f", got.Confidence)
	}
	if got.ReviewedBy != "alice" {
		t.Errorf("expected reviewer=alice, got %q", got.ReviewedBy)
	}
	if got.ReviewedAt == nil {
		t.Error("ReviewedAt should be set")
	}
}

func TestRejectTag_ZerosConfidence(t *testing.T) {
	defer setupTagLifecycleDB(t)()
	model.DB.Create(&model.TaskTag{
		ID: "tg1", TaskID: "t1", Tag: "bugfix", Dimension: "category",
		Status: "proposed", Confidence: 0.4,
	})
	if err := RejectTag("tg1", "alice", "actually a feature"); err != nil {
		t.Fatalf("RejectTag: %v", err)
	}
	var got model.TaskTag
	model.DB.Where("id = ?", "tg1").First(&got)
	if got.Status != "rejected" {
		t.Errorf("expected rejected, got %s", got.Status)
	}
	if got.Confidence != 0 {
		t.Errorf("expected confidence=0, got %.2f", got.Confidence)
	}
}

func TestSupersedeTag_LinksToReplacement(t *testing.T) {
	defer setupTagLifecycleDB(t)()
	model.DB.Create(&model.TaskTag{
		ID: "old", TaskID: "t1", Tag: "bugfix", Dimension: "category",
		Status: "confirmed", Confidence: 1,
	})
	model.DB.Create(&model.TaskTag{
		ID: "new", TaskID: "t1", Tag: "security", Dimension: "category",
		Status: "confirmed", Confidence: 1,
	})
	if err := SupersedeTag("old", "new", "alice"); err != nil {
		t.Fatalf("SupersedeTag: %v", err)
	}
	var got model.TaskTag
	model.DB.Where("id = ?", "old").First(&got)
	if got.Status != "superseded" {
		t.Errorf("expected superseded, got %s", got.Status)
	}
	if got.SupersededBy != "new" {
		t.Errorf("expected SupersededBy=new, got %q", got.SupersededBy)
	}
}

func TestTransition_IdempotentOnNoOp(t *testing.T) {
	defer setupTagLifecycleDB(t)()
	model.DB.Create(&model.TaskTag{
		ID: "tg1", TaskID: "t1", Tag: "bugfix", Dimension: "category",
		Status: "confirmed", Confidence: 1,
	})
	// Confirm-confirm should be a no-op.
	if err := ConfirmTag("tg1", "alice", ""); err != nil {
		t.Fatalf("ConfirmTag idempotent: %v", err)
	}
	// Verify nothing was bumped.
	var got model.TaskTag
	model.DB.Where("id = ?", "tg1").First(&got)
	if got.Status != "confirmed" || got.Confidence != 1 {
		t.Errorf("idempotent ConfirmTag mutated state: %+v", got)
	}
}

// -- selector integration -----------------------------------------------

func TestLoadTaskTagsForSelector_ExcludesRejectedSuperseded(t *testing.T) {
	defer setupTagLifecycleDB(t)()
	for _, tg := range []model.TaskTag{
		{ID: "c1", TaskID: "t1", Tag: "bugfix", Status: "confirmed", Confidence: 1},
		{ID: "p1", TaskID: "t1", Tag: "backend", Status: "proposed", Confidence: 0.4},
		{ID: "r1", TaskID: "t1", Tag: "feature", Status: "rejected", Confidence: 0},
		{ID: "s1", TaskID: "t1", Tag: "old", Status: "superseded", Confidence: 0.3},
	} {
		tg := tg
		model.DB.Create(&tg)
	}

	got := LoadTaskTagsForSelector("t1")
	if len(got) != 2 {
		t.Fatalf("expected 2 weighted tags (confirmed+proposed), got %d: %+v", len(got), got)
	}
	tagMap := map[string]float64{}
	for _, w := range got {
		tagMap[w.Tag] = w.Weight
	}
	if tagMap["bugfix"] != 1.0 {
		t.Errorf("confirmed tag weight expected 1.0, got %.2f", tagMap["bugfix"])
	}
	if tagMap["backend"] != 0.4 {
		t.Errorf("proposed tag weight expected 0.4, got %.2f", tagMap["backend"])
	}
}

func TestTagScore_ConfirmedBeatsProposed(t *testing.T) {
	a := model.KnowledgeArtifact{
		ID: "a1", Name: "recipe", Summary: "about bugfix in backend",
		Payload: `{"task_tag":"bugfix","file_category":"backend"}`,
	}
	confirmed := []WeightedTag{{Tag: "bugfix", Weight: 1.0}}
	proposed := []WeightedTag{{Tag: "bugfix", Weight: 0.4}}

	confirmedScore := tagScore(confirmed, nil, a)
	proposedScore := tagScore(proposed, nil, a)
	if confirmedScore <= proposedScore {
		t.Errorf("confirmed (%.2f) should beat proposed (%.2f) for the same tag match",
			confirmedScore, proposedScore)
	}
}

func TestTagScore_NoMatchIsZero(t *testing.T) {
	a := model.KnowledgeArtifact{
		ID: "a1", Name: "recipe", Summary: "about frontend",
		Payload: `{"task_tag":"frontend"}`,
	}
	got := tagScore([]WeightedTag{{Tag: "security", Weight: 1.0}}, nil, a)
	if got != 0 {
		t.Errorf("non-matching tag should score 0, got %.2f", got)
	}
}

func TestTagScore_EmptyInputsIsZero(t *testing.T) {
	got := tagScore(nil, nil, model.KnowledgeArtifact{})
	if got != 0 {
		t.Errorf("empty tags + empty fileCats → 0, got %.2f", got)
	}
}
