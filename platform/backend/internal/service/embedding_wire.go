package service

// Wires the service.EmbeddingClient into refinery.Embedder so refinery
// passes automatically get semantic vectors on every artifact they write.
//
// This adapter lives in the service package because:
//   - It's where EmbeddingClient is defined.
//   - refinery must not depend on service (cyclic import risk).
//   - main.go can call InstallEmbedderIntoRefinery() once at startup.
//
// Called exactly once from the wiring layer. Safe to call more than once
// (the refinery package uses last-writer-wins), but there's no reason to.

import (
	"context"

	"github.com/a3c/platform/internal/service/refinery"
)

// refineryEmbedderAdapter bridges between the Embedder interface exposed
// by the refinery package and our EmbeddingClient's batch-oriented API.
// Artifacts are always embedded one-at-a-time (one call per upsert), so
// we degrade to the single-document code path and serialize the result.
type refineryEmbedderAdapter struct {
	client *EmbeddingClient
}

func (a *refineryEmbedderAdapter) EmbedDocument(ctx context.Context, text string) ([]byte, int, error) {
	vecs, err := a.client.EmbedDocuments(ctx, []string{text})
	if err != nil {
		return nil, 0, err
	}
	if len(vecs) != 1 {
		// Shouldn't happen for a single-text input — but guard so we
		// never silently return a misaligned vector.
		return nil, 0, nil
	}
	return MarshalEmbedding(vecs[0]), len(vecs[0]), nil
}

// InstallEmbedderIntoRefinery hooks the default embedding client into
// the refinery package. Call once from main.go, after services start.
// If `client` is nil, the default client (A3C_EMBEDDER_URL or localhost)
// is used.
func InstallEmbedderIntoRefinery(client *EmbeddingClient) {
	if client == nil {
		client = DefaultEmbeddingClient()
	}
	refinery.SetEmbedder(&refineryEmbedderAdapter{client: client})
}
