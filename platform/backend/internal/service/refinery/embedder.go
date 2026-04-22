package refinery

// Semantic embedding hook for generated artifacts.
//
// We want PatternExtractor / AntiPatternDetector / ToolRecipeMiner /
// GlobalPromoter to attach a sentence embedding to every new or updated
// KnowledgeArtifact so the injection selector can do kNN retrieval later.
//
// BUT — the `refinery` package sits below `service` in the import graph
// (service imports refinery, never the reverse). We therefore define a
// tiny interface here and let the calling layer inject a concrete
// implementation at startup. Benefits:
//
//   - refinery tests don't need the bge sidecar. A nil embedder is a
//     valid state and artifact creation simply skips the embedding step.
//   - If the sidecar is down, refinery keeps running; it logs and moves
//     on. Artifacts created during the outage can be back-filled later
//     (future reconciler job).
//   - Swapping embedders later (different model, remote service, etc.)
//     is one registration change in main.go.

import (
	"context"
	"log"
	"time"
)

// Embedder produces a serialized sentence embedding for a piece of text.
// Implementations are expected to return a raw byte blob suitable for
// storage in `KnowledgeArtifact.Embedding` (little-endian float32s by
// convention — see service.MarshalEmbedding).
type Embedder interface {
	EmbedDocument(ctx context.Context, text string) (blob []byte, dim int, err error)
}

// Package-level registered embedder. Nil means "embedding disabled" —
// refinery still works, artifacts just ship without semantic vectors.
var registeredEmbedder Embedder

// SetEmbedder installs the process-wide embedder implementation. Call
// once at startup from the wiring layer (main.go / service package).
// Passing nil disables embedding (useful for tests).
func SetEmbedder(e Embedder) {
	registeredEmbedder = e
}

// artifactEmbeddingText is the canonical text we feed into the embedder
// for a given artifact. Kept here so every producer sees the same
// formatting (matters because queries and documents must be encoded by
// the same recipe to compare well).
//
// We prepend the kind and name so the semantic space has structure even
// for very short summaries; the summary gets most of the weight because
// it carries the actual meaning.
func artifactEmbeddingText(kind, name, summary string) string {
	switch {
	case summary != "" && name != "":
		return "[" + kind + "] " + name + "\n" + summary
	case name != "":
		return "[" + kind + "] " + name
	default:
		return summary
	}
}

// embedArtifactBestEffort tries to populate the Embedding/EmbeddingDim/
// EmbeddedAt fields on a freshly built artifact. It is deliberately
// swallow-errors: embedding is a nice-to-have enhancement, not a
// correctness-critical step. If the sidecar is unreachable we log and
// let the artifact save without a vector — reconcilers will back-fill.
//
// Timeout is capped at 5s per artifact so a slow/stalled sidecar can't
// bring a refinery run to its knees (worst case: whole run misses
// embeddings but otherwise completes).
func embedArtifactBestEffort(kind, name, summary string) (blob []byte, dim int, embeddedAt *time.Time) {
	if registeredEmbedder == nil {
		return nil, 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	b, d, err := registeredEmbedder.EmbedDocument(ctx, artifactEmbeddingText(kind, name, summary))
	if err != nil {
		log.Printf("[Refinery] embed artifact %s/%s failed (continuing without vector): %v", kind, name, err)
		return nil, 0, nil
	}
	now := time.Now()
	return b, d, &now
}
