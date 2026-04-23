// Package agentpool — dormancy lifecycle.
//
// Why: pool agents sitting idle still hold an opencode subprocess
// and its loaded model state, which on Windows is ~400 MB RSS per
// agent and one open bun runtime. Once we started running 3-4 of
// them for longer workflows the box was dedicating >1.5 GB to
// chat contexts that no one had spoken to in an hour. Temperate
// reclamation: after IdleTimeout (default 30m) with no inject /
// token-growth / operator poke, the pool:
//
//  1. Calls SessionCreator.CreateArchiveSession one last time,
//     turning the active session into a named "#N" entry so the
//     transcript is permanently addressable in opencode's own
//     session browser. This is the "hard recovery point" the
//     user asked for — they can always re-open it in opencode.
//  2. Terminates the opencode subprocess via the spawner handle.
//     Port is released immediately by the OS; PID is zeroed on
//     the Instance.
//  3. Flips Instance.Status to "dormant" and stamps DormantAt.
//     The Instance row stays in m.instances so its agent id,
//     provider/model pair, skills list and rotation count all
//     survive across the sleep. The Agent DB row moves to
//     status=offline so any task dispatcher sees it as
//     unavailable until a Wake.
//
// Wake is the mirror: re-spawn opencode on a fresh port (the old
// port may have been picked up by something else in the interim),
// re-prime the same .opencode template, create a *new* initial
// session (same title scheme so operators see a linear history).
// The agent id and access key survive, so any existing
// Authorization: Bearer header the operator captured still works.
//
// What we do NOT do in dormancy:
//
//   - Reuse the pre-dormant session id on wake. Opencode persists
//     sessions in its own DB and you could theoretically re-open
//     one, but a re-opened session carries the full pre-dormant
//     transcript and defeats the point of sleeping. Start fresh.
//   - Auto-garbage-collect (Purge) the Instance. The operator's
//     workflow typically expects "my platform-worker-1 is still
//     there" after a lunch break — the dashboard shows it as
//     dormant and the Wake button rehydrates it. If the operator
//     wants to free the slot, that's an explicit Purge.
//   - Restart session archiving as a background task. If the
//     archive session creation fails (network, zod crash, etc.)
//     we skip the archive step and still dormantise — losing an
//     archive entry is strictly better than blocking dormancy.

package agentpool

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"
)

// StartDormancyDetector launches the background goroutine that
// scans for idle-enough-to-dormantise instances. Idempotent; calling
// twice closes the first loop before starting the second. Interval
// is cfg.DormancyCheckInterval; IdleTimeout gates what counts as
// "idle enough". Both zero disables the detector.
func (m *Manager) StartDormancyDetector(ctx context.Context) {
	if m.cfg.IdleTimeout <= 0 || m.cfg.DormancyCheckInterval <= 0 {
		return
	}
	m.mu.Lock()
	if m.dormancyStop != nil {
		close(m.dormancyStop)
	}
	stop := make(chan struct{})
	m.dormancyStop = stop
	m.mu.Unlock()
	go m.runDormancyDetector(ctx, stop)
}

// StopDormancyDetector halts the detector goroutine. Safe to call
// when the detector isn't running. Called by ShutdownAll alongside
// the other long-running loops.
func (m *Manager) StopDormancyDetector() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dormancyStop != nil {
		close(m.dormancyStop)
		m.dormancyStop = nil
	}
}

func (m *Manager) runDormancyDetector(ctx context.Context, stop <-chan struct{}) {
	t := time.NewTicker(m.cfg.DormancyCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-t.C:
			m.dormancyCheckTick(ctx)
		}
	}
}

func (m *Manager) dormancyCheckTick(ctx context.Context) {
	cutoff := time.Now().Add(-m.cfg.IdleTimeout)
	m.mu.Lock()
	candidates := make([]*subprocess, 0)
	for _, sp := range m.instances {
		if sp.inst.Status != "ready" {
			continue
		}
		// Never stamped (shouldn't happen post-Spawn, but defensive):
		// treat as just-active to avoid reaping agents whose Spawn
		// handler didn't get far enough to stamp the field.
		if sp.inst.LastActivityAt.IsZero() {
			continue
		}
		if sp.inst.LastActivityAt.Before(cutoff) {
			candidates = append(candidates, sp)
		}
	}
	m.mu.Unlock()
	for _, sp := range candidates {
		if err := m.enterDormancy(ctx, sp, "idle_timeout"); err != nil {
			log.Printf("[Pool] enter dormancy failed for %s: %v (leaving as-is, next tick retries)", sp.inst.ID, err)
		}
	}
}

