package service

// Integration tests for SelectArtifactsForInjection.
//
// Uses an in-memory SQLite DB (same pattern as refinery/integration_test.go)
// so we get the full GORM query behaviour without network or MySQL.
// Embeddings are hand-crafted so the test can assert precise ranking.

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a3c/platform/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var artifactCtxDBCounter int64

func setupArtifactCtxDB(t *testing.T) func() {
	t.Helper()
	prev := model.DB
	n := atomic.AddInt64(&artifactCtxDBCounter, 1)
	dsn := fmt.Sprintf("file:artctx_%d?mode=memory&cache=shared", n)
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

// hand-crafted: point vectors on the unit circle so cosine similarity
// is trivially predictable. Each returned vec is length 3.
func makeUnitVec(x, y, z float32) []float32 {
	norm := float32(0)
	for _, v := range []float32{x, y, z} {
		norm += v * v
	}
	if norm == 0 {
		return []float32{0, 0, 0}
	}
	n := float32(1) / float32(sqrt32(norm))
	return []float32{x * n, y * n, z * n}
}

func sqrt32(x float32) float32 {
	// crude but sufficient for test vectors
	guess := x / 2
	for i := 0; i < 12; i++ {
		guess = 0.5 * (guess + x/guess)
	}
	return guess
}

func seedArtifact(t *testing.T, id, projectID, kind, name string, vec []float32) {
	t.Helper()
	ka := &model.KnowledgeArtifact{
		ID:           id,
		ProjectID:    projectID,
		Kind:         kind,
		Name:         name,
		Summary:      name + " summary",
		Status:       "active",
		Confidence:   0.8,
		Version:      1,
		Embedding:    MarshalEmbedding(vec),
		EmbeddingDim: len(vec),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := model.DB.Create(ka).Error; err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
}

func TestSelector_SemanticRecallOutranksConfidence(t *testing.T) {
	defer setupArtifactCtxDB(t)()

	// Three artifacts, all same kind, all active. A is semantically close
	// to the query; B and C are orthogonal. Under RRF, semantic is one of
	// four equally-weighted signals, so this test isolates semantic as
	// the only varying signal by flattening tag / importance / recency
	// across the three artifacts. A wins because it's rank 1 on semantic
	// while everything else ties at rank 1.
	seedArtifact(t, "a", "p1", "pattern", "pat:login",
		makeUnitVec(1, 0, 0)) // near the query
	seedArtifact(t, "b", "p1", "pattern", "pat:billing",
		makeUnitVec(0, 1, 0)) // orthogonal
	seedArtifact(t, "c", "p1", "pattern", "pat:analytics",
		makeUnitVec(0, 0, 1)) // orthogonal

	// Flatten updated_at across all three so recency doesn't prefer the
	// last-seeded artifact. We use UpdateColumn to bypass GORM's
	// auto-update-of-updated_at hook — otherwise the Update itself would
	// re-dirty the timestamps and defeat the purpose.
	fixed := time.Now()
	model.DB.Model(&model.KnowledgeArtifact{}).
		Where("id IN ?", []string{"a", "b", "c"}).
		UpdateColumn("updated_at", fixed)

	// Query vector aligned with A.
	queryVec := makeUnitVec(1, 0.05, 0)

	result := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID:      "p1",
		Audience:       AudienceCommander,
		QueryEmbedding: queryVec,
	})
	if len(result) == 0 {
		t.Fatal("expected at least one result")
	}
	if result[0].Artifact.ID != "a" {
		t.Errorf("semantic match should rank first; got %s (scores: %+v)",
			result[0].Artifact.ID, summariseScores(result))
	}
}

func TestSelector_AudienceFiltersKinds(t *testing.T) {
	defer setupArtifactCtxDB(t)()

	seedArtifact(t, "p1-pat", "p1", "pattern", "pat:x", makeUnitVec(1, 0, 0))
	seedArtifact(t, "p1-anti", "p1", "anti_pattern", "anti:y", makeUnitVec(1, 0, 0))
	seedArtifact(t, "p1-rec", "p1", "tool_recipe", "rec:z", makeUnitVec(1, 0, 0))
	// An operator-metadata kind that must never leak into prompts.
	seedArtifact(t, "p1-rep", "p1", "pass_report", "meta", makeUnitVec(1, 0, 0))

	query := makeUnitVec(1, 0, 0)

	// Auditor only sees anti-patterns.
	r := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAuditor, QueryEmbedding: query,
	})
	for _, ia := range r {
		if ia.Artifact.Kind != "anti_pattern" {
			t.Errorf("auditor audience should only get anti_pattern, got %s", ia.Artifact.Kind)
		}
	}
	if len(r) != 1 {
		t.Errorf("auditor expected 1 result, got %d", len(r))
	}

	// Commander sees the three agent kinds but never pass_report.
	r = SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceCommander, QueryEmbedding: query,
	})
	for _, ia := range r {
		if ia.Artifact.Kind == "pass_report" {
			t.Error("pass_report must never be injected into commander prompts")
		}
	}
	if len(r) != 3 {
		t.Errorf("commander expected 3 results, got %d", len(r))
	}
}

