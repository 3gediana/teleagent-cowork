package service

// Embedding client
// ================
//
// Thin HTTP wrapper over the Python bge-base-zh-v1.5 sidecar at
// platform/embedder/app.py. Two responsibilities:
//
//  1. Call the sidecar's /embed endpoint with retry + timeout + batching.
//  2. Provide pure-Go helpers for storing / loading / comparing embeddings
//     as float32 BLOBs in MySQL (no float-array dialect needed).
//
// The rest of the codebase should not know about HTTP or about bge. It
// calls `embedder.Default().EmbedDocuments([]string{...})` and stores the
// returned []byte on the artifact row. When selecting artifacts for
// injection, it calls `embedder.Default().EmbedQuery("...")` and scores
// candidates via CosineSimilarity.
//
// Failure mode: if the sidecar is down we do NOT want to block refinery
// or agent dispatch. The client returns an error, and every call site
// treats "no embedding available" as graceful degradation (fall back to
// tag-based / confidence-based ranking). Logs capture the outage.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sync"
	"time"
)

// -- Configuration --------------------------------------------------------

const (
	// Default sidecar URL. Override with A3C_EMBEDDER_URL env var when the
	// sidecar lives on a different host (e.g. k8s service DNS).
	defaultEmbedderURL = "http://127.0.0.1:3011"

	// Keep below the server-side cap (256) so chunking is predictable and
	// single requests stay under ~5MB wire size even for long texts.
	embedBatchSize = 64

	// Per-request timeout. Encoding 64 short texts on CPU takes ~1-2s on a
	// mid-range laptop, GPU is an order of magnitude faster. 30s covers
	// cold-start tail latency with a big safety margin.
	embedRequestTimeout = 30 * time.Second

	// Simple exponential backoff. We retry twice on transient failures
	// (connection refused when sidecar restarts, 5xx). Don't retry on
	// 4xx — that's our fault.
	embedMaxRetries   = 2
	embedRetryBaseDur = 500 * time.Millisecond
)

// -- Client ---------------------------------------------------------------

// EmbeddingClient is safe for concurrent use; the underlying http.Client
// already is, and no other state is mutated after construction.
type EmbeddingClient struct {
	baseURL    string
	httpClient *http.Client
	dim        int        // populated on first successful /health or /embed
	dimMu      sync.Mutex // guards dim (once-init pattern)
}

// NewEmbeddingClient builds a client pointed at baseURL. If baseURL is
// empty the default local sidecar URL is used.
func NewEmbeddingClient(baseURL string) *EmbeddingClient {
	if baseURL == "" {
		baseURL = defaultEmbedderURL
	}
	return &EmbeddingClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: embedRequestTimeout,
		},
	}
}

// Default returns the process-wide default client (lazy singleton).
// Env var A3C_EMBEDDER_URL overrides the default URL.
var (
	defaultClient     *EmbeddingClient
	defaultClientOnce sync.Once
)

func DefaultEmbeddingClient() *EmbeddingClient {
	defaultClientOnce.Do(func() {
		url := os.Getenv("A3C_EMBEDDER_URL")
		defaultClient = NewEmbeddingClient(url)
	})
	return defaultClient
}

// -- /health --------------------------------------------------------------

type EmbedderHealth struct {
	Status    string `json:"status"`
	Model     string `json:"model"`
	CacheDir  string `json:"cache_dir"`
	Dim       int    `json:"dim"`
	Device    string `json:"device"`
	BatchSize int    `json:"batch_size"`
}

// Health checks whether the sidecar is reachable and returns its reported
// config. Useful for startup probes and admin endpoints.
func (c *EmbeddingClient) Health(ctx context.Context) (*EmbedderHealth, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedder /health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("embedder /health: %s: %s", resp.Status, string(body))
	}
	var h EmbedderHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, fmt.Errorf("embedder /health: decode: %w", err)
	}
	c.setDim(h.Dim)
	return &h, nil
}

// Dim returns the embedding dimensionality. Returns 0 if we haven't
// successfully contacted the sidecar yet.
func (c *EmbeddingClient) Dim() int {
	c.dimMu.Lock()
	defer c.dimMu.Unlock()
	return c.dim
}

func (c *EmbeddingClient) setDim(d int) {
	c.dimMu.Lock()
	defer c.dimMu.Unlock()
	if d > 0 {
		c.dim = d
	}
}

// -- /embed ---------------------------------------------------------------

type embedRequest struct {
	Texts   []string `json:"texts"`
	IsQuery bool     `json:"is_query"`
}

type embedResponse struct {
	Vectors [][]float32 `json:"vectors"`
	Dim     int         `json:"dim"`
	Device  string      `json:"device"`
	Count   int         `json:"count"`
}

// EmbedDocuments encodes documents (artifacts, episode summaries, file
// conventions) for later retrieval. The sidecar does NOT apply bge-zh's
// query prefix.
//
// Returns len(texts) vectors, each of length Dim(). An empty input slice
// returns (nil, nil).
func (c *EmbeddingClient) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(ctx, texts, false)
}