// EnterDormancy is the public manual-trigger: operator hits "Sleep"
// on the dashboard before walking away. Same code path as the
// auto-detector — just bypasses the idle-check.
func (m *Manager) EnterDormancy(ctx context.Context, instanceID, reason string) error {
	m.mu.Lock()
	sp, ok := m.instances[instanceID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("pool: instance %s not found", instanceID)
	}
	if sp.inst.Status != "ready" {
		m.mu.Unlock()
		return fmt.Errorf("pool: instance %s is %s — only ready agents can enter dormancy", instanceID, sp.inst.Status)
	}
	m.mu.Unlock()
	return m.enterDormancy(ctx, sp, reason)
}

// enterDormancy drives the ready→dormant transition. Not exported —
// callers go through EnterDormancy (manual) or dormancyCheckTick
// (automatic). Synchronous so the caller can surface errors; the
// detector is tolerant of failures.
func (m *Manager) enterDormancy(ctx context.Context, sp *subprocess, reason string) error {
	instID := sp.inst.ID
	agentID := sp.inst.AgentID
	agentName := sp.inst.AgentName

	// Step 1: create a "stopping point" archive session so the
	// transcript up to this moment is named in opencode's session
	// browser. Best-effort — if opencode is already wedged, skip
	// the archive step rather than blocking the teardown. The
	// existing session id is preserved on the Instance either way;
	// we just won't have the "#N-dormancy" label.
	if m.sessionCreator != nil && sp.inst.OpencodeSessionID != "" {
		// Use a short ctx so a hung opencode doesn't stall the
		// detector. 5s is generous for a local HTTP POST to a
		// live serve; a wedged one will return error well before.
		archiveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		nextRot := sp.inst.ArchiveRotation + 1
		archiveID, err := m.sessionCreator.CreateArchiveSession(archiveCtx, instanceServeURL(sp), agentName, nextRot)
		if err != nil {
			log.Printf("[Pool] dormancy: archive session create failed for %s (continuing): %v", agentID, err)
		} else {
			log.Printf("[Pool] dormancy: archived %s -> %s (rotation=%d reason=%s)", sp.inst.OpencodeSessionID, archiveID, nextRot, reason)
			m.mu.Lock()
			sp.inst.OpencodeSessionID = archiveID
			sp.inst.ArchiveRotation = nextRot
			sp.inst.LastContextTokens = 0
			m.mu.Unlock()
			if m.archiveNotifier != nil {
				// Emits POOL_SESSION_ARCHIVE like a normal rotation
				// but with reason=dormancy so observers can tell
				// the difference on replay.
				m.archiveNotifier.NotifyArchive(agentID, sp.inst.OpencodeSessionID, archiveID, sp.inst.LastContextTokens, "dormancy_"+reason)
			}
		}
	}

	// Step 2: transition status and tear the subprocess down.
	// Status flip happens BEFORE Terminate so the watch() goroutine
	// doesn't see the exit as a crash.
	m.mu.Lock()
	sp.inst.Status = "dormant"
	sp.inst.DormantAt = time.Now()
	handle := sp.handle
	oldPID := sp.inst.PID
	sp.inst.PID = 0
	oldPort := sp.inst.Port
	sp.inst.Port = 0
	// Park the old handle so any late signals from the detector
	// map go nowhere — Terminate below owns the lifecycle now.
	sp.handle = nil
	m.mu.Unlock()

	// Record dormancy BEFORE Terminate so the event ring shows the
	// transition at the operator-visible moment (status flip) rather
	// than when the subprocess actually stops. Terminate holds up
	// to ShutdownGrace (10s by default) waiting for a clean exit,
	// which is long enough that the consumer's dormant-scan can
	// see the new state and fire auto-wake before the dormancy
	// event ever lands in the ring.
	m.recordEvent(instID, "dormancy", fmt.Sprintf("reason=%s pid=%d port=%d", reason, oldPID, oldPort))

	if handle != nil {
		handle.Terminate(m.cfg.ShutdownGrace)
	}

	// DB row: flip to offline so agent dispatchers see it as
	// unavailable. Session id stays (the archive we just created)
	// so operators reviewing dialogue history don't lose context.
	_ = m.store.UpdateAgent(agentID, map[string]any{"status": "offline"})

	log.Printf("[Pool] dormant: instance=%s agent=%s (was pid=%d port=%d, reason=%s)", instID, agentID, oldPID, oldPort, reason)
	return nil
}

