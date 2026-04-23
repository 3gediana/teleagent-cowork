package agentpool

// Context watcher — the goroutine that polls each pool agent's
// current opencode session for its accumulated token footprint and
// rotates to a fresh session before opencode's own 80% auto-compact
// kicks in. See the user-facing rationale in docs/archive-design.md
// (to be written) and the bug that made this necessary in
// internal/agentpool/opencode_env.go (zod v4 incompatibility).
//
// High-level contract:
//
//   StartContextWatcher(ctx):
//     - no-op if ContextWatchInterval == 0 or ContextProbe is nil
//     - spins up one goroutine that loops on a ticker
//     - each tick: iterate ready instances, probe context, compare
//       against ArchiveThresholdTokens, rotate when over
//
//   Stop (manager.Shutdown / Purge / ShutdownAll):
//     - closes watchStop; the goroutine exits next tick or sooner
//
// Rotation is best-effort. Failure to probe or create a replacement
// session just gets logged — the next tick retries. We never leave
// an agent pointing at a session whose id we can't resolve, because
// the Instance keeps the pre-rotation id until the replacement
// lands successfully.

import (
	"context"
	"log"
	"strconv"
	"time"
)

// StartContextWatcher launches the background poller. Safe to call
// multiple times; subsequent calls are no-ops until the first one's
// watcher exits (tracked via m.watchStop).
//
// ctx is the lifetime ceiling — if the caller cancels it, the
// watcher exits too. Useful for tying the watcher to the server's
// shutdown signal.
func (m *Manager) StartContextWatcher(ctx context.Context) {
	if m.cfg.ContextWatchInterval <= 0 {
		return
	}
	if m.contextProbe == nil {
		log.Printf("[Pool] context watcher skipped: no ContextProbe wired")
		return
	}

	m.mu.Lock()
	if m.watchStop != nil {
		m.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	m.watchStop = stop
	m.mu.Unlock()

	go m.runContextWatcher(ctx, stop)
}

// stopContextWatcher closes the stop channel iff one is running.
// Called from Manager.Shutdown / ShutdownAll; safe to call when
// the watcher isn't running.
func (m *Manager) stopContextWatcher() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watchStop != nil {
		close(m.watchStop)
		m.watchStop = nil
	}
}

func (m *Manager) runContextWatcher(ctx context.Context, stop <-chan struct{}) {
	ticker := time.NewTicker(m.cfg.ContextWatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			m.contextWatchTick(ctx)
		}
	}
}

// contextWatchTick runs once per interval: snapshot the ready
// instances under the lock, then probe + rotate each one without
// holding the lock. Individual failures are logged and skipped so
// one flaky agent doesn't starve the others.
func (m *Manager) contextWatchTick(ctx context.Context) {
	m.mu.Lock()
	snapshot := make([]*subprocess, 0, len(m.instances))
	for _, sp := range m.instances {
		if sp.inst.Status != "ready" {
			continue
		}
		if sp.inst.OpencodeSessionID == "" {
			continue
		}
		snapshot = append(snapshot, sp)
	}
	m.mu.Unlock()

	for _, sp := range snapshot {
		m.checkAndMaybeArchive(ctx, sp)
	}
}

// checkAndMaybeArchive probes one agent's session, updates the
// cached token count, and rotates if the threshold is hit. The
// separation keeps the tick loop readable; it's also the unit test
// seam for archive logic.
func (m *Manager) checkAndMaybeArchive(ctx context.Context, sp *subprocess) {
	serveURL := instanceServeURL(sp)
	sessionID := sp.inst.OpencodeSessionID
	agentID := sp.inst.AgentID

	tokens, err := m.contextProbe.ContextSize(ctx, serveURL, sessionID)
	if err != nil {
		// Transient errors (serve restart, network blip) are
		// expected; suppress them at debug level so the log
		// doesn't drown on a failing agent.
		log.Printf("[Pool] context probe failed agent=%s session=%s: %v", agentID, sessionID, err)
		return
	}

	// Cache the reading so /agentpool/list can render a gauge
	// without re-hitting opencode from the browser.
	m.mu.Lock()
	sp.inst.LastContextTokens = tokens
	m.mu.Unlock()

	if tokens < m.cfg.ArchiveThresholdTokens {
		return
	}

	// Over the line — rotate. We need a SessionCreator to do so;
	// if the operator only wired a ContextProbe without a creator
	// we can still report the number but we can't act on it.
	if m.sessionCreator == nil {
		log.Printf("[Pool] agent=%s session=%s at %d tokens — would archive but no SessionCreator wired", agentID, sessionID, tokens)
		return
	}

	if err := m.rotateSession(ctx, sp, tokens, "context_exceeded"); err != nil {
		log.Printf("[Pool] rotate session failed agent=%s: %v — will retry next tick", agentID, err)
	}
}

// rotateSession creates a replacement opencode session, updates the
// instance + DB row, and fires the archive notification. On any
// step failure we bail out WITHOUT swapping the id — we'd rather
// retry on the next tick than strand the agent on a freshly-created
// session that the MCP poller hasn't been told about.
func (m *Manager) rotateSession(ctx context.Context, sp *subprocess, tokens int, reason string) error {
	serveURL := instanceServeURL(sp)
	agentName := sp.inst.AgentName
	agentID := sp.inst.AgentID
	oldSessionID := sp.inst.OpencodeSessionID
	nextRotation := sp.inst.ArchiveRotation + 1

	newID, err := m.sessionCreator.CreateArchiveSession(ctx, serveURL, agentName, nextRotation)
	if err != nil {
		return err
	}

	m.mu.Lock()
	sp.inst.OpencodeSessionID = newID
	sp.inst.ArchiveRotation = nextRotation
	// Reset the cached token reading so the next tick reports the
	// new session's size (0 until the first assistant reply lands).
	sp.inst.LastContextTokens = 0
	m.mu.Unlock()

	// Persist the new id to the agent row. Errors here are logged
	// but not fatal — the DB is a cache for the dashboard; the
	// runtime state lives in the Manager anyway.
	if err := m.store.UpdateAgent(agentID, map[string]any{"session_id": newID}); err != nil {
		log.Printf("[Pool] rotate session: agent %s DB update failed: %v (state in-memory is still correct)", agentID, err)
	}

	// Notify the MCP poller last — the new id has to already be on
	// the instance so any immediate "which session am I on?" query
	// from the poller sees the fresh value. Missing notifier is OK;
	// tests that only observe state skip it deliberately.
	if m.archiveNotifier != nil {
		m.archiveNotifier.NotifyArchive(agentID, oldSessionID, newID, tokens, reason)
	}

	log.Printf("[Pool] archived session agent=%s %s -> %s (tokens=%d reason=%s rotation=%d)",
		agentID, oldSessionID, newID, tokens, reason, nextRotation)
	return nil
}

// instanceServeURL reconstructs the local opencode serve URL for an
// instance. We don't store it on the Instance struct because the
// port is already there and the hostname is always 127.0.0.1 for
// pool agents — deriving it on demand avoids a second source of
// truth that could drift.
func instanceServeURL(sp *subprocess) string {
	if sp.inst.Port == 0 {
		return ""
	}
	return httpLocalURL(sp.inst.Port)
}

// httpLocalURL builds "http://127.0.0.1:<port>". Kept as a tiny
// helper so the loopback host has exactly one spelling in the
// package; if it ever needs to become configurable, grep finds this
// and the instanceServeURL call site above.
func httpLocalURL(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port)
}
