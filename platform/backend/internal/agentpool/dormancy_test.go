package agentpool

// dormancy_test.go covers the Phase-4 idle lifecycle:
//
//   - ready + activity stamp drives the dormancy detector's idle
//     reaping decision
//   - enterDormancy preserves Instance metadata but terminates the
//     subprocess and flips Status to "dormant"
//   - Wake revives the same instance with a fresh subprocess +
//     session, keeping agent id + access key
//   - manual Sleep + failed Wake both go through the same code
//     paths as the automatic detector
//
// We lean heavily on the existing memStore + FakeSpawner +
// fakeSessionCreator test infra so nothing here needs a live
// opencode.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/a3c/platform/internal/model"
)

// newDormancyManager builds a manager wired with the pieces dormancy
// tests care about (SessionCreator for archive-on-sleep, Store for
// access-key lookup on Wake). Returns the manager + memStore so
// assertions can peek at the agent row.
func newDormancyManager(t *testing.T) (*Manager, *memStore, *fakeSessionCreator) {
	t.Helper()
	store := newMemStore()
	sc := &fakeSessionCreator{initialID: "ses_dorm_initial"}
	m := NewManager(ManagerConfig{
		Root:                  t.TempDir(),
		StartupTimeout:        2 * time.Second,
		ShutdownGrace:         50 * time.Millisecond,
		SkipOpencodeEnvPrep:   true,
		IdleTimeout:           200 * time.Millisecond, // short for tests
		DormancyCheckInterval: 20 * time.Millisecond,
	}, &FakeSpawner{HealthDelay: 10 * time.Millisecond}).
		WithStore(store).
		WithSessionCreator(sc)
	return m, store, sc
}

// spawnReady is a convenience — spawn a ready agent with a session,
// fast-forward the activity stamp, and hand back the instance.
func spawnReady(t *testing.T, m *Manager, name string) *Instance {
	t.Helper()
	inst, err := m.Spawn(context.Background(), SpawnRequest{
		ProjectID:          "proj_dorm",
		Name:               name,
		OpencodeProviderID: "minimax-coding-plan",
		OpencodeModelID:    "MiniMax-M2.7",
	})
	if err != nil {
		t.Fatalf("spawn %s: %v", name, err)
	}
	if inst.Status != "ready" {
		t.Fatalf("precondition: expected ready, got %q", inst.Status)
	}
	return inst
}

// TestEnterDormancy_ReadyToDormant — the happy path. A ready agent
// with a session gets archived, torn down, and flipped to dormant.
// Instance metadata (agent id, provider/model) survives; port + pid
// are cleared.
func TestEnterDormancy_ReadyToDormant(t *testing.T) {
	m, store, sc := newDormancyManager(t)
	inst := spawnReady(t, m, "dorm-alpha")
	// Pre-seed the DB-side agent row so Wake's store.GetAgent has
	// an access key to return. Spawn populates this already but
	// defensively check.
	if a, _ := store.GetAgent(inst.AgentID); a == nil || a.AccessKey == "" {
		t.Fatalf("expected agent row with access_key after Spawn")
	}

	if err := m.EnterDormancy(context.Background(), inst.ID, "manual"); err != nil {
		t.Fatalf("EnterDormancy: %v", err)
	}

	got, _ := m.Get(inst.ID)
	if got.Status != "dormant" {
		t.Errorf("status: want dormant, got %q", got.Status)
	}
	if got.PID != 0 || got.Port != 0 {
		t.Errorf("pid/port should be zeroed, got pid=%d port=%d", got.PID, got.Port)
	}
	if got.AgentID != inst.AgentID {
		t.Errorf("agent id must survive dormancy: was %q got %q", inst.AgentID, got.AgentID)
	}
	if got.OpencodeProviderID != "minimax-coding-plan" {
		t.Errorf("provider id must survive dormancy, got %q", got.OpencodeProviderID)
	}
	if got.DormantAt.IsZero() {
		t.Error("DormantAt not stamped on dormancy transition")
	}
	// Archive rotation should have bumped exactly once (the
	// "stopping point" archive call).
	if got.ArchiveRotation != 1 {
		t.Errorf("expected rotation=1 after dormancy archive, got %d", got.ArchiveRotation)
	}
	if sc.archiveCalls != 1 {
		t.Errorf("expected SessionCreator.CreateArchiveSession called once, got %d", sc.archiveCalls)
	}

	// Agent DB row should be marked offline.
	a, _ := store.GetAgent(inst.AgentID)
	if a.Status != "offline" {
		t.Errorf("agent row status: want offline, got %q", a.Status)
	}
}