// Wake revives a dormant instance. Mirror of Spawn but keyed on the
// existing Instance — the agent id, access key, provider/model and
// skills are preserved so the agent retains its identity across the
// sleep. Only the opencode subprocess + session are fresh.
//
// Returns the updated Instance once it's gone healthy. Errors leave
// the status as "dormant" (safe to retry) unless the spawner failed
// mid-flight, in which case the status is "crashed" and the
// operator should Shutdown+Purge manually.
func (m *Manager) Wake(ctx context.Context, instanceID string) (*Instance, error) {
	m.mu.Lock()
	sp, ok := m.instances[instanceID]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("pool: instance %s not found", instanceID)
	}
	if sp.inst.Status != "dormant" {
		m.mu.Unlock()
		return nil, fmt.Errorf("pool: instance %s is %s — only dormant agents can wake", instanceID, sp.inst.Status)
	}
	sp.inst.Status = "waking"
	// Capture identity bits while we hold the lock so the outside
	// can't observe a half-stated instance on re-spawn.
	agentID := sp.inst.AgentID
	projectID := sp.inst.ProjectID
	providerID := sp.inst.OpencodeProviderID
	modelID := sp.inst.OpencodeModelID
	agentName := sp.inst.AgentName
	workDir := sp.inst.WorkingDir
	m.mu.Unlock()

	// Need a fresh port; old one is gone and may well be claimed.
	port, err := m.pickPort()
	if err != nil {
		m.revertWakeFailure(sp, fmt.Sprintf("pick port: %v", err))
		return nil, fmt.Errorf("pool: pick port for wake: %w", err)
	}

	// Re-prime .opencode in case the template directory is now on a
	// different pool root (operator moved DataDir, etc.). Fast on a
	// warm filesystem; no-op when the marker file is already there.
	if !m.cfg.SkipOpencodeEnvPrep && workDir != "" {
		if err := prepareOpencodeDir(workDir, m.cfg.Root); err != nil {
			m.revertWakeFailure(sp, fmt.Sprintf("prepare .opencode: %v", err))
			return nil, fmt.Errorf("pool: prepare .opencode for wake: %w", err)
		}
	} else if workDir == "" {
		// Shouldn't happen on a properly-Spawn'd instance, but be
		// defensive: synthesise a workdir from the instance id.
		workDir = filepath.Join(m.cfg.Root, instanceID)
	}

	// Pull the agent's access key from the store so the subprocess
	// has the same identity it had pre-dormancy. Going through
	// Store (rather than model.DB directly) keeps this testable
	// without a live MySQL.
	agentRow, err := m.store.GetAgent(agentID)
	if err != nil {
		m.revertWakeFailure(sp, fmt.Sprintf("lookup agent: %v", err))
		return nil, fmt.Errorf("pool: lookup agent row on wake: %w", err)
	}
	if agentRow == nil {
		m.revertWakeFailure(sp, "agent row missing")
		return nil, fmt.Errorf("pool: agent row for %s not found on wake (was it purged?)", agentID)
	}
	accessKey := agentRow.AccessKey

	serveURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	spawnReq := SpawnerRequest{
		WorkingDir: workDir,
		Port:       port,
		Env: map[string]string{
			"A3C_PLATFORM_URL":         m.cfg.PlatformURL,
			"A3C_ACCESS_KEY":           accessKey,
			"A3C_PROJECT_ID":           projectID,
			"A3C_AGENT_ID":             agentID,
			"A3C_INSTANCE_ID":          instanceID,
			"A3C_WORK_DIR":             workDir,
			"A3C_OPENCODE_PROVIDER_ID": providerID,
			"A3C_OPENCODE_MODEL_ID":    modelID,
			"OPENCODE_SERVE_URL":       serveURL,
		},
		Command: m.cfg.Command,
		Args:    m.cfg.Args,
	}
	handle, err := m.spawner.Spawn(ctx, spawnReq)
	if err != nil {
		m.revertWakeFailure(sp, fmt.Sprintf("spawn: %v", err))
		return nil, fmt.Errorf("pool: spawn subprocess on wake: %w", err)
	}

	// Bind the new handle + port onto the same Instance so any
	// operator who captured inst.ID sees the same one reappear.
	m.mu.Lock()
	sp.inst.Port = port
	sp.inst.PID = handle.PID()
	sp.handle = handle
	sp.stopChan = make(chan struct{})
	m.mu.Unlock()

	// Wait for /global/health. Same timeout as Spawn.
	if !handle.WaitHealthy(ctx, m.cfg.StartupTimeout) {
		handle.Terminate(m.cfg.ShutdownGrace)
		m.revertWakeFailure(sp, "health check never landed")
		return nil, fmt.Errorf("pool: wake never became healthy for %s", instanceID)
	}

	// Fresh session — we intentionally don't reuse the pre-dormant
	// session id (see package doc). Rotation count carries across
	// so the titles keep climbing ("pool:name#5" after the 4 that
	// happened before sleeping).
	var newSession string
	if m.sessionCreator != nil {
		nextRot := sp.inst.ArchiveRotation + 1
		sid, err := m.sessionCreator.CreateArchiveSession(ctx, serveURL, agentName, nextRot)
		if err != nil {
			log.Printf("[Pool] wake: create session failed for %s (agent is up but unbound): %v", agentID, err)
		} else {
			newSession = sid
		}
	}

	now := time.Now()
	m.mu.Lock()
	sp.inst.Status = "ready"
	if newSession != "" {
		sp.inst.OpencodeSessionID = newSession
		sp.inst.ArchiveRotation++
	}
	sp.inst.LastActivityAt = now
	// DormantAt stays put — it's a historical record now. Dashboard
	// can compute "was asleep for N" by diffing against the current
	// StartedAt if it wants; simpler to just overwrite StartedAt so
	// the status panel shows the refreshed clock.
	sp.inst.StartedAt = now
	sp.inst.LastContextTokens = 0
	m.mu.Unlock()

	updates := map[string]any{
		"status":         "online",
		"last_heartbeat": &now,
	}
	if newSession != "" {
		updates["session_id"] = newSession
	}
	_ = m.store.UpdateAgent(agentID, updates)

	go m.watch(sp)

	log.Printf("[Pool] woke instance=%s agent=%s on port=%d session=%s", instanceID, agentID, port, newSession)
	m.recordEvent(instanceID, "wake", fmt.Sprintf("port=%d session=%s", port, newSession))
	return &sp.inst, nil
}

