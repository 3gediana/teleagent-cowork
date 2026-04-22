package service

// PR 7 — Analyze Agent's tag-review hook.
//
// Guards the self-evolution loop: the Analyze Agent can confirm or
// reject rule-proposed tags using real execution signal, BUT it must
// never overwrite a decision a human already made. Tests pin the
// safety contract + the happy paths.

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/a3c/platform/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var analyzeReviewDBCounter int64

func setupAnalyzeReviewDB(t *testing.T) func() {
	t.Helper()
	prev := model.DB
	n := atomic.AddInt64(&analyzeReviewDBCounter, 1)
	dsn := fmt.Sprintf("file:anareview_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskTag{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	model.DB = db
	return func() { model.DB = prev }
}

// --- AnalyzeReviewTag -----------------------------------------------------

func TestAnalyzeReviewTag_ConfirmsProposed(t *testing.T) {
	defer setupAnalyzeReviewDB(t)()
	model.DB.Create(&model.TaskTag{
		ID: "tg1", TaskID: "t1", Tag: "bugfix", Dimension: "category",
		Status: "proposed", Confidence: 0.4, Source: "auto_kw",
	})

	if err := AnalyzeReviewTag("tg1", "confirm", "execution failed as a bugfix-class event"); err != nil {
		t.Fatalf("AnalyzeReviewTag: %v", err)
	}

	var got model.TaskTag
	model.DB.Where("id = ?", "tg1").First(&got)
	if got.Status != "confirmed" {
		t.Errorf("expected confirmed, got %s", got.Status)
	}
	if got.ReviewedBy != "analyze/v1" {
		t.Errorf("reviewer should be analyze/v1, got %q", got.ReviewedBy)
	}
	if got.Confidence != 1.0 {
		t.Errorf("confidence should bump to 1.0 on confirm, got %.2f", got.Confidence)
	}
}

func TestAnalyzeReviewTag_RejectsProposed(t *testing.T) {
	defer setupAnalyzeReviewDB(t)()
	model.DB.Create(&model.TaskTag{
		ID: "tg1", TaskID: "t1", Tag: "bugfix", Dimension: "category",
		Status: "proposed", Confidence: 0.4, Source: "auto_kw",
	})

	if err := AnalyzeReviewTag("tg1", "reject", "actually a refactor per tool trace"); err != nil {
		t.Fatalf("AnalyzeReviewTag: %v", err)
	}

	var got model.TaskTag
	model.DB.Where("id = ?", "tg1").First(&got)
	if got.Status != "rejected" {
		t.Errorf("expected rejected, got %s", got.Status)
	}
}

func TestAnalyzeReviewTag_RespectsHumanDecision(t *testing.T) {
	defer setupAnalyzeReviewDB(t)()
	// A tag a human already confirmed should NEVER be flipped by
	// Analyze even if Analyze thinks otherwise — human wins.
	model.DB.Create(&model.TaskTag{
		ID: "tg1", TaskID: "t1", Tag: "bugfix", Dimension: "category",
		Status: "confirmed", Confidence: 1.0,
		ReviewedBy: "human:pm_alice",
	})

	if err := AnalyzeReviewTag("tg1", "reject", "trying to override"); err != nil {
		t.Fatalf("should silently skip, got error: %v", err)
	}

	var got model.TaskTag
	model.DB.Where("id = ?", "tg1").First(&got)
	if got.Status != "confirmed" {
		t.Errorf("human decision must stand; got %s", got.Status)
	}
	if got.ReviewedBy != "human:pm_alice" {
		t.Errorf("human reviewer must stand; got %q", got.ReviewedBy)
	}
}

func TestAnalyzeReviewTag_UnknownAction_NoOp(t *testing.T) {
	defer setupAnalyzeReviewDB(t)()
	model.DB.Create(&model.TaskTag{
		ID: "tg1", TaskID: "t1", Tag: "bugfix", Dimension: "category",
		Status: "proposed", Confidence: 0.4,
	})
	if err := AnalyzeReviewTag("tg1", "explode", ""); err != nil {
		t.Fatalf("unknown action must be silent no-op, got %v", err)
	}
	var got model.TaskTag
	model.DB.Where("id = ?", "tg1").First(&got)
	if got.Status != "proposed" {
		t.Errorf("unknown action must leave status alone; got %s", got.Status)
	}
}

func TestAnalyzeReviewTag_UnknownTag_ReturnsError(t *testing.T) {
	defer setupAnalyzeReviewDB(t)()
	if err := AnalyzeReviewTag("nonexistent", "confirm", ""); err == nil {
		t.Error("expected error on missing tag")
	}
}

// --- HandleAnalyzeOutput.tag_reviews + tag_suggestions integration -------

func TestHandleAnalyzeOutput_TagReviewsBranch(t *testing.T) {
	defer setupAnalyzeReviewDB(t)()
	// Seed two proposed tags. Analyze confirms one, rejects the other.
	model.DB.Create(&model.Task{ID: "t1", ProjectID: "p1", Name: "demo", CreatedBy: "h"})
	model.DB.Create(&model.TaskTag{
		ID: "tg_good", TaskID: "t1", Tag: "bugfix", Dimension: "category",
		Status: "proposed", Confidence: 0.5, Source: "auto_kw",
	})
	model.DB.Create(&model.TaskTag{
		ID: "tg_bad", TaskID: "t1", Tag: "feature", Dimension: "category",
		Status: "proposed", Confidence: 0.5, Source: "auto_kw",
	})

	args := map[string]any{
		"tag_reviews": []map[string]any{
			{"tag_id": "tg_good", "action": "confirm", "note": "real crash"},
			{"tag_id": "tg_bad", "action": "reject", "note": "not a feature"},
			{"tag_id": "tg_missing", "action": "confirm"}, // must not break the whole batch
		},
	}
	if err := HandleAnalyzeOutput("sess1", "p1", args); err != nil {
		t.Fatalf("HandleAnalyzeOutput: %v", err)
	}

	var good, bad model.TaskTag
	model.DB.Where("id = ?", "tg_good").First(&good)
	model.DB.Where("id = ?", "tg_bad").First(&bad)
	if good.Status != "confirmed" {
		t.Errorf("tg_good should be confirmed, got %s", good.Status)
	}
	if bad.Status != "rejected" {
		t.Errorf("tg_bad should be rejected, got %s", bad.Status)
	}
}

func TestHandleAnalyzeOutput_TagSuggestionsLandAsConfirmed(t *testing.T) {
	defer setupAnalyzeReviewDB(t)()
	model.DB.Create(&model.Task{ID: "t1", ProjectID: "p1", Name: "demo", CreatedBy: "h"})

	args := map[string]any{
		"tag_suggestions": []map[string]any{
			{
				"task_id":        "t1",
				"suggested_tags": []string{"security"},
				"dimension":      "category",
			},
		},
	}
	if err := HandleAnalyzeOutput("sess1", "p1", args); err != nil {
		t.Fatalf("HandleAnalyzeOutput: %v", err)
	}

	var got []model.TaskTag
	model.DB.Where("task_id = ? AND tag = ?", "t1", "security").Find(&got)
	if len(got) != 1 {
		t.Fatalf("expected 1 security tag row, got %d", len(got))
	}
	if got[0].Status != "confirmed" {
		t.Errorf("analyze-suggested tag should land as confirmed, got %s", got[0].Status)
	}
	if got[0].Source != "analyze" {
		t.Errorf("source should be 'analyze', got %q", got[0].Source)
	}
	if got[0].Confidence < 0.8 {
		t.Errorf("analyze tag confidence should be high (~0.85), got %.2f", got[0].Confidence)
	}
}

func TestHandleAnalyzeOutput_TagSuggestionsSkipRejected(t *testing.T) {
	defer setupAnalyzeReviewDB(t)()
	model.DB.Create(&model.Task{ID: "t1", ProjectID: "p1", Name: "demo", CreatedBy: "h"})
	// Human already rejected this tag. Analyze's suggestion must NOT
	// resurrect it via a fresh row (which would reintroduce the
	// problem for the injection selector).
	model.DB.Create(&model.TaskTag{
		ID: "rejected", TaskID: "t1", Tag: "security", Dimension: "category",
		Status: "rejected", ReviewedBy: "human:pm",
	})

	args := map[string]any{
		"tag_suggestions": []map[string]any{
			{"task_id": "t1", "suggested_tags": []string{"security"}, "dimension": "category"},
		},
	}
	if err := HandleAnalyzeOutput("sess1", "p1", args); err != nil {
		t.Fatalf("HandleAnalyzeOutput: %v", err)
	}

	var got []model.TaskTag
	model.DB.Where("task_id = ? AND dimension = ? AND tag = ?", "t1", "category", "security").Find(&got)
	if len(got) != 1 {
		t.Errorf("should still have exactly 1 security row (rejected), got %d", len(got))
	}
	if got[0].Status != "rejected" {
		t.Errorf("human rejection must stand, got %s", got[0].Status)
	}
}