// TestEnterDormancy_RefusesNonReady — only ready instances should
// transition. Starting/crashed/stopped/dormant all error out so
// the dashboard button surfaces a real message instead of a silent
// no-op.
func TestEnterDormancy_RefusesNonReady(t *testing.T) {
	m, _, _ := newDormancyManager(t)
	inst := spawnReady(t, m, "dorm-states")

	// Put it into dormant first and then retry — should error.
	if err := m.EnterDormancy(context.Background(), inst.ID, "t1"); err != nil {
		t.Fatalf("first dormancy: %v", err)
	}
	if err := m.EnterDormancy(context.Background(), inst.ID, "t2"); err == nil {
		t.Error("second EnterDormancy on already-dormant should error")
	}
}

// TestEnterDormancy_MissingInstance — unknown instance id should
// error so callers don't silently succeed.
func TestEnterDormancy_MissingInstance(t *testing.T) {
	m, _, _ := newDormancyManager(t)
	if err := m.EnterDormancy(context.Background(), "pool_nope", "x"); err == nil {
		t.Error("expected error for unknown instance id")
	}
}

// TestWake_DormantToReady — mirror of ReadyToDormant. Wake respawns
// the subprocess, issues a fresh session, and flips back to ready.
// Agent id + access key must be identical to pre-dormancy.
func TestWake_DormantToReady(t *testing.T) {
	m, store, _ := newDormancyManager(t)
	inst := spawnReady(t, m, "dorm-roundtrip")
	agentID := inst.AgentID
	preKey, _ := store.GetAgent(agentID)

	if err := m.EnterDormancy(context.Background(), inst.ID, "pre-wake"); err != nil {
		t.Fatalf("EnterDormancy: %v", err)
	}

	woken, err := m.Wake(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if woken.Status != "ready" {
		t.Errorf("post-Wake status: want ready, got %q", woken.Status)
	}
	if woken.AgentID != agentID {
		t.Errorf("agent id must survive wake: was %q got %q", agentID, woken.AgentID)
	}
	if woken.PID == 0 || woken.Port == 0 {
		t.Errorf("wake should assign new pid/port, got pid=%d port=%d", woken.PID, woken.Port)
	}
	if woken.OpencodeSessionID == "" {
		t.Error("wake should bind a fresh session id")
	}
	postKey, _ := store.GetAgent(agentID)
	if preKey.AccessKey != postKey.AccessKey {
		t.Errorf("access key must not change across sleep/wake: pre=%s post=%s", preKey.AccessKey, postKey.AccessKey)
	}
	if postKey.Status != "online" {
		t.Errorf("agent row status after wake: want online, got %q", postKey.Status)
	}
}

// TestWake_RefusesNonDormant — operators poking Wake on a healthy
// agent should get a clear error rather than a weird double-spawn.
func TestWake_RefusesNonDormant(t *testing.T) {
	m, _, _ := newDormancyManager(t)
	inst := spawnReady(t, m, "dorm-already-ready")
	if _, err := m.Wake(context.Background(), inst.ID); err == nil {
		t.Error("Wake on ready instance should error")
	}
}

// TestWake_AgentRowMissing — if the agent row was hard-deleted
// (e.g. Purge-then-Wake race), Wake errors out and leaves the
// instance in dormant so operators can fix the DB and retry.
func TestWake_AgentRowMissing(t *testing.T) {
	m, store, _ := newDormancyManager(t)
	inst := spawnReady(t, m, "dorm-gone")
	if err := m.EnterDormancy(context.Background(), inst.ID, "test"); err != nil {
		t.Fatalf("dormancy: %v", err)
	}
	// Delete the row out from under Wake.
	_ = store.DeleteAgent(inst.AgentID)

	if _, err := m.Wake(context.Background(), inst.ID); err == nil {
		t.Error("Wake with missing agent row should error")
	}
	got, _ := m.Get(inst.ID)
	if got.Status != "dormant" {
		t.Errorf("failed Wake must leave instance in dormant, got %q", got.Status)
	}
}

// TestDormancyDetector_ReapsIdleInstance — the auto detector
// dormantises a ready agent whose LastActivityAt has crossed the
// IdleTimeout cutoff.
func TestDormancyDetector_ReapsIdleInstance(t *testing.T) {
	m, _, _ := newDormancyManager(t)
	inst := spawnReady(t, m, "dorm-auto")

	// Push the activity stamp well into the past so the very next
	// detector tick qualifies it. Spawn just stamped it to now.
	m.mu.Lock()
	m.instances[inst.ID].inst.LastActivityAt = time.Now().Add(-1 * time.Second)
	m.mu.Unlock()

	m.StartDormancyDetector(context.Background())
	defer m.StopDormancyDetector()

	// Detector interval is 20ms in the test manager; give it a few
	// ticks to pick up the idle instance + archive + terminate.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := m.Get(inst.ID)
		if got.Status == "dormant" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := m.Get(inst.ID)
	t.Fatalf("expected dormant within 2s, still %q", got.Status)
}

// TestDormancyDetector_SparesActive — a recently-active agent must
// NOT be reaped, even across many ticks.
func TestDormancyDetector_SparesActive(t *testing.T) {
	m, _, _ := newDormancyManager(t)
	inst := spawnReady(t, m, "dorm-active")

	// Don't artificially age the activity stamp — Spawn left it at
	// "now", and the 200ms timeout is well above 20ms tick + sample
	// noise, so it shouldn't be reaped.
	m.StartDormancyDetector(context.Background())
	defer m.StopDormancyDetector()

	// 3 ticks worth — if the detector was wrong this is plenty to
	// see the flip. Keep refreshing the activity stamp to simulate
	// a chatty agent.
	for i := 0; i < 3; i++ {
		time.Sleep(50 * time.Millisecond)
		m.mu.Lock()
		m.instances[inst.ID].inst.LastActivityAt = time.Now()
		m.mu.Unlock()
	}
	got, _ := m.Get(inst.ID)
	if got.Status != "ready" {
		t.Errorf("active agent was reaped: status=%q", got.Status)
	}
}

// TestDormancyDetector_ZeroTimeoutDisables — a manager with
// IdleTimeout=0 should not run the detector at all.
func TestDormancyDetector_ZeroTimeoutDisables(t *testing.T) {
	store := newMemStore()
	m := NewManager(ManagerConfig{
		Root:                  t.TempDir(),
		StartupTimeout:        2 * time.Second,
		ShutdownGrace:         50 * time.Millisecond,
		SkipOpencodeEnvPrep:   true,
		IdleTimeout:           0, // explicitly disabled
		DormancyCheckInterval: 10 * time.Millisecond,
	}, &FakeSpawner{HealthDelay: 10 * time.Millisecond}).
		WithStore(store).
		WithSessionCreator(&fakeSessionCreator{initialID: "ses_disabled"})
	inst, err := m.Spawn(context.Background(), SpawnRequest{Name: "dorm-disabled"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// Even with activity ancient, detector must not act.
	m.mu.Lock()
	m.instances[inst.ID].inst.LastActivityAt = time.Now().Add(-1 * time.Hour)
	m.mu.Unlock()

	m.StartDormancyDetector(context.Background())
	defer m.StopDormancyDetector()
	time.Sleep(80 * time.Millisecond)
	got, _ := m.Get(inst.ID)
	if got.Status != "ready" {
		t.Errorf("zero IdleTimeout should keep detector off, got status=%q", got.Status)
	}
}

// TestBroadcastConsumer_SkipsDormant — a dormant instance must not
// receive injects. The fetch queue must also stay intact so the
// events can be delivered after Wake.
func TestBroadcastConsumer_SkipsDormant(t *testing.T) {
	cons := newFakeBroadcastConsumer()
	inj := &fakeBroadcastInjector{}
	m, _, _ := newDormancyManager(t)
	m = m.WithBroadcastConsumer(cons).WithBroadcastInjector(inj)
	inst := spawnReady(t, m, "dorm-bc")
	if err := m.EnterDormancy(context.Background(), inst.ID, "test"); err != nil {
		t.Fatalf("dormancy: %v", err)
	}
	cons.enqueue(inst.AgentID, []BroadcastEvent{{Type: "TASK_ASSIGN", MessageID: "m1"}})

	m.consumeOnce(context.Background())

	if len(inj.snapshot()) != 0 {
		t.Error("dormant instance should not receive injects")
	}
	// The queue on the fake consumer should still hold its pending
	// batch — we never called FetchEvents for this agent.
	if cons.fetches[inst.AgentID] != 0 {
		t.Errorf("dormant instance should not have had FetchEvents called, got %d", cons.fetches[inst.AgentID])
	}
}

// TestDormancyRoundtrip_PreservesBroadcastChannel — simulate the
// end-to-end operator journey: spawn, idle into dormancy,
// broadcasts buffer up on the queue, wake, broadcasts deliver.
func TestDormancyRoundtrip_PreservesBroadcastChannel(t *testing.T) {
	cons := newFakeBroadcastConsumer()
	inj := &fakeBroadcastInjector{}
	m, _, _ := newDormancyManager(t)
	m = m.WithBroadcastConsumer(cons).WithBroadcastInjector(inj)
	inst := spawnReady(t, m, "dorm-roundtrip-bc")

	// Sleep
	if err := m.EnterDormancy(context.Background(), inst.ID, "roundtrip"); err != nil {
		t.Fatalf("dormancy: %v", err)
	}

	// Enqueue while dormant
	cons.enqueue(inst.AgentID, []BroadcastEvent{{Type: "PING", MessageID: "rt-1"}})
	m.consumeOnce(context.Background())
	if len(inj.snapshot()) != 0 {
		t.Fatal("inject should not fire while dormant")
	}

	// Wake
	if _, err := m.Wake(context.Background(), inst.ID); err != nil {
		t.Fatalf("wake: %v", err)
	}

	// Now consume — the previously-queued event must fire.
	m.consumeOnce(context.Background())
	calls := inj.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 inject after wake, got %d", len(calls))
	}
	if calls[0].Text[:10] != "[broadcast" {
		t.Errorf("rendered text shape wrong: %q", calls[0].Text)
	}
}

// TestGetAgent_MemStore — sanity check that the memStore impl of
// the new interface method behaves like a gorm.First: nil on miss,
// copy on hit.
func TestGetAgent_MemStore(t *testing.T) {
	s := newMemStore()
	if a, err := s.GetAgent("nonexistent"); err != nil || a != nil {
		t.Errorf("expected (nil, nil) on miss, got (%v, %v)", a, err)
	}
	_ = s.CreateAgent(&model.Agent{ID: "a1", Name: "one", AccessKey: "k"})
	got, err := s.GetAgent("a1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got == nil {
		t.Fatal("hit returned nil")
	}
	if got.AccessKey != "k" {
		t.Errorf("access key mismatch, got %q", got.AccessKey)
	}
	// Check that the returned copy is independent (mutating it
	// shouldn't corrupt the stored row).
	got.AccessKey = "changed"
	refetch, _ := s.GetAgent("a1")
	if refetch.AccessKey != "k" {
		t.Error("GetAgent returned aliased pointer — mutation leaked")
	}
}

// TestApplyDefaults_DormancyKnobs — unit-level lock-in: zero idle
// timeout means "detector off" (caller opts in by setting it
// positive); once set, DormancyCheckInterval auto-derives with a
// 5m cap.
func TestApplyDefaults_DormancyKnobs(t *testing.T) {
	var cfg ManagerConfig
	cfg.ApplyDefaults()
	if cfg.IdleTimeout != 0 {
		t.Errorf("IdleTimeout default: want 0 (off), got %s", cfg.IdleTimeout)
	}
	if cfg.DormancyCheckInterval != 0 {
		t.Errorf("DormancyCheckInterval should stay 0 when IdleTimeout is 0, got %s", cfg.DormancyCheckInterval)
	}

	// Once the caller opts in, check interval auto-derives via /6.
	short := ManagerConfig{IdleTimeout: 3 * time.Minute}
	short.ApplyDefaults()
	if short.DormancyCheckInterval != 30*time.Second {
		t.Errorf("3m idle should yield 30s check, got %s", short.DormancyCheckInterval)
	}

	// And the /6 rule is capped at 5m so a 24h IdleTimeout doesn't
	// produce a 4h probe cadence.
	long := ManagerConfig{IdleTimeout: 24 * time.Hour}
	long.ApplyDefaults()
	if long.DormancyCheckInterval != 5*time.Minute {
		t.Errorf("24h idle should cap at 5m check, got %s", long.DormancyCheckInterval)
	}
}

// smoke: ensure fmt import is live (used indirectly via the archive
// fake). Keeps gofmt happy across the test file's evolving body.
var _ = fmt.Sprintf
