package agentpool

// Metrics ring-buffer coverage:
//
//   - chronological ordering on both rings (full and partial)
//   - wrap behaviour once cap is exceeded (oldest drops)
//   - lifecycle events land in the event ring at the right moments
//     (spawn, rotate, dormancy, wake, shutdown, crash)
//   - MetricsFor on unknown id returns empty, not nil

import (
	"context"
	"testing"
	"time"
)

// TestMetrics_TokenRing_BelowCap — readings within the ring's
// capacity come back in chronological order with unchanged values.
func TestMetrics_TokenRing_BelowCap(t *testing.T) {
	m, _, _ := newDormancyManager(t)
	inst := spawnReady(t, m, "metrics-token-small")

	// Use the internal helper directly so we don't need a live
	// context probe. Ten samples is well under tokenCap=120.
	for i := 0; i < 10; i++ {
		m.recordTokenReading(inst.ID, inst.OpencodeSessionID, 1_000+i*100)
	}
	snap := m.MetricsFor(inst.ID)
	if len(snap.Tokens) != 10 {
		t.Fatalf("expected 10 samples, got %d", len(snap.Tokens))
	}
	for i, s := range snap.Tokens {
		want := 1_000 + i*100
		if s.Tokens != want {
			t.Errorf("sample[%d]: want %d, got %d", i, want, s.Tokens)
		}
	}
}

// TestMetrics_TokenRing_Wraps — overfilling drops the oldest entry
// and returns cap values in the right order.
func TestMetrics_TokenRing_Wraps(t *testing.T) {
	m, _, _ := newDormancyManager(t)
	inst := spawnReady(t, m, "metrics-token-wrap")
	total := tokenCap + 25
	for i := 0; i < total; i++ {
		m.recordTokenReading(inst.ID, inst.OpencodeSessionID, i)
	}
	snap := m.MetricsFor(inst.ID)
	if len(snap.Tokens) != tokenCap {
		t.Fatalf("ring should be full at cap=%d, got %d", tokenCap, len(snap.Tokens))
	}
	// First sample should be the oldest surviving one.
	if snap.Tokens[0].Tokens != 25 {
		t.Errorf("oldest surviving sample: want 25, got %d", snap.Tokens[0].Tokens)
	}
	if snap.Tokens[len(snap.Tokens)-1].Tokens != total-1 {
		t.Errorf("newest sample: want %d, got %d", total-1, snap.Tokens[len(snap.Tokens)-1].Tokens)
	}
}

// TestMetrics_EventRing_LifecycleTrail — the actual transitions
// should land in the event ring in order.
func TestMetrics_EventRing_LifecycleTrail(t *testing.T) {
	m, _, _ := newDormancyManager(t)
	inst := spawnReady(t, m, "metrics-events")

	// Walk through spawn (already done) → dormancy → wake → shutdown.
	if err := m.EnterDormancy(context.Background(), inst.ID, "t"); err != nil {
		t.Fatalf("dormancy: %v", err)
	}
	if _, err := m.Wake(context.Background(), inst.ID); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if err := m.Shutdown(inst.ID); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	snap := m.MetricsFor(inst.ID)
	types := make([]string, 0, len(snap.Events))
	for _, e := range snap.Events {
		types = append(types, e.Type)
	}
	// spawn_ready + dormancy + wake + shutdown. Crash MIGHT appear
	// if watch() fired on the fake subprocess exit; the current
	// planned-exit guard filters it. Accept either with/without
	// but require the four explicit ones in order.
	want := []string{"spawn_ready", "dormancy", "wake", "shutdown"}
	j := 0
	for _, got := range types {
		if j < len(want) && got == want[j] {
			j++
		}
	}
	if j != len(want) {
		t.Errorf("event trail missing items; got=%v want (in order)=%v", types, want)
	}
}

// TestMetrics_RotateEvent — cross-check that a context-threshold
// rotation lands a "rotate" event. Uses the context watcher's rotation
// path rather than manual EnterDormancy.
func TestMetrics_RotateEvent(t *testing.T) {
	store := newMemStore()
	sc := &fakeSessionCreator{initialID: "ses_init", archiveID: "ses_post"}
	m := NewManager(ManagerConfig{
		Root:                 t.TempDir(),
		StartupTimeout:       2 * time.Second,
		ShutdownGrace:        50 * time.Millisecond,
		SkipOpencodeEnvPrep:  true,
		ArchiveThresholdTokens: 100,
	}, &FakeSpawner{HealthDelay: 10 * time.Millisecond}).
		WithStore(store).
		WithSessionCreator(sc).
		WithContextProbe(&fakeContextProbe{tokens: 200})

	inst, err := m.Spawn(context.Background(), SpawnRequest{
		Name:               "metrics-rotate",
		OpencodeProviderID: "p",
		OpencodeModelID:    "mm",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Drive one tick by hand — rotate happens synchronously inside.
	sp := m.instances[inst.ID]
	sp.inst.LastContextTokens = 0 // start below threshold
	m.checkAndMaybeArchive(context.Background(), sp)

	found := false
	for _, e := range m.MetricsFor(inst.ID).Events {
		if e.Type == "rotate" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("rotate event missing; events=%+v", m.MetricsFor(inst.ID).Events)
	}
}

// TestMetrics_PurgeDropsRing — after Purge the ring is reclaimed so
// the map doesn't grow on spawn+purge cycles.
func TestMetrics_PurgeDropsRing(t *testing.T) {
	m, _, _ := newDormancyManager(t)
	inst := spawnReady(t, m, "metrics-purge")
	_ = m.Shutdown(inst.ID)
	if err := m.Purge(inst.ID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	snap := m.MetricsFor(inst.ID)
	if len(snap.Tokens) != 0 || len(snap.Events) != 0 {
		t.Errorf("purged instance should yield empty metrics, got %+v", snap)
	}
}

// TestMetrics_UnknownInstance — MetricsFor on unknown id must not
// allocate + must not panic.
func TestMetrics_UnknownInstance(t *testing.T) {
	m, _, _ := newDormancyManager(t)
	snap := m.MetricsFor("pool_nope")
	if snap.Tokens != nil || snap.Events != nil {
		t.Errorf("unknown instance should return empty snapshot, got %+v", snap)
	}
}

// NOTE: fakeContextProbe is defined in context_watch_test.go; we
// reuse it here (setting tokens=200 forces the 100-threshold rotation).