// revertWakeFailure flips the status back to "dormant" on a failed
// wake attempt so the operator can retry. Held-lock variant to
// avoid a second wave of corruption if the failure happens mid-
// status-transition.
func (m *Manager) revertWakeFailure(sp *subprocess, cause string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sp.inst.Status = "dormant"
	sp.inst.LastError = cause
}

// EnsureReadyByAgentID is the auto-wake entry point. Caller passes
// an agent id; if the pool manages that agent and it's dormant, the
// manager kicks off a Wake in the background and returns immediately.
// Already-ready / unknown / non-dormant agents are no-ops.
//
// Async is important: the caller is typically
// service.BroadcastDirected, which is downstream of RPC handlers and
// workflow timers — blocking it on a full opencode spawn (seconds)
// would ripple into user-facing latency and starve task claim loops.
// The broadcast itself lands in Redis regardless; the pool's own
// broadcast consumer will pick it up once wake flips status back
// to ready.
//
// Returns a bool indicating whether a wake was actually kicked off,
// so callers can log/observe without having to re-query the pool.
func (m *Manager) EnsureReadyByAgentID(agentID string) bool {
	m.mu.Lock()
	var target *subprocess
	for _, sp := range m.instances {
		if sp.inst.AgentID == agentID {
			target = sp
			break
		}
	}
	m.mu.Unlock()
	if target == nil {
		return false // not a pool-hosted agent (external / already purged)
	}
	if target.inst.Status != "dormant" {
		return false // already awake, or mid-something-else
	}
	instID := target.inst.ID
	go func() {
		if _, err := m.Wake(context.Background(), instID); err != nil {
			log.Printf("[Pool] auto-wake for %s (%s) failed: %v", agentID, instID, err)
		} else {
			log.Printf("[Pool] auto-woke %s (%s) on broadcast arrival", agentID, instID)
		}
	}()
	return true
}

