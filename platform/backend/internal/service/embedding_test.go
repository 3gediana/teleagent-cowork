package service

// Unit tests for the pure-Go parts of the embedding client — no sidecar
// required. Covers: blob serialization round-trip, endian correctness,
// cosine similarity on L2-normalised vectors, retry classification, and
// batched HTTP request construction against a stub server.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestMarshalUnmarshalEmbedding_RoundTrip(t *testing.T) {
	cases := [][]float32{
		{},                               // empty
		{0},                              // zero
		{1, -1, 0.5, -0.5},               // sign mix
		{float32(math.Pi), -float32(math.E), 1e-9, 1e9}, // edge magnitudes
	}
	for _, in := range cases {
		blob := MarshalEmbedding(in)
		out := UnmarshalEmbedding(blob)
		if len(in) == 0 {
			if out != nil {
				t.Errorf("empty input should round-trip to nil, got %v", out)
			}
			continue
		}
		if len(out) != len(in) {
			t.Fatalf("length mismatch: in=%d out=%d", len(in), len(out))
		}
		for i := range in {
			if math.Float32bits(in[i]) != math.Float32bits(out[i]) {
				t.Errorf("value mismatch at %d: in=%v out=%v", i, in[i], out[i])
			}
		}
	}
}

func TestUnmarshalEmbedding_MalformedBlob(t *testing.T) {
	// Non-multiple-of-4 byte count should return nil rather than panic.
	if got := UnmarshalEmbedding([]byte{1, 2, 3}); got != nil {
		t.Errorf("malformed blob should return nil, got %v", got)
	}
}

func TestCosineSimilarity_NormalizedVectors(t *testing.T) {
	// Parallel unit vectors → similarity 1
	a := normalize([]float32{3, 4})
	b := normalize([]float32{3, 4})
	if got := CosineSimilarity(a, b); !approxEq(got, 1.0, 1e-6) {
		t.Errorf("parallel unit vectors: expected 1, got %v", got)
	}

	// Orthogonal unit vectors → similarity 0
	c := normalize([]float32{1, 0})
	d := normalize([]float32{0, 1})
	if got := CosineSimilarity(c, d); !approxEq(got, 0.0, 1e-6) {
		t.Errorf("orthogonal unit vectors: expected 0, got %v", got)
	}

	// Anti-parallel unit vectors → similarity -1
	e := normalize([]float32{1, 0})
	f := normalize([]float32{-1, 0})
	if got := CosineSimilarity(e, f); !approxEq(got, -1.0, 1e-6) {
		t.Errorf("anti-parallel unit vectors: expected -1, got %v", got)
	}
}

func TestCosineSimilarity_SafeOnBadInput(t *testing.T) {
	// Dimension mismatch → 0 (not panic, not error)
	if got := CosineSimilarity([]float32{1, 2}, []float32{1, 2, 3}); got != 0 {
		t.Errorf("dim mismatch: expected 0, got %v", got)
	}
	// Empty inputs → 0
	if got := CosineSimilarity(nil, nil); got != 0 {
		t.Errorf("empty: expected 0, got %v", got)
	}
}

func TestCosineSimilarityBlob_CombinesRoundTripAndScore(t *testing.T) {
	a := MarshalEmbedding(normalize([]float32{1, 0, 0}))
	b := MarshalEmbedding(normalize([]float32{1, 0, 0}))
	if got := CosineSimilarityBlob(a, b); !approxEq(got, 1.0, 1e-6) {
		t.Errorf("identical blobs should score 1, got %v", got)
	}
}

