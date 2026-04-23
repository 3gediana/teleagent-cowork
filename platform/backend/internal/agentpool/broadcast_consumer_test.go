package agentpool

// broadcast_consumer_test.go covers the pool's internal directed-
// broadcast consumer loop: queue drain, injection routing, filter
// of POOL_SESSION_ARCHIVE, and the couple of edge cases (no
// session yet, non-ready instance) that let the loop coexist
// with spawns mid-flight without panicking.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeBroadcastConsumer returns scripted events per agent. First
// FetchEvents call returns queue[0], second returns queue[1], etc.
// After the scripted list is exhausted it returns nil (empty).
type fakeBroadcastConsumer struct {
	mu      sync.Mutex
	queues  map[string][][]BroadcastEvent
	fetches map[string]int
	err     error
}

func newFakeBroadcastConsumer() *fakeBroadcastConsumer {
	return &fakeBroadcastConsumer{
		queues:  map[string][][]BroadcastEvent{},
		fetches: map[string]int{},
	}
}

func (f *fakeBroadcastConsumer) enqueue(agentID string, events []BroadcastEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues[agentID] = append(f.queues[agentID], events)
}

func (f *fakeBroadcastConsumer) FetchEvents(_ context.Context, agentID string) ([]BroadcastEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches[agentID]++
	if f.err != nil {
		return nil, f.err
	}
	q := f.queues[agentID]
	if len(q) == 0 {
		return nil, nil
	}
	head := q[0]
	f.queues[agentID] = q[1:]
	return head, nil
}

// fakeBroadcastInjector records every InjectMessage call in order.
// Exposes helpers so tests can assert shape + counts.
type fakeBroadcastInjector struct {
	mu    sync.Mutex
	calls []injectCall
	err   error
}

type injectCall struct {
	ServeURL   string
	SessionID  string
	Text       string
	ProviderID string
	ModelID    string
}

func (f *fakeBroadcastInjector) InjectMessage(_ context.Context, serveURL, sessionID, text, providerID, modelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, injectCall{serveURL, sessionID, text, providerID, modelID})
	return f.err
}

func (f *fakeBroadcastInjector) snapshot() []injectCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]injectCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// newBroadcastTestManager spins up a manager with fake spawner /
// store / session creator and the given broadcast pair. Spawn
// returns a ready instance so the caller can immediately enqueue
// broadcasts for it.
func newBroadcastTestManager(t *testing.T, consumer BroadcastConsumer, injector BroadcastInjector) (*Manager, *memStore) {
	t.Helper()
	store := newMemStore()
	sc := &fakeSessionCreator{initialID: "ses_bc_test"}
	m := NewManager(ManagerConfig{
		Root:                t.TempDir(),
		StartupTimeout:      2 * time.Second,
		ShutdownGrace:       50 * time.Millisecond,
		SkipOpencodeEnvPrep: true,
	}, &FakeSpawner{HealthDelay: 10 * time.Millisecond}).
		WithStore(store).
		WithSessionCreator(sc).
		WithBroadcastConsumer(consumer).
		WithBroadcastInjector(injector)
	return m, store
}

