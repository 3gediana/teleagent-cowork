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
	"reflect"
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
	// Reset the process-global cluster-cooldown state up front: previous
	// tests in this binary may have left it populated, and these tests
	// assert top-1 ordering that the cooldown can perturb.
	resetRecentClusters()
	return func() {
		model.DB = prev
		resetRecentClusters()
	}
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
	// Pin every artifact's timestamps to the same instant. Looping with
	// fresh time.Now() calls — the previous version — produced
	// nanosecond differences that made the recency rank non-flat across
	// the three rows. Once recency contributed any variance, RRF could
	// tie two artifacts whose semantic ranks differed by one (sem-r1 +
	// rec-r2 == sem-r2 + rec-r1), defeating the test's intent of
	// isolating the semantic signal.
	now := time.Now()
	for i := range artifacts {
		artifacts[i].CreatedAt = now
		artifacts[i].UpdatedAt = now
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

// TestHints_ClusterCooldownFlipsTop1OnSecondCall is the end-to-end
// proof that the BuildTaskClaimHints path actually wires the
// cluster-cooldown tracker. Two artifacts with identical signals but
// different clusters: the first claim picks artifact_a by ID
// tie-break, recording cluster ep_A into the window. The second claim
// reads the window back via recentClustersFor, hands ep_A to the
// selector via RecentTopClusters, and the 0.85 multiplier on
// artifact_a's RRF score is enough to flip artifact_b on top.
func TestHints_ClusterCooldownFlipsTop1OnSecondCall(t *testing.T) {
	defer setupHintsDB(t)()

	// Seed a task with a query embedding aligned with both artifacts.
	taskID := "task_cooldown_demo"
	task := &model.Task{
		ID: taskID, ProjectID: "p_cd", Name: "shared title",
		Description: "shared description", Priority: "high", Status: "pending",
		CreatedBy: "agent_demo",
	}
	task.DescriptionEmbedding = MarshalEmbedding([]float32{1, 0, 0})
	task.DescriptionEmbeddingDim = 3
	now := time.Now()
	task.DescriptionEmbeddedAt = &now
	if err := model.DB.Create(task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}

	// Two artifacts identical on every signal except clusterKey.
	// `tool_recipe` is in AudienceCoder's kind list, so both will
	// survive the audience filter that BuildTaskClaimHints uses.
	makeArtifact := func(id, cluster string) *model.KnowledgeArtifact {
		return &model.KnowledgeArtifact{
			ID: id, ProjectID: "p_cd", Kind: "tool_recipe",
			Name: "rec:" + id, Summary: "shared summary",
			Status: "active", Confidence: 0.8, Version: 1,
			Embedding:    MarshalEmbedding([]float32{1, 0, 0}),
			EmbeddingDim: 3,
			SourceEvents: `["` + cluster + `","` + id + `_evt"]`,
			CreatedAt:    now, UpdatedAt: now,
		}
	}
	if err := model.DB.Create(makeArtifact("artifact_a", "ep_A")).Error; err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := model.DB.Create(makeArtifact("artifact_b", "ep_B")).Error; err != nil {
		t.Fatalf("seed b: %v", err)
	}

	// First claim: cooldown window is empty, so the deterministic ID
	// tie-break (artifact_a < artifact_b) puts artifact_a on top. The
	// hint builder records ep_A into the per-project window as a
	// side effect of returning the result.
	first, err := BuildTaskClaimHints(context.Background(), taskID)
	if err != nil {
		t.Fatalf("first BuildTaskClaimHints: %v", err)
	}
	if len(first.InjectedIDs) < 2 || first.InjectedIDs[0] != "artifact_a" {
		t.Fatalf("first call expected artifact_a top-1, got %v", first.InjectedIDs)
	}

	// Sanity-check the side effect actually fired — otherwise the
	// next assertion could pass trivially in a future regression.
	if got := recentClustersFor("p_cd"); len(got) != 1 || got[0] != "ep_A" {
		t.Fatalf("after first call expected window=[ep_A]; got %v", got)
	}

	// Second claim: cooldown applies 0.85 to artifact_a's RRF score.
	// With both rows otherwise tied, this is enough to drop it behind
	// artifact_b.
	second, err := BuildTaskClaimHints(context.Background(), taskID)
	if err != nil {
		t.Fatalf("second BuildTaskClaimHints: %v", err)
	}
	if len(second.InjectedIDs) < 2 || second.InjectedIDs[0] != "artifact_b" {
		t.Errorf("second call expected artifact_b top-1 (cooldown should flip); got %v",
			second.InjectedIDs)
	}

	// Window should now hold both clusters in chronological order.
	if got, want := recentClustersFor("p_cd"), []string{"ep_A", "ep_B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("window after two calls: got %v want %v", got, want)
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
