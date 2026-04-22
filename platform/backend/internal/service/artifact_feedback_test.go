package service

// Integration tests for the refinery artifact feedback loop.
//
// HandleSessionCompletion is the hook called by the opencode scheduler
// when an agent session finishes. It must bump success_count /
// failure_count on every KnowledgeArtifact that was injected into that
// session's prompt.

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

var feedbackTestDBCounter int64

func setupFeedbackDB(t *testing.T) func() {
	t.Helper()
	prev := model.DB
	n := atomic.AddInt64(&feedbackTestDBCounter, 1)
	dsn := fmt.Sprintf("file:fb_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentSession{}, &model.KnowledgeArtifact{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	model.DB = db
	return func() { model.DB = prev }
}

func seedSessionWithArtifacts(t *testing.T, sessionID string, artifactIDs []string) {
	t.Helper()
	b, _ := json.Marshal(artifactIDs)
	if err := model.DB.Create(&model.AgentSession{
		ID:                sessionID,
		Role:              "chief",
		ProjectID:         "p1",
		Status:            "completed",
		InjectedArtifacts: string(b),
	}).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
	for _, id := range artifactIDs {
		if err := model.DB.Create(&model.KnowledgeArtifact{
			ID:        id,
			ProjectID: "p1",
			Kind:      "pattern",
			Name:      id,
			Status:    "active",
			Version:   1,
		}).Error; err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
	}
}

func TestHandleSessionCompletion_BumpsSuccessOnCompleted(t *testing.T) {
	defer setupFeedbackDB(t)()
	seedSessionWithArtifacts(t, "s1", []string{"a1", "a2"})

	HandleSessionCompletion("s1", "p1", "chief", "completed")

	var arts []model.KnowledgeArtifact
	model.DB.Where("id IN ?", []string{"a1", "a2"}).Find(&arts)
	for _, a := range arts {
		if a.SuccessCount != 1 {
			t.Errorf("%s: expected success_count=1, got %d", a.ID, a.SuccessCount)
		}
		if a.FailureCount != 0 {
			t.Errorf("%s: expected failure_count=0, got %d", a.ID, a.FailureCount)
		}
	}
}

func TestHandleSessionCompletion_BumpsFailureOnRejected(t *testing.T) {
	defer setupFeedbackDB(t)()
	seedSessionWithArtifacts(t, "s1", []string{"a1"})

	HandleSessionCompletion("s1", "p1", "chief", "rejected")

	var a model.KnowledgeArtifact
	model.DB.Where("id = ?", "a1").First(&a)
	if a.FailureCount != 1 || a.SuccessCount != 0 {
		t.Errorf("expected failure=1 success=0, got failure=%d success=%d",
			a.FailureCount, a.SuccessCount)
	}
}

func TestHandleSessionCompletion_NoArtifactsIsNoOp(t *testing.T) {
	defer setupFeedbackDB(t)()
	// Session with empty InjectedArtifacts — handler must not panic and
	// must not create spurious KnowledgeArtifact rows.
	if err := model.DB.Create(&model.AgentSession{
		ID: "s1", Role: "chief", ProjectID: "p1", Status: "completed",
		InjectedArtifacts: "",
	}).Error; err != nil {
		t.Fatal(err)
	}
	HandleSessionCompletion("s1", "p1", "chief", "completed")

	var n int64
	model.DB.Model(&model.KnowledgeArtifact{}).Count(&n)
	if n != 0 {
		t.Errorf("expected 0 artifacts, got %d", n)
	}
}

func TestHandleSessionCompletion_UnknownSessionIsNoOp(t *testing.T) {
	defer setupFeedbackDB(t)()
	// Session doesn't exist at all — must silently no-op (the hook could
	// fire for sessions from before the new schema).
	HandleSessionCompletion("does-not-exist", "p1", "chief", "completed")
}

func TestHandleSessionCompletion_IgnoresNonTerminalStatus(t *testing.T) {
	defer setupFeedbackDB(t)()
	seedSessionWithArtifacts(t, "s1", []string{"a1"})

	HandleSessionCompletion("s1", "p1", "chief", "running")

	var a model.KnowledgeArtifact
	model.DB.Where("id = ?", "a1").First(&a)
	if a.SuccessCount != 0 || a.FailureCount != 0 {
		t.Errorf("non-terminal status should not bump counters; got succ=%d fail=%d",
			a.SuccessCount, a.FailureCount)
	}
}
