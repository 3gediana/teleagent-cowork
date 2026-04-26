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

// TestSelector_RecentClusterCooldown_PenalisesRepeatOffenders verifies
// the opt-in RecentTopClusters mechanism actually displaces a cluster
// from top-1 once it has dominated recent rounds. evobench surfaced
// the underlying bias: whichever cluster lucked into hosting the
// highest-importance artifacts at simulation start kept winning every
// subsequent round, because each successful inject reinforced its
// importance score and fed back into RRF. Cooldown breaks that loop.
func TestSelector_RecentClusterCooldown_PenalisesRepeatOffenders(t *testing.T) {
	defer setupArtifactCtxDB(t)()

	// Two singletons-by-cluster, identical semantic match. Without
	// cooldown the deterministic ID tie-break (artifact_a < artifact_b)
	// should put artifact_a on top. seedArtifactWithCluster calls
	// time.Now() per row, which leaves nanosecond drift between
	// CreatedAt/UpdatedAt — enough to give artifact_b a younger
	// recency rank and break the tie-break test below. Flatten the
	// timestamps explicitly so only the cooldown signal varies.
	seedArtifactWithCluster(t, "artifact_a", "p1", "pattern", "pat:a", makeUnitVec(1, 0, 0), "ep_A")
	seedArtifactWithCluster(t, "artifact_b", "p1", "pattern", "pat:b", makeUnitVec(1, 0, 0), "ep_B")
	pinned := time.Now()
	if err := model.DB.Model(&model.KnowledgeArtifact{}).
		Where("project_id = ?", "p1").
		Updates(map[string]any{"created_at": pinned, "updated_at": pinned}).Error; err != nil {
		t.Fatalf("flatten timestamps: %v", err)
	}

	// Sanity: empty RecentTopClusters reproduces the legacy behaviour
	// (pre-cooldown). artifact_a wins by ID tie-break.
	base := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAnalyzer,
		QueryEmbedding: makeUnitVec(1, 0, 0),
	})
	if len(base) < 2 || base[0].Artifact.ID != "artifact_a" {
		t.Fatalf("baseline (no cooldown) expected artifact_a top-1; got %v",
			injectedIDs(base))
	}

	// One occurrence of ep_A in the recent window. 0.85^1 ≈ 0.85
	// applied to artifact_a's RRF score; with both at sem rank 1
	// (i.e. genuinely tied on every signal), that single 15% haircut
	// is enough to drop artifact_a behind artifact_b.
	once := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAnalyzer,
		QueryEmbedding:    makeUnitVec(1, 0, 0),
		RecentTopClusters: []string{"ep_A"},
	})
	if len(once) < 2 || once[0].Artifact.ID != "artifact_b" {
		t.Errorf("with RecentTopClusters=[ep_A] expected artifact_b on top; got %v",
			injectedIDs(once))
	}

	// Three occurrences should hammer ep_A even harder
	// (0.85^3 ≈ 0.614). artifact_b stays on top.
	thrice := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAnalyzer,
		QueryEmbedding:    makeUnitVec(1, 0, 0),
		RecentTopClusters: []string{"ep_A", "ep_A", "ep_A"},
	})
	if len(thrice) < 2 || thrice[0].Artifact.ID != "artifact_b" {
		t.Errorf("with 3× ep_A in window expected artifact_b on top; got %v",
			injectedIDs(thrice))
	}

	// Penalising a cluster that nobody owns is a no-op — falls back
	// to the deterministic ID tie-break.
	noop := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAnalyzer,
		QueryEmbedding:    makeUnitVec(1, 0, 0),
		RecentTopClusters: []string{"ep_NEVER_EXISTED"},
	})
	if len(noop) < 2 || noop[0].Artifact.ID != "artifact_a" {
		t.Errorf("penalising a non-existent cluster should be a no-op; got %v",
			injectedIDs(noop))
	}
}