func TestSelector_GlobalArtifactsIncluded(t *testing.T) {
	defer setupArtifactCtxDB(t)()

	seedArtifact(t, "project-a", "p1", "pattern", "pat:project", makeUnitVec(1, 0, 0))
	// Global artifact lives on project_id="" and must be visible to any project.
	seedArtifact(t, "global-a", "", "pattern", "pat:global", makeUnitVec(0.9, 0.1, 0))

	r := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceCommander,
		QueryEmbedding: makeUnitVec(1, 0, 0),
	})
	ids := map[string]bool{}
	for _, ia := range r {
		ids[ia.Artifact.ID] = true
	}
	if !ids["global-a"] {
		t.Error("global artifact should be visible to any project")
	}
	if !ids["project-a"] {
		t.Error("project-scoped artifact should be present")
	}
}

func TestSelector_RespectsPerKindBudget(t *testing.T) {
	defer setupArtifactCtxDB(t)()

	// 5 patterns, 5 anti-patterns, all perfectly matching.
	for i := 0; i < 5; i++ {
		seedArtifact(t, fmt.Sprintf("pat%d", i), "p1", "pattern",
			fmt.Sprintf("pat#%d", i), makeUnitVec(1, 0, 0))
	}
	for i := 0; i < 5; i++ {
		seedArtifact(t, fmt.Sprintf("anti%d", i), "p1", "anti_pattern",
			fmt.Sprintf("anti#%d", i), makeUnitVec(1, 0, 0))
	}

	// Commander budget is 2 patterns, 3 anti_patterns, total 7.
	r := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceCommander,
		QueryEmbedding: makeUnitVec(1, 0, 0),
	})

	perKind := map[string]int{}
	for _, ia := range r {
		perKind[ia.Artifact.Kind]++
	}
	if perKind["pattern"] > 2 {
		t.Errorf("pattern cap = 2, got %d", perKind["pattern"])
	}
	if perKind["anti_pattern"] > 3 {
		t.Errorf("anti_pattern cap = 3, got %d", perKind["anti_pattern"])
	}
}

func TestSelector_NoQueryEmbedding_GracefulDegradation(t *testing.T) {
	defer setupArtifactCtxDB(t)()

	// Two artifacts — one with higher importance (more successes), one
	// with recent update. No query vector provided.
	seedArtifact(t, "hi-import", "p1", "pattern", "pat:old", makeUnitVec(1, 0, 0))
	seedArtifact(t, "fresh", "p1", "pattern", "pat:fresh", makeUnitVec(0, 1, 0))

	// High-importance wins on that signal.
	model.DB.Model(&model.KnowledgeArtifact{}).Where("id = ?", "hi-import").
		Updates(map[string]any{"success_count": 20, "usage_count": 22})

	// No QueryEmbedding, no QueryText — selector must still return
	// something sensible (ranked by importance + recency).
	r := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceCommander,
	})
	if len(r) == 0 {
		t.Fatal("selector should degrade gracefully, not return nil")
	}
	// Both artifacts should be returned; the high-importance one ranked
	// first because semantic=0 for everyone.
	if r[0].Artifact.ID != "hi-import" {
		t.Errorf("without semantic signal, importance should win; got %s first",
			r[0].Artifact.ID)
	}
}

func TestSelector_DeprecatedArtifactsExcluded(t *testing.T) {
	defer setupArtifactCtxDB(t)()
	seedArtifact(t, "active", "p1", "pattern", "pat:live", makeUnitVec(1, 0, 0))
	seedArtifact(t, "dep", "p1", "pattern", "pat:dead", makeUnitVec(1, 0, 0))
	model.DB.Model(&model.KnowledgeArtifact{}).Where("id = ?", "dep").Update("status", "deprecated")

	r := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceCommander,
		QueryEmbedding: makeUnitVec(1, 0, 0),
	})
	for _, ia := range r {
		if ia.Artifact.ID == "dep" {
			t.Error("deprecated artifact must not appear in injection results")
		}
	}
}

func TestSelector_ReasonReflectsContributingSignals(t *testing.T) {
	defer setupArtifactCtxDB(t)()
	seedArtifact(t, "a", "p1", "pattern", "pat:a", makeUnitVec(1, 0, 0))

	r := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceCommander,
		QueryEmbedding: makeUnitVec(1, 0, 0),
	})
	if len(r) == 0 {
		t.Fatal("expected a result")
	}
	// Semantic AND recency (fresh artifact) should both appear; tag and
	// importance should not (no tags, zero usage).
	if !contains(r[0].Reason, "semantic=") {
		t.Errorf("reason should mention semantic: %q", r[0].Reason)
	}
	if !contains(r[0].Reason, "recency=") {
		t.Errorf("reason should mention recency: %q", r[0].Reason)
	}
	if contains(r[0].Reason, "tag=") {
		t.Errorf("no tags present, reason should not mention tag: %q", r[0].Reason)
	}
}

