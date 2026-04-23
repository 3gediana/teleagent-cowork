package agentpool

// Tests for the context watcher: does it probe, does it rotate at
// the threshold, does it survive a flaky probe, does it stay quiet
// below the line? These use a handcrafted fake probe instead of
// httptest so the tests run in single-digit milliseconds even when
// the watcher's ticker fires often.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/a3c/platform/internal/model"
)

type fakeContextProbe struct {
	mu     sync.Mutex
	calls  int
	// tokens is returned for any session id not listed in perSession.
	// This keeps the simple "always returns N" test case readable.
	tokens int
	// perSession lets a test answer differently per session id — we
	// use it to model the real serve behaviour where a freshly
	// rotated session reports close to zero while the old one was
	// full. Without this the watcher would rotate forever in tests.
	perSession map[string]int
	err        error
}

func (f *fakeContextProbe) ContextSize(_ context.Context, _, sessionID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	if v, ok := f.perSession[sessionID]; ok {
		return v, nil
	}
	return f.tokens, nil
}

type fakeArchiveNotifier struct {
	mu           sync.Mutex
	lastAgentID  string
	lastOldID    string
	lastNewID    string
	lastTokens   int
	lastReason   string
	invocations  int
}

func (f *fakeArchiveNotifier) NotifyArchive(agentID, oldID, newID string, tokens int, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invocations++
	f.lastAgentID = agentID
	f.lastOldID = oldID
	f.lastNewID = newID
	f.lastTokens = tokens
	f.lastReason = reason
}

// buildWatcherManager spins up a ready pool instance without going
// through Spawn (which would require a real subprocess or FakeSpawner
// dance). Seeds the internal map so the watcher has something to
// tick over. Returns the manager and the instance id.
func buildWatcherManager(t *testing.T, probe ContextProbe, sc SessionCreator, notifier ArchiveNotifier, threshold int) (*Manager, *subprocess) {
	t.Helper()
	m := NewManager(ManagerConfig{
		Root:                   t.TempDir(),
		StartupTimeout:         1 * time.Second,
		ShutdownGrace:          10 * time.Millisecond,
		ContextWatchInterval:   20 * time.Millisecond,
		ArchiveThresholdTokens: threshold,
		SkipOpencodeEnvPrep:    true,
	}, &FakeSpawner{HealthDelay: 10 * time.Millisecond}).
		WithStore(newMemStore()).
		WithSessionCreator(sc).
		WithContextProbe(probe).
		WithArchiveNotifier(notifier)

	// Hand-craft a ready subprocess so we skip the full Spawn path
	// (tested elsewhere). The watcher only reads Status, Port,
	// OpencodeSessionID, AgentID, AgentName — so those are the
	// fields we populate.
	sp := &subprocess{
		inst: Instance{
			ID:                "pool_watcher_test",
			AgentID:           "agent_w",
			AgentName:         "alpha",
			Port:              48123,
			Status:            "ready",
			OpencodeSessionID: "ses_initial",
			ArchiveRotation:   0,
		},
	}
	m.mu.Lock()
	m.instances[sp.inst.ID] = sp
	m.mu.Unlock()
	// Register the agent row so UpdateAgent has a target when we
	// rotate.
	_ = m.store.CreateAgent(&model.Agent{ID: sp.inst.AgentID, Name: sp.inst.AgentName})
	return m, sp
}

func TestContextWatcher_BelowThreshold_NoRotation(t *testing.T) {
	probe := &fakeContextProbe{tokens: 40_000}
	sc := &fakeSessionCreator{}
	notifier := &fakeArchiveNotifier{}
	m, sp := buildWatcherManager(t, probe, sc, notifier, 150_000)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	m.StartContextWatcher(ctx)
	// Let a few ticks run, then stop.
	time.Sleep(80 * time.Millisecond)
	m.stopContextWatcher()

	probe.mu.Lock()
	if probe.calls == 0 {
		t.Error("probe never ran")
	}
	probe.mu.Unlock()

	if sc.archiveCalls != 0 {
		t.Errorf("below threshold should not rotate; got %d archive calls", sc.archiveCalls)
	}
	if notifier.invocations != 0 {
		t.Errorf("below threshold should not notify; got %d", notifier.invocations)
	}
	if sp.inst.OpencodeSessionID != "ses_initial" {
		t.Errorf("session id changed unexpectedly: %s", sp.inst.OpencodeSessionID)
	}
	if sp.inst.LastContextTokens != 40_000 {
		t.Errorf("LastContextTokens not cached; got %d", sp.inst.LastContextTokens)
	}
}