// TestCooldownDecayFactor verifies the magnitude-aware decay math
// in isolation. The integration with SelectArtifactsForInjection is
// covered by the *_GoldClusterSurvives test below; this one nails
// down the formula's edge cases so future tweaks can't drift the
// abstention semantics by accident.
func TestCooldownDecayFactor(t *testing.T) {
	cases := []struct {
		name         string
		myMax        float64
		runnerUp     float64
		n            int
		baseDecay    float64
		want         float64
		toleranceAbs float64
	}{
		{
			name: "tied scores apply full decay",
			// myMax == runnerUp → dominance=0 → effective=baseDecay
			myMax: 1.0, runnerUp: 1.0, n: 1, baseDecay: 0.85,
			want: 0.85, toleranceAbs: 1e-9,
		},
		{
			name: "tied scores compose across n=3",
			myMax: 1.0, runnerUp: 1.0, n: 3, baseDecay: 0.85,
			want: 0.614125, toleranceAbs: 1e-6,
		},
		{
			name: "half-dominance softens decay halfway",
			// (1.0 - 0.5)/1.0 = 0.5 → effective = 0.85 + 0.075 = 0.925
			myMax: 1.0, runnerUp: 0.5, n: 1, baseDecay: 0.85,
			want: 0.925, toleranceAbs: 1e-9,
		},
		{
			name: "extreme dominance nearly disables decay",
			// (1.0 - 0.01)/1.0 = 0.99 → effective ≈ 0.9985
			myMax: 1.0, runnerUp: 0.01, n: 1, baseDecay: 0.85,
			want: 0.9985, toleranceAbs: 1e-3,
		},
		{
			name: "zero occurrence is identity",
			myMax: 1.0, runnerUp: 0.5, n: 0, baseDecay: 0.85,
			want: 1.0, toleranceAbs: 1e-9,
		},
		{
			name: "zero myMax is identity",
			// Defensive: cluster has no positive-scored candidates.
			myMax: 0.0, runnerUp: 0.5, n: 1, baseDecay: 0.85,
			want: 1.0, toleranceAbs: 1e-9,
		},
		{
			name: "no alternative cluster abstains",
			// runnerUp == 0 means every cluster is recent. Per the
			// helper's documented contract we abstain → no penalty.
			myMax: 1.0, runnerUp: 0.0, n: 2, baseDecay: 0.85,
			want: 1.0, toleranceAbs: 1e-9,
		},
		{
			name: "negative dominance is clamped to zero",
			// Pathological: runnerUp > myMax (caller asked us about a
			// cluster that's already losing). Formula would yield a
			// negative dominance; clamp pins it to 0 → full baseDecay.
			myMax: 0.5, runnerUp: 1.0, n: 1, baseDecay: 0.85,
			want: 0.85, toleranceAbs: 1e-9,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cooldownDecayFactor(tc.myMax, tc.runnerUp, tc.n, tc.baseDecay)
			diff := got - tc.want
			if diff < 0 {
				diff = -diff
			}
			if diff > tc.toleranceAbs {
				t.Errorf("cooldownDecayFactor(%g, %g, %d, %g) = %g, want %g (±%g)",
					tc.myMax, tc.runnerUp, tc.n, tc.baseDecay,
					got, tc.want, tc.toleranceAbs)
			}
		})
	}
}

// TestSelector_RecentClusterCooldown_TrustExemptsKnownGold verifies
// the TrustedClusters opt-in exempts a cluster from cooldown decay.
// This is the operational replacement for magnitude-aware decay in
// the gold-cluster scenario: an external signal source (LLM judge
// agreement, ops review, etc.) tells us "ep_A is gold" and the
// selector honours that even when the cluster also appears in
// RecentTopClusters.
//
// Test scenario: identical-signal twins in two clusters, one round
// of cooldown for ep_A. Without trust, artifact_a (ep_A) would be
// haircut and artifact_b would surface on top by ID tie-break.
// With ep_A trusted, the cooldown is skipped and the deterministic
// ID tie-break (artifact_a < artifact_b) keeps artifact_a on top.
func TestSelector_RecentClusterCooldown_TrustExemptsKnownGold(t *testing.T) {
	defer setupArtifactCtxDB(t)()

	seedArtifactWithCluster(t, "artifact_a", "p1", "pattern", "pat:a", makeUnitVec(1, 0, 0), "ep_A")
	seedArtifactWithCluster(t, "artifact_b", "p1", "pattern", "pat:b", makeUnitVec(1, 0, 0), "ep_B")
	pinned := time.Now()
	if err := model.DB.Model(&model.KnowledgeArtifact{}).
		Where("project_id = ?", "p1").
		Updates(map[string]any{"created_at": pinned, "updated_at": pinned}).Error; err != nil {
		t.Fatalf("flatten timestamps: %v", err)
	}

	// Reference call: cooldown active, no trust → artifact_a haircut,
	// artifact_b wins. (Same assertion as
	// TestSelector_RecentClusterCooldown_PenalisesRepeatOffenders'
	// "once" branch — included here so the trust assertion below has
	// a contrast to point at, not to be the sole source of truth.)
	without := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAnalyzer,
		QueryEmbedding:    makeUnitVec(1, 0, 0),
		RecentTopClusters: []string{"ep_A"},
	})
	if len(without) < 2 || without[0].Artifact.ID != "artifact_b" {
		t.Fatalf("setup sanity: cooldown without trust should haircut ep_A; got %v",
			injectedIDs(without))
	}

	// The actual assertion: ep_A is in the trust set, so cooldown
	// must skip it. RRF scores tie, ID tie-break puts artifact_a
	// back on top.
	withTrust := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAnalyzer,
		QueryEmbedding:    makeUnitVec(1, 0, 0),
		RecentTopClusters: []string{"ep_A"},
		TrustedClusters:   []string{"ep_A"},
	})
	if len(withTrust) < 2 || withTrust[0].Artifact.ID != "artifact_a" {
		t.Errorf("trust exemption failed; expected artifact_a top-1 (cooldown skipped), got %v",
			injectedIDs(withTrust))
	}

	// Defensive: trusting an unrelated cluster doesn't accidentally
	// exempt artifact_a. ep_A is still penalised, artifact_b wins.
	withWrongTrust := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAnalyzer,
		QueryEmbedding:    makeUnitVec(1, 0, 0),
		RecentTopClusters: []string{"ep_A"},
		TrustedClusters:   []string{"ep_DIFFERENT"},
	})
	if len(withWrongTrust) < 2 || withWrongTrust[0].Artifact.ID != "artifact_b" {
		t.Errorf("trusting unrelated cluster shouldn't exempt ep_A; got %v",
			injectedIDs(withWrongTrust))
	}
}