// TestBroadcastConsumer_InjectsNormalEvent — the happy path. A
// single non-archive event queued for a ready agent gets delivered
// via the injector with the right provider/model pair.
func TestBroadcastConsumer_InjectsNormalEvent(t *testing.T) {
	cons := newFakeBroadcastConsumer()
	inj := &fakeBroadcastInjector{}
	m, _ := newBroadcastTestManager(t, cons, inj)

	inst, err := m.Spawn(context.Background(), SpawnRequest{
		ProjectID:          "proj_b1",
		Name:               "bc-one",
		OpencodeProviderID: "minimax-coding-plan",
		OpencodeModelID:    "MiniMax-M2.7",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	cons.enqueue(inst.AgentID, []BroadcastEvent{
		{Type: "TASK_ASSIGN", MessageID: "dir_1", Payload: map[string]interface{}{"task_id": "t1"}},
	})

	// Drive one loop tick directly rather than spinning up the
	// background goroutine — makes the test deterministic.
	m.consumeOnce(context.Background())

	calls := inj.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 inject, got %d", len(calls))
	}
	if calls[0].SessionID != "ses_bc_test" {
		t.Errorf("session id routed wrong: got %q", calls[0].SessionID)
	}
	if calls[0].ProviderID != "minimax-coding-plan" || calls[0].ModelID != "MiniMax-M2.7" {
		t.Errorf("model not threaded through: %+v", calls[0])
	}
	if want := "[broadcast/TASK_ASSIGN id=dir_1] "; !startsWith(calls[0].Text, want) {
		t.Errorf("text shape wrong: %q", calls[0].Text)
	}
}

// TestBroadcastConsumer_SkipsArchiveEvent — POOL_SESSION_ARCHIVE
// must never hit the injector. The pool's context watcher has
// already rotated the session; echoing the event into the
// transcript would just confuse the LLM about which session it's
// on.
func TestBroadcastConsumer_SkipsArchiveEvent(t *testing.T) {
	cons := newFakeBroadcastConsumer()
	inj := &fakeBroadcastInjector{}
	m, _ := newBroadcastTestManager(t, cons, inj)

	inst, err := m.Spawn(context.Background(), SpawnRequest{
		Name:               "bc-arch",
		OpencodeProviderID: "p",
		OpencodeModelID:    "m",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	cons.enqueue(inst.AgentID, []BroadcastEvent{
		{Type: "POOL_SESSION_ARCHIVE", MessageID: "dir_arch", Payload: map[string]interface{}{"new_session_id": "ses_x"}},
		{Type: "TASK_ASSIGN", MessageID: "dir_2", Payload: map[string]interface{}{"foo": "bar"}},
	})

	m.consumeOnce(context.Background())

	calls := inj.snapshot()
	if len(calls) != 1 {
		t.Fatalf("archive must be filtered; expected 1 inject (TASK_ASSIGN), got %d", len(calls))
	}
	if calls[0].Text[:18] != "[broadcast/TASK_AS" {
		t.Errorf("wrong event injected, text=%q", calls[0].Text)
	}
}

// TestBroadcastConsumer_SkipsAgentWithoutSession — an agent whose
// SessionCreator failed has empty OpencodeSessionID. We must not
// inject (would error at opencode) and must not drain the queue
// (the events belong to the NEXT session once one lands).
func TestBroadcastConsumer_SkipsAgentWithoutSession(t *testing.T) {
	cons := newFakeBroadcastConsumer()
	inj := &fakeBroadcastInjector{}
	store := newMemStore()
	m := NewManager(ManagerConfig{
		Root:                t.TempDir(),
		StartupTimeout:      2 * time.Second,
		ShutdownGrace:       50 * time.Millisecond,
		SkipOpencodeEnvPrep: true,
	}, &FakeSpawner{HealthDelay: 10 * time.Millisecond}).
		WithStore(store).
		WithSessionCreator(&fakeSessionCreator{nextErr: fmt.Errorf("sess err")}).
		WithBroadcastConsumer(cons).
		WithBroadcastInjector(inj)

	inst, err := m.Spawn(context.Background(), SpawnRequest{Name: "no-sess"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if inst.OpencodeSessionID != "" {
		t.Fatal("precondition: session id must be empty")
	}
	cons.enqueue(inst.AgentID, []BroadcastEvent{{Type: "X", MessageID: "id"}})

	m.consumeOnce(context.Background())

	if len(inj.snapshot()) != 0 {
		t.Error("no injection expected when session id is missing")
	}
	// fetchEvents must NOT have been called for this agent —
	// otherwise we'd drop the queue on the floor.
	if cons.fetches[inst.AgentID] != 0 {
		t.Errorf("fetch should not run for agent without session, got %d calls", cons.fetches[inst.AgentID])
	}
}

// TestBroadcastConsumer_FetchErrorIsTolerated — the loop must
// survive a Redis / consumer error without dropping state and
// without blocking later agents.
func TestBroadcastConsumer_FetchErrorIsTolerated(t *testing.T) {
	cons := newFakeBroadcastConsumer()
	cons.err = fmt.Errorf("redis down")
	inj := &fakeBroadcastInjector{}
	m, _ := newBroadcastTestManager(t, cons, inj)

	_, err := m.Spawn(context.Background(), SpawnRequest{
		Name:               "bc-err",
		OpencodeProviderID: "p",
		OpencodeModelID:    "m",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Should not panic or block.
	m.consumeOnce(context.Background())

	if len(inj.snapshot()) != 0 {
		t.Error("no injection expected when fetch errored")
	}
}

// TestBroadcastConsumer_NonReadyIsSkipped — while an instance is
// still "starting" or "crashed" the loop must leave it alone.
// Previously a pool crash + fast broadcast arrival produced nil-
// pointer panics when the fake spawner's handle was already gone.
func TestBroadcastConsumer_NonReadyIsSkipped(t *testing.T) {
	cons := newFakeBroadcastConsumer()
	inj := &fakeBroadcastInjector{}
	m, _ := newBroadcastTestManager(t, cons, inj)

	inst, err := m.Spawn(context.Background(), SpawnRequest{
		Name:               "bc-ready",
		OpencodeProviderID: "p",
		OpencodeModelID:    "m",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// Force the instance back to a non-ready state to simulate a
	// mid-flight shutdown racing with a broadcast arrival.
	m.mu.Lock()
	m.instances[inst.ID].inst.Status = "stopped"
	m.mu.Unlock()

	cons.enqueue(inst.AgentID, []BroadcastEvent{{Type: "X", MessageID: "id"}})
	m.consumeOnce(context.Background())

	if len(inj.snapshot()) != 0 {
		t.Error("stopped instance should not receive broadcasts")
	}
}

// TestBroadcastConsumer_StartStopLoop — the background goroutine
// must actually deliver events when enabled, and must stop cleanly.
func TestBroadcastConsumer_StartStopLoop(t *testing.T) {
	cons := newFakeBroadcastConsumer()
	inj := &fakeBroadcastInjector{}
	m, _ := newBroadcastTestManager(t, cons, inj)

	inst, err := m.Spawn(context.Background(), SpawnRequest{
		Name:               "bc-loop",
		OpencodeProviderID: "p",
		OpencodeModelID:    "m",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	cons.enqueue(inst.AgentID, []BroadcastEvent{{Type: "Y", MessageID: "mm"}})

	m.StartBroadcastConsumer(context.Background(), 20*time.Millisecond)

	// Give the loop a few ticks to drain.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(inj.snapshot()) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	m.StopBroadcastConsumer()

	if got := len(inj.snapshot()); got != 1 {
		t.Errorf("expected 1 inject via loop, got %d", got)
	}
}

// TestRenderBroadcastText_JSONShape — locks in the exact text format
// the LLM sees, since prompt templates key on the `[broadcast/TYPE`
// prefix.
func TestRenderBroadcastText_JSONShape(t *testing.T) {
	out := RenderBroadcastText(BroadcastEvent{
		Type:      "TASK_ASSIGN",
		MessageID: "dir_42",
		Payload:   map[string]interface{}{"k": "v"},
	})
	want := `[broadcast/TASK_ASSIGN id=dir_42] {"k":"v"}`
	if out != want {
		t.Errorf("render mismatch:\n got: %s\nwant: %s", out, want)
	}
}

// startsWith is a tiny helper so tests can check prefix without
// pulling in strings.HasPrefix everywhere.
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