// TestSelector_SessionDiversity_CapsSameClusterArtifacts verifies the
// Phase 2 session-diversity guard: at most maxArtifactsPerSourceCluster
// (2) artifacts from the same root cluster survive into top-K even
// when many same-cluster siblings match the query equally well.
//
// Without the guard, a single "productive" session could monopolise
// the prompt — the selector would inject 4-5 near-duplicate lessons
// derived from the same trajectory, crowding out genuinely different
// knowledge from other sessions.
func TestSelector_SessionDiversity_CapsSameClusterArtifacts(t *testing.T) {
	defer setupArtifactCtxDB(t)()

	// Four artifacts, all perfect semantic match. First three share
	// source_events[0] = "ep_A" (same cluster); the fourth has a
	// different cluster. Expected: ep_A yields at most 2 survivors;
	// ep_B is unaffected. Use Analyzer audience so the per-kind
	// budget (10) doesn't clip before we can observe diversity.
	for _, id := range []string{"a1", "a2", "a3"} {
		seedArtifactWithCluster(t, id, "p1", "pattern", "pat:"+id, makeUnitVec(1, 0, 0), "ep_A")
	}
	seedArtifactWithCluster(t, "b1", "p1", "pattern", "pat:b1", makeUnitVec(1, 0, 0), "ep_B")

	r := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAnalyzer,
		QueryEmbedding: makeUnitVec(1, 0, 0),
	})

	clusterCount := map[string]int{}
	for _, ia := range r {
		clusterCount[clusterKey(ia.Artifact)]++
	}
	if clusterCount["ep_A"] > 2 {
		t.Errorf("ep_A cluster cap = 2, got %d", clusterCount["ep_A"])
	}
	if clusterCount["ep_B"] != 1 {
		t.Errorf("ep_B (singleton cluster) should survive, got %d", clusterCount["ep_B"])
	}
}

// TestSelector_EmptySourceEventsTreatedAsSingleton verifies legacy
// artifacts with no SourceEvents field don't get unfairly penalised
// by the diversity guard. They all have empty clusterKey and are
// treated as singletons — every one of them survives.
func TestSelector_EmptySourceEventsTreatedAsSingleton(t *testing.T) {
	defer setupArtifactCtxDB(t)()

	// Five artifacts, no SourceEvents set on any. All should survive
	// the diversity filter (they're "different" singletons by default)
	// even if the per-kind budget clips them afterwards.
	for i := 0; i < 5; i++ {
		seedArtifact(t, fmt.Sprintf("p%d", i), "p1", "pattern",
			fmt.Sprintf("pat#%d", i), makeUnitVec(1, 0, 0))
	}

	// Inspect the pre-budget scored list by driving the selector for
	// a budget-free audience (Analyzer has MaxTotal=30 and 10/kind, so
	// none of our 5 get clipped).
	r := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAnalyzer,
		QueryEmbedding: makeUnitVec(1, 0, 0),
	})
	if len(r) != 5 {
		t.Errorf("legacy artifacts (no SourceEvents) must all survive diversity; got %d, want 5", len(r))
	}
}

// TestDenseRank covers the ranking primitive in isolation. Important
// edge cases: ties share rank, all-zero input becomes all-tied-at-1
// (critical for RRF graceful degradation), empty input returns nil.
func TestDenseRank(t *testing.T) {
	cases := []struct {
		name   string
		values []float64
		want   []int
	}{
		{"descending", []float64{0.9, 0.5, 0.1}, []int{1, 2, 3}},
		{"ties middle", []float64{0.9, 0.5, 0.5, 0.1}, []int{1, 2, 2, 3}},
		{"all equal", []float64{0.5, 0.5, 0.5}, []int{1, 1, 1}},
		{"all zero (RRF degradation)", []float64{0, 0, 0}, []int{1, 1, 1}},
		{"ascending input", []float64{0.1, 0.5, 0.9}, []int{3, 2, 1}},
		{"single element", []float64{0.42}, []int{1}},
		{"empty", []float64{}, nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := denseRank(tc.values)
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %d want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("rank[%d]: got %d want %d (full: %v want %v)",
						i, got[i], tc.want[i], got, tc.want)
				}
			}
		})
	}
}

// -- helpers --------------------------------------------------------------

// seedArtifactWithCluster is like seedArtifact but also sets the
// SourceEvents field so the diversity guard can detect shared
// clusters. `clusterID` becomes the first element of the JSON array.
func seedArtifactWithCluster(t *testing.T, id, projectID, kind, name string, vec []float32, clusterID string) {
	t.Helper()
	sourceEvents := `["` + clusterID + `","` + id + `_evt"]`
	ka := &model.KnowledgeArtifact{
		ID:           id,
		ProjectID:    projectID,
		Kind:         kind,
		Name:         name,
		Summary:      name + " summary",
		Status:       "active",
		Confidence:   0.8,
		Version:      1,
		Embedding:    MarshalEmbedding(vec),
		EmbeddingDim: len(vec),
		SourceEvents: sourceEvents,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := model.DB.Create(ka).Error; err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
}

func summariseScores(r []InjectedArtifact) []string {
	out := make([]string, len(r))
	for i, ia := range r {
		out[i] = fmt.Sprintf("%s:%.3f", ia.Artifact.ID, ia.Score)
	}
	return out
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
