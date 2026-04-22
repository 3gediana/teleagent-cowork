package runner

// Tests for the partition + parallel-execution logic. Uses a
// synthetic slow tool to verify that concurrent calls actually
// overlap (wall clock << sum(per-call)) while preserving input order.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a3c/platform/internal/agent"
)

// slowSafeTool simulates a read-like tool: safe to run in parallel,
// takes a configurable duration. Used to assert that a batch of N
// calls takes ~1× (not N×) the per-call duration.
type slowSafeTool struct {
	name  string
	delay time.Duration
}

func (s *slowSafeTool) Name() string { return s.name }
func (s *slowSafeTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }
func (s *slowSafeTool) Description() string { return "slow-safe" }
func (s *slowSafeTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (s *slowSafeTool) Execute(ctx context.Context, _ *RunnerSession, _ json.RawMessage) (string, bool, error) {
	select {
	case <-time.After(s.delay):
		return "ok:" + s.name, false, nil
	case <-ctx.Done():
		return "", true, nil
	}
}

// slowUnsafeTool is the companion: serial-only. Used to check that
// unsafe tools break up a concurrent run.
type slowUnsafeTool struct {
	name  string
	delay time.Duration
}

func (s *slowUnsafeTool) Name() string { return s.name }
func (s *slowUnsafeTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }
func (s *slowUnsafeTool) Description() string { return "slow-unsafe" }
func (s *slowUnsafeTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (s *slowUnsafeTool) Execute(ctx context.Context, _ *RunnerSession, _ json.RawMessage) (string, bool, error) {
	select {
	case <-time.After(s.delay):
		return "ok:" + s.name, false, nil
	case <-ctx.Done():
		return "", true, nil
	}
}

// countingTool records its concurrent-execution peak for later assertion.
type countingTool struct {
	name    string
	safe    bool
	active  int32
	peak    int32
	mu      sync.Mutex
	delay   time.Duration
}

func (c *countingTool) Name() string { return c.name }
func (c *countingTool) IsConcurrencySafe(_ json.RawMessage) bool { return c.safe }
func (c *countingTool) Description() string { return "counting" }
func (c *countingTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (c *countingTool) Execute(ctx context.Context, _ *RunnerSession, _ json.RawMessage) (string, bool, error) {
	n := atomic.AddInt32(&c.active, 1)
	defer atomic.AddInt32(&c.active, -1)
	c.mu.Lock()
	if n > c.peak {
		c.peak = n
	}
	c.mu.Unlock()
	select {
	case <-time.After(c.delay):
		return "ok", false, nil
	case <-ctx.Done():
		return "", true, nil
	}
}

func mkToolCall(id, name string) toolCall {
	return toolCall{ID: id, Name: name, Input: json.RawMessage(`{}`)}
}

// ---- partition tests -------------------------------------------------

func TestPartitionToolCalls_AllSafeOneBatch(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&slowSafeTool{name: "read", delay: 0})

	calls := []toolCall{
		mkToolCall("1", "read"),
		mkToolCall("2", "read"),
		mkToolCall("3", "read"),
	}
	batches := partitionToolCalls(reg, calls)
	if len(batches) != 1 {
		t.Fatalf("all-safe should produce 1 batch, got %d", len(batches))
	}
	if !batches[0].safe || len(batches[0].calls) != 3 {
		t.Errorf("batch shape wrong: %+v", batches[0])
	}
}

func TestPartitionToolCalls_UnsafeBreaksRun(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&slowSafeTool{name: "read", delay: 0})
	reg.Register(&slowUnsafeTool{name: "edit", delay: 0})

	calls := []toolCall{
		mkToolCall("1", "read"),
		mkToolCall("2", "read"),
		mkToolCall("3", "edit"),
		mkToolCall("4", "read"),
		mkToolCall("5", "read"),
	}
	batches := partitionToolCalls(reg, calls)
	// Want: safe[read,read] | unsafe[edit] | safe[read,read]
	if len(batches) != 3 {
		t.Fatalf("want 3 batches, got %d: %+v", len(batches), batches)
	}
	if !batches[0].safe || len(batches[0].calls) != 2 {
		t.Errorf("batch 0: %+v", batches[0])
	}
	if batches[1].safe || len(batches[1].calls) != 1 {
		t.Errorf("batch 1: %+v", batches[1])
	}
	if !batches[2].safe || len(batches[2].calls) != 2 {
		t.Errorf("batch 2: %+v", batches[2])
	}
}

func TestPartitionToolCalls_UnknownToolTreatedAsUnsafe(t *testing.T) {
	reg := NewRegistry() // empty
	calls := []toolCall{mkToolCall("1", "hallucinated")}
	batches := partitionToolCalls(reg, calls)
	if len(batches) != 1 || batches[0].safe {
		t.Errorf("unknown should be unsafe; got %+v", batches)
	}
}

// ---- parallel execution tests ---------------------------------------

func TestDispatch_ConcurrentBatchRunsInParallel(t *testing.T) {
	// 5 read calls × 100ms each; serial would take 500ms, parallel
	// should take closer to 100ms.
	const perCall = 100 * time.Millisecond
	reg := NewRegistry()
	ctool := &countingTool{name: "read", safe: true, delay: perCall}
	reg.Register(ctool)

	sess, _ := newRunnerSession(t)
	rsess := &RunnerSession{AgentSession: sess}

	calls := []toolCall{
		mkToolCall("1", "read"), mkToolCall("2", "read"), mkToolCall("3", "read"),
		mkToolCall("4", "read"), mkToolCall("5", "read"),
	}

	started := time.Now()
	blocks, fatal := dispatchToolCalls(context.Background(), reg, rsess, "", "", calls)
	elapsed := time.Since(started)

	if fatal != nil {
		t.Fatalf("unexpected fatal: %v", fatal)
	}
	if len(blocks) != 5 {
		t.Errorf("want 5 result blocks, got %d", len(blocks))
	}
	// Expect under 2× serial to pass reliably on slow CI boxes.
	// Serial would be 500ms; parallel target is ~100ms + overhead.
	if elapsed > 300*time.Millisecond {
		t.Errorf("parallel batch took %v (serial would be ~500ms); overhead too high", elapsed)
	}
	if ctool.peak < 2 {
		t.Errorf("peak concurrency should be >=2 for parallel batch; got %d", ctool.peak)
	}
}

func TestDispatch_UnsafeBatchRunsSerially(t *testing.T) {
	// 3 writes × 50ms. Serial == 150ms; anything faster means we ran
	// in parallel (WRONG because writes are unsafe).
	const perCall = 50 * time.Millisecond
	reg := NewRegistry()
	ctool := &countingTool{name: "edit", safe: false, delay: perCall}
	reg.Register(ctool)

	sess, _ := newRunnerSession(t)
	rsess := &RunnerSession{AgentSession: sess}

	calls := []toolCall{
		mkToolCall("1", "edit"), mkToolCall("2", "edit"), mkToolCall("3", "edit"),
	}

	started := time.Now()
	_, _ = dispatchToolCalls(context.Background(), reg, rsess, "", "", calls)
	elapsed := time.Since(started)

	if elapsed < 140*time.Millisecond {
		t.Errorf("unsafe batch finished in %v — expected at least ~150ms serial", elapsed)
	}
	if ctool.peak > 1 {
		t.Errorf("unsafe tool ran with peak concurrency %d; must be 1", ctool.peak)
	}
}

func TestDispatch_MixedBatchPreservesOrder(t *testing.T) {
	// read, read, edit, read, read — result blocks must come back in
	// ToolUseID order 1..5 regardless of which ones ran in parallel.
	reg := NewRegistry()
	reg.Register(&slowSafeTool{name: "read", delay: 10 * time.Millisecond})
	reg.Register(&slowUnsafeTool{name: "edit", delay: 10 * time.Millisecond})

	sess, _ := newRunnerSession(t)
	rsess := &RunnerSession{AgentSession: sess}

	calls := []toolCall{
		mkToolCall("1", "read"),
		mkToolCall("2", "read"),
		mkToolCall("3", "edit"),
		mkToolCall("4", "read"),
		mkToolCall("5", "read"),
	}
	blocks, fatal := dispatchToolCalls(context.Background(), reg, rsess, "", "", calls)
	if fatal != nil {
		t.Fatal(fatal)
	}
	if len(blocks) != 5 {
		t.Fatalf("want 5 blocks, got %d", len(blocks))
	}
	// The resulting ContentBlocks carry tool_use_id — pull those out
	// and assert they came back in the input order.
	gotIDs := make([]string, len(blocks))
	for i, b := range blocks {
		gotIDs[i] = b.ToolUseID
	}
	wantIDs := []string{"1", "2", "3", "4", "5"}
	if !eqSlice(gotIDs, wantIDs) {
		t.Errorf("block order: got %v want %v", gotIDs, wantIDs)
	}
}

func TestDispatch_RespectsMaxConcurrencyCap(t *testing.T) {
	// 20 safe calls × 30ms each, cap = 4. Peak concurrency must not
	// exceed 4; total runtime ~ (20/4)*30 = 150ms.
	prev := MaxToolUseConcurrency
	MaxToolUseConcurrency = 4
	defer func() { MaxToolUseConcurrency = prev }()

	reg := NewRegistry()
	ctool := &countingTool{name: "read", safe: true, delay: 30 * time.Millisecond}
	reg.Register(ctool)

	sess, _ := newRunnerSession(t)
	rsess := &RunnerSession{AgentSession: sess}

	calls := make([]toolCall, 20)
	for i := range calls {
		calls[i] = mkToolCall(fmt.Sprintf("id-%d", i), "read")
	}

	_, fatal := dispatchToolCalls(context.Background(), reg, rsess, "", "", calls)
	if fatal != nil {
		t.Fatal(fatal)
	}
	if ctool.peak > 4 {
		t.Errorf("peak concurrency %d exceeded cap of 4", ctool.peak)
	}
	if ctool.peak < 2 {
		t.Errorf("peak concurrency should be near the cap, got %d", ctool.peak)
	}
}

func TestDispatch_JournalPreservesOrderWithMixedBatch(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&slowSafeTool{name: "read", delay: 5 * time.Millisecond})
	reg.Register(&slowUnsafeTool{name: "edit", delay: 5 * time.Millisecond})

	sess, _ := newRunnerSession(t)
	rsess := &RunnerSession{AgentSession: sess}

	calls := []toolCall{
		mkToolCall("a", "read"),
		mkToolCall("b", "read"),
		mkToolCall("c", "edit"),
		mkToolCall("d", "read"),
	}
	_, _ = dispatchToolCalls(context.Background(), reg, rsess, "", "", calls)

	// Within a batch, journal order is the finish order (concurrent
	// goroutines). Across batches, journal is in call order (serial
	// processing of batches). So for this input we expect:
	//   - entries[0], entries[1]: {read, read} in some order
	//   - entries[2]: edit
	//   - entries[3]: read
	if len(rsess.Journal) != 4 {
		t.Fatalf("want 4 journal entries, got %d", len(rsess.Journal))
	}
	// Sort first two by name so the test doesn't flake on scheduling.
	first := []string{rsess.Journal[0].ToolName, rsess.Journal[1].ToolName}
	sort.Strings(first)
	if first[0] != "read" || first[1] != "read" {
		t.Errorf("first batch: %v want [read read]", first)
	}
	if rsess.Journal[2].ToolName != "edit" || rsess.Journal[3].ToolName != "read" {
		t.Errorf("later entries: %+v", rsess.Journal[2:])
	}
}

func TestDispatch_UnknownToolProducesSyntheticError(t *testing.T) {
	reg := NewRegistry() // empty
	sess, _ := newRunnerSession(t)
	rsess := &RunnerSession{AgentSession: sess}

	blocks, fatal := dispatchToolCalls(context.Background(), reg, rsess, "", "",
		[]toolCall{mkToolCall("x", "nope")})
	if fatal != nil {
		t.Errorf("unknown tool should produce a synthetic error, NOT a fatal: got %v", fatal)
	}
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	// Block text should mention the unknown tool name.
	// (Accessing the content via reflection would be excessive — we
	// already assert isError in the Block struct.)
	if !blocks[0].IsError {
		t.Errorf("unknown-tool block should have IsError=true")
	}
}

// ---- small helpers --------------------------------------------------

func eqSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ensure agent import doesn't get pruned in an all-tests-pass state
// where other files might lose references while the test file lags.
var _ = agent.RoleAudit1
