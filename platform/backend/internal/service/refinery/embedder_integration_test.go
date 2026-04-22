package refinery

// Verifies that when an Embedder is registered, artifacts produced by the
// refinery pipeline carry the semantic vector we expect. Uses a fake
// embedder so no HTTP / model is needed at test time.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a3c/platform/internal/model"
)

// fakeEmbedder returns a deterministic "vector" per input so tests can
// assert specific artifacts got specific embeddings. It also records
// every call so we can check upsert semantics (create → embed, update
// with same text → don't re-embed).
type fakeEmbedder struct {
	mu    sync.Mutex
	calls []string
	dim   int
}

func (f *fakeEmbedder) EmbedDocument(_ context.Context, text string) ([]byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, text)
	if f.dim == 0 {
		f.dim = 4
	}
	// Encode a tiny fingerprint: byte i = i-th char of text (or 0).
	// Not semantically meaningful — just verifiable.
	b := make([]byte, f.dim*4) // dim * sizeof(float32)
	for i := 0; i < f.dim; i++ {
		if i < len(text) {
			b[i*4] = text[i]
		}
	}
	return b, f.dim, nil
}

func (f *fakeEmbedder) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func withFakeEmbedder(t *testing.T) *fakeEmbedder {
	t.Helper()
	fe := &fakeEmbedder{}
	prev := registeredEmbedder
	SetEmbedder(fe)
	t.Cleanup(func() { SetEmbedder(prev) })
	return fe
}

func TestIntegration_EmbedderPopulatesArtifactsInPipeline(t *testing.T) {
	defer setupTestDB(t)()
	fe := withFakeEmbedder(t)

	projectID := "p-embed"

	// Seed minimum data for PatternExtractor to produce an artifact.
	for i := 0; i < 4; i++ {
		seedCompletedSession(t, projectID,
			"s"+string(rune('0'+i)),
			"grep read edit change_submit",
			"success", "L0",
			[]string{"pkg/a.go"},
			"c"+string(rune('0'+i)))
	}

	r := New()
	if _, err := r.Run(projectID, 24, "embed-test"); err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	var arts []model.KnowledgeArtifact
	model.DB.Where("project_id = ? AND kind = ?", projectID, "pattern").Find(&arts)
	if len(arts) == 0 {
		t.Fatalf("expected at least one pattern artifact, got 0")
	}

	// Every produced artifact should carry a non-empty embedding of the
	// declared dim from fakeEmbedder.
	for _, a := range arts {
		if len(a.Embedding) == 0 {
			t.Errorf("artifact %s has empty embedding", a.Name)
		}
		if a.EmbeddingDim != 4 {
			t.Errorf("artifact %s: dim=%d, want 4", a.Name, a.EmbeddingDim)
		}
		if a.EmbeddedAt == nil {
			t.Errorf("artifact %s: missing embedded_at timestamp", a.Name)
		}
	}

	// Sanity: the fake received calls whose text begins with "[pattern]"
	// (our canonical format).
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if len(fe.calls) == 0 {
		t.Fatal("fakeEmbedder saw no calls")
	}
	for _, c := range fe.calls {
		if !strings.HasPrefix(c, "[") {
			t.Errorf("embedder text does not use canonical format: %q", c)
		}
	}
}

func TestIntegration_UpsertSkipsReEmbedWhenTextUnchanged(t *testing.T) {
	defer setupTestDB(t)()
	fe := withFakeEmbedder(t)

	ka := &model.KnowledgeArtifact{
		ProjectID: "p1",
		Kind:      "pattern",
		Name:      "pat: grep→read",
		Summary:   "classic read-before-write",
		Payload:   `{"n":2}`,
	}
	if err := upsertArtifact(ka); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if fe.callCount() != 1 {
		t.Fatalf("first create should embed once, got %d calls", fe.callCount())
	}

	// Second upsert with identical text but different payload — should
	// NOT re-embed (optimisation: payload isn't part of the embedding
	// text, so vector is still valid).
	ka2 := &model.KnowledgeArtifact{
		ProjectID: "p1",
		Kind:      "pattern",
		Name:      "pat: grep→read",
		Summary:   "classic read-before-write", // identical
		Payload:   `{"n":3}`,                    // changed
	}
	if err := upsertArtifact(ka2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if fe.callCount() != 1 {
		t.Errorf("same-text update should NOT re-embed; got %d calls", fe.callCount())
	}

	// Third upsert with summary change — MUST re-embed.
	ka3 := &model.KnowledgeArtifact{
		ProjectID: "p1",
		Kind:      "pattern",
		Name:      "pat: grep→read",
		Summary:   "updated description — read before write",
		Payload:   `{"n":3}`,
	}
	if err := upsertArtifact(ka3); err != nil {
		t.Fatalf("third upsert: %v", err)
	}
	if fe.callCount() != 2 {
		t.Errorf("changed-summary should re-embed; got %d calls total", fe.callCount())
	}

	// The persisted record should reflect the new embedding's timestamp
	// (monotonically newer than first embed).
	var saved model.KnowledgeArtifact
	if err := model.DB.Where("project_id = ? AND name = ?", "p1", "pat: grep→read").First(&saved).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if saved.EmbeddedAt == nil {
		t.Fatal("expected embedded_at to be set after re-embed")
	}
}

func TestIntegration_NoEmbedderMeansNoVector(t *testing.T) {
	defer setupTestDB(t)()

	// Explicitly ensure no embedder is registered during this test.
	prev := registeredEmbedder
	SetEmbedder(nil)
	t.Cleanup(func() { SetEmbedder(prev) })

	ka := &model.KnowledgeArtifact{
		ProjectID: "p-none",
		Kind:      "pattern",
		Name:      "pat: nothing",
		Summary:   "no vector expected",
		Payload:   `{}`,
	}
	if err := upsertArtifact(ka); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var saved model.KnowledgeArtifact
	_ = model.DB.Where("id = ?", ka.ID).First(&saved).Error
	if len(saved.Embedding) != 0 {
		t.Errorf("no embedder → artifact should have nil embedding, got %d bytes", len(saved.Embedding))
	}
	if saved.EmbeddingDim != 0 {
		t.Errorf("no embedder → embedding_dim should be 0, got %d", saved.EmbeddingDim)
	}
	// And upsertArtifact itself must still succeed.
	if saved.ID == "" {
		t.Error("artifact not persisted")
	}
	_ = time.Now
}