// EmbedQuery encodes a search query (e.g. a task description looking up
// relevant past patterns). The sidecar applies bge-zh's query prefix for
// better cross-side retrieval quality.
func (c *EmbeddingClient) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vecs, err := c.embed(ctx, []string{text}, true)
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embedder returned %d vectors for 1 query", len(vecs))
	}
	return vecs[0], nil
}

// embed is the shared implementation. It chunks texts into embedBatchSize
// requests and concatenates the results in the original order. All or
// nothing: a mid-batch failure aborts the whole call, since a partially
// populated result would silently produce wrong rankings downstream.
func (c *EmbeddingClient) embed(ctx context.Context, texts []string, isQuery bool) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Pre-allocate the result so concurrent chunks could be dropped in by
	// index later if we ever parallelise (we don't today — sequential
	// keeps load on the sidecar predictable).
	out := make([][]float32, 0, len(texts))

	for start := 0; start < len(texts); start += embedBatchSize {
		end := start + embedBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := c.embedBatchWithRetry(ctx, texts[start:end], isQuery)
		if err != nil {
			return nil, fmt.Errorf("embed batch [%d:%d]: %w", start, end, err)
		}
		if len(batch) != end-start {
			return nil, fmt.Errorf(
				"embedder returned %d vectors for batch of size %d",
				len(batch), end-start,
			)
		}
		out = append(out, batch...)
	}
	return out, nil
}

func (c *EmbeddingClient) embedBatchWithRetry(ctx context.Context, texts []string, isQuery bool) ([][]float32, error) {
	var lastErr error
	for attempt := 0; attempt <= embedMaxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 500ms, 1s, 2s, ...
			delay := embedRetryBaseDur * (1 << (attempt - 1))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		vecs, err := c.embedBatchOnce(ctx, texts, isQuery)
		if err == nil {
			return vecs, nil
		}
		lastErr = err

		// Only retry on transient errors: network failure or 5xx.
		if !isTransientEmbedError(err) {
			break
		}
		log.Printf("[Embedder] transient error (attempt %d/%d): %v",
			attempt+1, embedMaxRetries+1, err)
	}
	return nil, lastErr
}

// embedBatchOnce makes a single /embed call with no retry. Separated out
// so tests can drive it directly.
func (c *EmbeddingClient) embedBatchOnce(ctx context.Context, texts []string, isQuery bool) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Texts: texts, IsQuery: isQuery})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &transientError{wrapped: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err := fmt.Errorf("embedder returned %s: %s", resp.Status, string(raw))
		if resp.StatusCode >= 500 {
			return nil, &transientError{wrapped: err}
		}
		return nil, err
	}

	var r embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("embedder decode: %w", err)
	}
	c.setDim(r.Dim)
	return r.Vectors, nil
}

// -- Error classification -------------------------------------------------

// transientError wraps an underlying error to mark it as retry-eligible.
// Network hiccups, 5xx, and the sidecar briefly restarting all qualify.
type transientError struct{ wrapped error }

func (e *transientError) Error() string { return e.wrapped.Error() }
func (e *transientError) Unwrap() error { return e.wrapped }

func isTransientEmbedError(err error) bool {
	var te *transientError
	return errors.As(err, &te)
}

// -- Serialization helpers ------------------------------------------------
//
// We store embeddings as raw little-endian float32 BLOBs. Pros:
//   - Compact: 768-dim vector = 3072 bytes vs. ~15KB as JSON array
//   - Portable across MySQL / SQLite (no float[] type dependency)
//   - Decode is zero-alloc with a slice reinterpretation
//
// Cons:
//   - Not human-readable in the DB client — but we don't need that.
//   - Endianness pinned to little-endian, which matches every target
//     architecture we care about (amd64, arm64). Writing a different
//     endian would corrupt silently; asserted in MarshalEmbedding.

// MarshalEmbedding packs a float32 slice into a little-endian byte blob
// suitable for storing in a BLOB/VARBINARY column.
func MarshalEmbedding(vec []float32) []byte {
	if len(vec) == 0 {
		return nil
	}
	buf := make([]byte, 4*len(vec))
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// UnmarshalEmbedding parses a blob previously produced by MarshalEmbedding.
// Returns nil (not error) for empty/nil input so callers can treat
// "missing embedding" as just "no semantic signal yet".
func UnmarshalEmbedding(blob []byte) []float32 {
	if len(blob) == 0 || len(blob)%4 != 0 {
		return nil
	}
	n := len(blob) / 4
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return out
}

// -- Similarity -----------------------------------------------------------

// CosineSimilarity for two L2-normalised vectors reduces to a dot product.
// Our sidecar returns normalised vectors, so we skip the norm divide.
// If a caller passes non-normalised vectors the result is still a valid
// similarity measure — just not in [-1, 1].
//
// Returns 0 when either input is empty or dimensions don't match. This
// "silent zero" is deliberate: callers use cosine as one factor in a
// ranking score, and missing embeddings should simply contribute 0 to
// the final score, not crash the request.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

// CosineSimilarityBlob is a convenience that unmarshals two blobs and
// computes similarity in one call. Handy at injection-selection time.
func CosineSimilarityBlob(a, b []byte) float32 {
	return CosineSimilarity(UnmarshalEmbedding(a), UnmarshalEmbedding(b))
}