func TestContextWatcher_AboveThreshold_RotatesOnce(t *testing.T) {
	// Old session is full, new session reports zero — matches the
	// real serve. Without the per-session map the watcher would
	// rotate on every tick because the probe never "forgot" the
	// old reading.
	probe := &fakeContextProbe{
		perSession: map[string]int{
			"ses_initial": 160_000,
			"ses_rotated": 0,
		},
	}
	sc := &fakeSessionCreator{archiveID: "ses_rotated"}
	notifier := &fakeArchiveNotifier{}
	m, sp := buildWatcherManager(t, probe, sc, notifier, 150_000)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	m.StartContextWatcher(ctx)
	// Let several ticks run — the first will rotate, subsequent
	// ticks should see the new session id and 0 tokens so they
	// stay quiet.
	time.Sleep(160 * time.Millisecond)
	m.stopContextWatcher()

	if sc.archiveCalls == 0 {
		t.Fatal("should have rotated at least once")
	}
	if sc.archiveCalls > 1 {
		t.Errorf("should rotate once then settle; got %d rotations", sc.archiveCalls)
	}
	if sp.inst.OpencodeSessionID != "ses_rotated" {
		t.Errorf("session id not updated; got %s", sp.inst.OpencodeSessionID)
	}
	if sp.inst.ArchiveRotation != 1 {
		t.Errorf("rotation count should be 1; got %d", sp.inst.ArchiveRotation)
	}
	if sp.inst.LastContextTokens != 0 {
		t.Errorf("LastContextTokens should reset after rotation; got %d", sp.inst.LastContextTokens)
	}
	if notifier.invocations != 1 {
		t.Fatalf("should have notified once; got %d", notifier.invocations)
	}
	if notifier.lastOldID != "ses_initial" || notifier.lastNewID != "ses_rotated" {
		t.Errorf("notifier got wrong ids: old=%s new=%s", notifier.lastOldID, notifier.lastNewID)
	}
	if notifier.lastReason != "context_exceeded" {
		t.Errorf("notifier reason should be context_exceeded; got %s", notifier.lastReason)
	}
}

func TestContextWatcher_ProbeErrorIsTolerated(t *testing.T) {
	probe := &fakeContextProbe{err: fmt.Errorf("serve restarted")}
	sc := &fakeSessionCreator{}
	notifier := &fakeArchiveNotifier{}
	m, sp := buildWatcherManager(t, probe, sc, notifier, 150_000)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	m.StartContextWatcher(ctx)
	time.Sleep(80 * time.Millisecond)
	m.stopContextWatcher()

	// Probe should have been called (multiple times) but nothing
	// should have rotated.
	probe.mu.Lock()
	if probe.calls == 0 {
		t.Error("probe never ran")
	}
	probe.mu.Unlock()
	if sc.archiveCalls != 0 {
		t.Errorf("probe errors should not trigger archive; got %d", sc.archiveCalls)
	}
	if sp.inst.OpencodeSessionID != "ses_initial" {
		t.Errorf("session id changed despite probe failing: %s", sp.inst.OpencodeSessionID)
	}
}

func TestContextWatcher_IntervalZeroDisables(t *testing.T) {
	probe := &fakeContextProbe{tokens: 200_000}
	sc := &fakeSessionCreator{}

	m := NewManager(ManagerConfig{
		Root:                   t.TempDir(),
		StartupTimeout:         1 * time.Second,
		ShutdownGrace:          10 * time.Millisecond,
		ContextWatchInterval:   0, // explicit off
		ArchiveThresholdTokens: 150_000,
		SkipOpencodeEnvPrep:    true,
	}, &FakeSpawner{HealthDelay: 10 * time.Millisecond}).
		WithStore(newMemStore()).
		WithSessionCreator(sc).
		WithContextProbe(probe)

	m.StartContextWatcher(context.Background())
	time.Sleep(80 * time.Millisecond)
	m.stopContextWatcher()

	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.calls != 0 {
		t.Errorf("interval=0 should disable watcher; probe ran %d times", probe.calls)
	}
}
