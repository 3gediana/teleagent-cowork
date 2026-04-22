package service

// Integration tests for BuildTaskClaimHints — the code path MCP clients
// hit on task.claim. Covers:
//
//   * hints bundle is non-empty when relevant artifacts exist
//   * kind routing (recipes go to Recipes field, etc.)
//   * InjectedIDs preserves ranking order
//   * tasks without a cached embedding still get usable results (falls
//     back to live embed if sidecar is up, importance + recency if not)
//   * unknown task ID returns an error, not a panic
//
// Tests use the hand-written embedding vectors + stub server pattern
// established by artifact_context_test.go so they run offline.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a3c/platform/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var hintsDBCounter int64

func setupHintsDB(t *testing.T) func() {
	t.Helper()
	prev := model.DB
	n := atomic.AddInt64(&hintsDBCounter, 1)
	dsn := fmt.Sprintf("file:hints_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.KnowledgeArtifact{}, &model.Task{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	model.DB = db
	return func() { model.DB = prev }
}

// seedHintFixtures creates a task (optionally with a query embedding)
// plus three artifacts aligned with that embedding so relevance scoring
// has a clear winner per kind.
func seedHintFixtures(t *testing.T, taskEmb []float32) string {
	t.Helper()
	taskID := "task_hints_demo"
	task := &model.Task{
		ID: taskID, ProjectID: "p1", Name: "修复登录 401",
		Description: "线上登录一直 401,需要排查", Priority: "high", Status: "pending",
		CreatedBy: "agent_demo",
	}
	if len(taskEmb) > 0 {
		task.DescriptionEmbedding = MarshalEmbedding(taskEmb)
		task.DescriptionEmbeddingDim = len(taskEmb)
		now := time.Now()
		task.DescriptionEmbeddedAt = &now
	}
	if err := model.DB.Create(task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	unit := func(x, y, z float32) []float32 {
		n := float32(1.0) / float32(sqrt32(x*x+y*y+z*z))
		return []float32{x * n, y * n, z * n}
	}

	artifacts := []model.KnowledgeArtifact{
		{ID: "rec-auth", ProjectID: "p1", Kind: "tool_recipe",
			Name: "recipe: auth 排查", Summary: "middleware → token → exp",
			Status: "active", Confidence: 0.9, Version: 1,
			Embedding: MarshalEmbedding(unit(1, 0.05, 0)), EmbeddingDim: 3},
		{ID: "pat-handler", ProjectID: "p1", Kind: "pattern",
			Name: "pattern: REST 新增流", Summary: "declare → register → impl",
			Status: "active", Confidence: 0.8, Version: 1,
			Embedding: MarshalEmbedding(unit(0.9, 0.2, 0)), EmbeddingDim: 3},
		{ID: "anti-own", ProjectID: "p1", Kind: "anti_pattern",
			Name: "anti: 忽略 ownership 校验", Summary: "接口未做归属校验",
			Status: "active", Confidence: 0.85, Version: 1,
			Embedding: MarshalEmbedding(unit(0.8, 0.3, 0.1)), EmbeddingDim: 3},
	}
	for i := range artifacts {
		artifacts[i].CreatedAt = time.Now()
		artifacts[i].UpdatedAt = time.Now()
		if err := model.DB.Create(&artifacts[i]).Error; err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
	}
	return taskID
}

func TestHints_BundleIsStructuredByKind(t *testing.T) {
	defer setupHintsDB(t)()
	taskID := seedHintFixtures(t, []float32{1, 0.1, 0})

	hints, err := BuildTaskClaimHints(context.Background(), taskID)
	if err != nil {
		t.Fatalf("BuildTaskClaimHints: %v", err)
	}
	if len(hints.InjectedIDs) == 0 {
		t.Fatal("expected at least one injected artifact")
	}
	// All three kinds should be represented — our fixture has one of each.
	if len(hints.Recipes) != 1 || hints.Recipes[0].ID != "rec-auth" {
		t.Errorf("recipes: expected 1 rec-auth, got %+v", hints.Recipes)
	}
	if len(hints.Patterns) != 1 || hints.Patterns[0].ID != "pat-handler" {
		t.Errorf("patterns: expected 1 pat-handler, got %+v", hints.Patterns)
	}
	if len(hints.AntiPatterns) != 1 || hints.AntiPatterns[0].ID != "anti-own" {
		t.Errorf("anti_patterns: expected 1 anti-own, got %+v", hints.AntiPatterns)
	}
	if !hints.Meta.QueryHadEmbedding {
		t.Error("meta.query_had_embedding should be true when task has a cached embedding")
	}
	if hints.Meta.CandidatePool != 3 {
		t.Errorf("meta.candidate_pool = %d, want 3", hints.Meta.CandidatePool)
	}
	if hints.Meta.Selected != len(hints.InjectedIDs) {
		t.Errorf("meta.selected (%d) != len(InjectedIDs) (%d)",
			hints.Meta.Selected, len(hints.InjectedIDs))
	}
}

func TestHints_InjectedIDsReflectRankingOrder(t *testing.T) {
	defer setupHintsDB(t)()
	// Query aligned with rec-auth — should rank first.
	taskID := seedHintFixtures(t, []float32{1, 0.05, 0})

	hints, err := BuildTaskClaimHints(context.Background(), taskID)
	if err != nil {
		t.Fatalf("BuildTaskClaimHints: %v", err)
	}
	if len(hints.InjectedIDs) < 2 {
		t.Fatalf("need ≥ 2 items to check ordering, got %d", len(hints.InjectedIDs))
	}
	if hints.InjectedIDs[0] != "rec-auth" {
		t.Errorf("top injected id should be rec-auth, got %s (all: %v)",
			hints.InjectedIDs[0], hints.InjectedIDs)
	}
}

func TestHints_UnknownTaskReturnsError(t *testing.T) {
	defer setupHintsDB(t)()
	// No fixtures — empty DB.
	_, err := BuildTaskClaimHints(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown task id")
	}
}

func TestHints_TaskWithoutEmbeddingUsesLiveFallback(t *testing.T) {
	defer setupHintsDB(t)()

	// Stand up a stub embedder that pretends to be bge-zh — returns a
	// vector close to rec-auth's direction for any query, so the
	// selector should still pick rec-auth first.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "dim": 3, "device": "test", "batch_size": 32,
			})
			return
		}
		// Embed endpoint: return a vec aligned with rec-auth for any input.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vectors": [][]float32{{1, 0.05, 0}},
			"dim":     3,
			"device":  "test",
			"count":   1,
		})
	}))
	defer srv.Close()

	// Consume the sync.Once before overriding — otherwise the first call
	// to DefaultEmbeddingClient from inside the selector will re-init
	// defaultClient back to the real localhost:3011 sidecar.
	_ = DefaultEmbeddingClient()
	prev := defaultClient
	defer func() { defaultClient = prev }()
	defaultClient = NewEmbeddingClient(srv.URL)

	// Task seeded WITHOUT an embedding — nil → live fallback.
	taskID := seedHintFixtures(t, nil)

	hints, err := BuildTaskClaimHints(context.Background(), taskID)
	if err != nil {
		t.Fatalf("BuildTaskClaimHints: %v", err)
	}
	if hints.Meta.QueryHadEmbedding {
		t.Error("meta.query_had_embedding must be false for task w/o cached embedding")
	}
	if len(hints.InjectedIDs) == 0 {
		t.Fatal("live fallback should still produce results when sidecar is up")
	}
	if hints.InjectedIDs[0] != "rec-auth" {
		t.Errorf("live fallback + aligned stub → expected rec-auth first, got %s",
			hints.InjectedIDs[0])
	}
}

func TestHints_EmptyProjectReturnsEmptyBundle(t *testing.T) {
	defer setupHintsDB(t)()
	// A task exists but no artifacts — selector returns nil, so hints
	// should carry empty slices (not nil) so JSON serializers emit
	// [] rather than null.
	task := &model.Task{
		ID: "t-empty", ProjectID: "p-empty", Name: "new",
		Priority: "low", Status: "pending", CreatedBy: "a",
	}
	if err := model.DB.Create(task).Error; err != nil {
		t.Fatal(err)
	}

	hints, err := BuildTaskClaimHints(context.Background(), "t-empty")
	if err != nil {
		t.Fatalf("BuildTaskClaimHints: %v", err)
	}
	if hints.Recipes == nil || hints.Patterns == nil || hints.AntiPatterns == nil {
		t.Error("kind-grouped slices should be empty (not nil) for JSON stability")
	}
	if len(hints.InjectedIDs) != 0 {
		t.Errorf("expected no injected IDs on empty project, got %v", hints.InjectedIDs)
	}
	if hints.Meta.CandidatePool != 0 {
		t.Errorf("candidate pool should be 0, got %d", hints.Meta.CandidatePool)
	}
}