// TestEmbeddingClient_BatchesCorrectly verifies that a request larger than
// embedBatchSize is split into multiple HTTP calls and the results are
// concatenated in the original order.
func TestEmbeddingClient_BatchesCorrectly(t *testing.T) {
	var calls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, err.Error(), 400)
			return
		}
		// Return one 3-dim vector per input whose first coord encodes the
		// (truncated) text length so we can verify per-item ordering.
		vecs := make([][]float32, len(req.Texts))
		for i, tx := range req.Texts {
			vecs[i] = []float32{float32(len(tx)), 0, 0}
		}
		_ = json.NewEncoder(w).Encode(embedResponse{
			Vectors: vecs, Dim: 3, Device: "cpu", Count: len(req.Texts),
		})
	}))
	defer srv.Close()

	// Construct 150 texts of varying lengths → expect ceil(150/64) = 3 batches.
	n := 150
	texts := make([]string, n)
	for i := 0; i < n; i++ {
		texts[i] = repeatChar('x', i%17) // 0..16 chars
	}

	cli := NewEmbeddingClient(srv.URL)
	vecs, err := cli.EmbedDocuments(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(vecs) != n {
		t.Fatalf("expected %d vectors, got %d", n, len(vecs))
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 HTTP calls, got %d", calls)
	}
	for i := range vecs {
		want := float32(len(texts[i]))
		if vecs[i][0] != want {
			t.Errorf("order scrambled at index %d: vec[0]=%v, want %v", i, vecs[i][0], want)
		}
	}
}

func TestEmbeddingClient_EmptyTextsIsNoOp(t *testing.T) {
	// No server needed — client must short-circuit.
	cli := NewEmbeddingClient("http://127.0.0.1:1") // refused-conn URL if called
	vecs, err := cli.EmbedDocuments(context.Background(), nil)
	if err != nil {
		t.Fatalf("empty input should be a no-op, got err=%v", err)
	}
	if vecs != nil {
		t.Errorf("expected nil, got %v", vecs)
	}
}

func TestEmbeddingClient_QueryAppliesPrefixFlag(t *testing.T) {
	var seenIsQuery bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		seenIsQuery = req.IsQuery
		_ = json.NewEncoder(w).Encode(embedResponse{
			Vectors: [][]float32{{1, 0, 0}}, Dim: 3, Count: 1,
		})
	}))
	defer srv.Close()

	cli := NewEmbeddingClient(srv.URL)
	if _, err := cli.EmbedQuery(context.Background(), "hello"); err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if !seenIsQuery {
		t.Error("EmbedQuery should send is_query=true, sidecar saw false")
	}
}

func TestEmbeddingClient_Retries5xxButNot4xx(t *testing.T) {
	var calls int32
	var mode atomic.Value
	mode.Store("500")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		switch mode.Load().(string) {
		case "500":
			// First attempt fails with 503, second succeeds.
			if n == 1 {
				http.Error(w, "restarting", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(embedResponse{
				Vectors: [][]float32{{1, 0, 0}}, Dim: 3, Count: 1,
			})
		case "400":
			http.Error(w, "bad request", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	cli := NewEmbeddingClient(srv.URL)

	// 503 → retry, eventual success
	atomic.StoreInt32(&calls, 0)
	mode.Store("500")
	if _, err := cli.EmbedDocuments(context.Background(), []string{"a"}); err != nil {
		t.Errorf("expected recovery after 503, got err=%v", err)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Errorf("expected retry, only %d calls made", calls)
	}

	// 400 → no retry, fail fast
	atomic.StoreInt32(&calls, 0)
	mode.Store("400")
	if _, err := cli.EmbedDocuments(context.Background(), []string{"a"}); err == nil {
		t.Error("expected error on 400, got nil")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("4xx should not retry, got %d calls", calls)
	}
}

func TestEmbeddingClient_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(embedResponse{
			Vectors: [][]float32{{1, 0, 0}}, Dim: 3, Count: 1,
		})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	cli := NewEmbeddingClient(srv.URL)
	_, err := cli.EmbedDocuments(ctx, []string{"will timeout"})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// --- helpers -------------------------------------------------------------

func normalize(v []float32) []float32 {
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		return v
	}
	inv := 1.0 / float32(math.Sqrt(float64(sum)))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

func approxEq(a, b, tol float32) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= tol
}

func repeatChar(c byte, n int) string {
	if n == 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