// Why there's no end-to-end "gold cluster survives cooldown" test
// here despite the helper's nominal magnitude-awareness: in
// practice, RRF dense-rank fusion compresses the score gap between
// the best and second-best cluster to about 1.6 % at worst-case
// (k=60, four signals, gold rank 1 on every signal vs runner-up
// rank 2 on every signal: 4/61 vs 4/62 ≈ 1.63 %). The cooldown's
// multiplicative penalty is 15 % per occurrence at base 0.85, so
// any decay strong enough to break a self-reinforced monopoly is
// also strong enough to flip a gold cluster of typical RRF margin.
//
// The cooldownDecayFactor helper STILL applies: at extreme
// dominance ratios (e.g. RRF gap ≥ 30 % — when the runner-up is
// many ranks behind on every signal) it reduces decay toward 1.0.
// We just don't yet have a reliable way to construct that scenario
// in a small test. evobench seeds dozens of artifacts and would
// occasionally hit the regime; unit-test scale doesn't.
//
// Backlog: a proper "gold-cluster preservation" mechanism needs a
// cluster-trust signal independent of RRF (e.g., judge-agreement
// persistence, hand-curated overrides, or a smoothed
// success-count-per-cluster score). Tracked in the section
// comment above section 5b in artifact_context.go.

// TestSelector_RecentClusterCooldown_LeavesSingletonsAlone verifies
// that artifacts with no parseable cluster (legacy rows pre-dating
// SourceEvents tracking) are NOT penalised even when an empty-string
// cluster ID happens to be in RecentTopClusters. The cooldown is a
// targeted fix for cluster-monopoly dynamics; treating singletons as
// part of any "cluster" would unfairly demote half the historical pool.
func TestSelector_RecentClusterCooldown_LeavesSingletonsAlone(t *testing.T) {
	defer setupArtifactCtxDB(t)()

	// Two artifacts with no SourceEvents set — both have clusterKey="".
	// One has marginally higher confidence so the candidate fetch
	// returns a stable order before scoring.
	seedArtifact(t, "p1_lone", "p1", "pattern", "pat:lone", makeUnitVec(1, 0, 0))
	seedArtifact(t, "p2_lone", "p1", "pattern", "pat:lone2", makeUnitVec(1, 0, 0))
	// Flatten timestamps so the test isn't flaky on the recency
	// signal: seedArtifact stamps each row with time.Now() and the
	// monotonic drift between two consecutive calls is enough to
	// give p2_lone a fresher rank, which then wins RRF and breaks
	// the ID tie-break assertion below.
	pinned := time.Now()
	if err := model.DB.Model(&model.KnowledgeArtifact{}).
		Where("project_id = ?", "p1").
		Updates(map[string]any{"created_at": pinned, "updated_at": pinned}).Error; err != nil {
		t.Fatalf("flatten timestamps: %v", err)
	}

	// Even with empty-string in the cooldown window, neither artifact
	// should see its score multiplied — the cooldown skips empty
	// cluster keys entirely (see counts map population).
	r := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: "p1", Audience: AudienceAnalyzer,
		QueryEmbedding:    makeUnitVec(1, 0, 0),
		RecentTopClusters: []string{""},
	})
	if len(r) != 2 {
		t.Fatalf("expected both legacy artifacts to survive, got %d", len(r))
	}
	// Order must be deterministic via ID tie-break since RRF scores tie.
	if r[0].Artifact.ID != "p1_lone" {
		t.Errorf("expected ID tie-break to put p1_lone first; got %v",
			injectedIDs(r))
	}
}

// injectedIDs is a small helper for failure messages — turns a result
// slice into a flat ID list so log lines stay readable.
func injectedIDs(result []InjectedArtifact) []string {
	out := make([]string, len(result))
	for i, ia := range result {
		out[i] = ia.Artifact.ID
	}
	return out
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
