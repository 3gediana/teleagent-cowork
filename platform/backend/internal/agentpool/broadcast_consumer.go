// Background: platform-hosted pool agents originally relied on an
// external MCP client (node client/mcp/dist/index.js) to poll
// Redis for directed broadcasts and inject them into the agent's
// opencode session. That made sense for *external* agents running
// opencode on a different box — the MCP bridged the platform Redis
// and the operator's local opencode. But for agents the platform
// itself spawned, dropping a node process into the pool just so
// it could poll Redis and HTTP-post back to a serve we also own
// was pure ceremony. The backend is already sitting on both ends
// of the wire.
//
// This file is the internal consumer. It takes the same directed
// broadcast shape `BroadcastDirected` produces
// (internal/service/broadcast.go), pulls pending events for each
// ready pool agent, and hands them to a BroadcastInjector that
// speaks opencode's HTTP API. POOL_SESSION_ARCHIVE events are
// filtered out because the pool is the one that generated them
// (the watcher already updated Instance.OpencodeSessionID in-
// process; re-injecting a "you rotated" message would just noise
// up the transcript).

package agentpool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// BroadcastConsumer pops pending directed broadcasts for an agent
// from whatever queue the platform uses. Production wires in a
// Redis-backed impl (see internal/service/pool_broadcast_consumer.go);
// tests pass a fake that returns scripted events without Redis.
type BroadcastConsumer interface {
	// FetchEvents atomically drains and returns all pending events
	// for the given agent. Returning ([], nil) means "queue empty,
	// try again later" — no error. Errors should be returned only
	// for infrastructure failures (Redis unreachable etc.).
	FetchEvents(ctx context.Context, agentID string) ([]BroadcastEvent, error)
}

// BroadcastEvent is the decoded shape of one directed broadcast,
// matching the envelope BroadcastDirected writes to Redis.
type BroadcastEvent struct {
	Type      string                 `json:"type"`
	MessageID string                 `json:"messageID"`
	Payload   map[string]interface{} `json:"payload"`
}

// BroadcastInjector posts a user turn into an opencode session so
// the pool agent's next reply sees the broadcast content. The
// provider/model pair is what opencode uses to route the prompt
// — both must be non-empty or opencode silently returns a reply
// with parts=0.
type BroadcastInjector interface {
	InjectMessage(ctx context.Context, serveURL, sessionID, text, providerID, modelID string) error
}

// WithBroadcastConsumer installs the source that produces pending
// directed broadcasts for each pool agent. Optional — if unset, the
// consumer loop is a no-op. Returns the manager for chaining.
func (m *Manager) WithBroadcastConsumer(c BroadcastConsumer) *Manager {
	m.broadcastConsumer = c
	return m
}

// WithBroadcastInjector installs the sink that turns a decoded
// broadcast event into a real message posted on the agent's
// opencode session. Optional — without it, events are dequeued
// but dropped (useful for observability-only tests).
func (m *Manager) WithBroadcastInjector(i BroadcastInjector) *Manager {
	m.broadcastInjector = i
	return m
}

// StartBroadcastConsumer launches the background goroutine that
// drives the broadcast consumer loop. Idempotent: calling it twice
// stops the first loop before starting the second so configuration
// changes at runtime don't leak goroutines. Passing interval <= 0
// falls back to 2s which is snappier than the 30s context watcher
// (operators notice "my task didn't start" much faster than they
// notice "token usage crept up").
func (m *Manager) StartBroadcastConsumer(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	m.mu.Lock()
	if m.broadcastStop != nil {
		close(m.broadcastStop)
	}
	stop := make(chan struct{})
	m.broadcastStop = stop
	m.mu.Unlock()

	go m.broadcastLoop(ctx, interval, stop)
}

// StopBroadcastConsumer halts the background loop. Called by
// ShutdownAll; safe to call standalone too.
func (m *Manager) StopBroadcastConsumer() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.broadcastStop != nil {
		close(m.broadcastStop)
		m.broadcastStop = nil
	}
}

func (m *Manager) broadcastLoop(ctx context.Context, interval time.Duration, stop <-chan struct{}) {
	// One tick per poll. We tolerate per-tick failures; the next
	// tick will retry. The only fatal exit is ctx.Done or a close
	// on stop.
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-t.C:
			m.consumeOnce(ctx)
		}
	}
}

// consumeOnce snapshots the ready-pool set under the lock, then
// drains broadcasts for each agent without holding the lock.
// Drains happen in parallel (one goroutine per agent) so a slow
// opencode inject on one agent can't delay broadcasts to others.
// All parallelism is bounded by len(instances) which is small.
func (m *Manager) consumeOnce(ctx context.Context) {
	if m.broadcastConsumer == nil {
		return
	}

	type target struct {
		agentID     string
		sessionID   string
		port        int
		providerID  string
		modelID     string
	}

	m.mu.Lock()
	targets := make([]target, 0, len(m.instances))
	for _, sp := range m.instances {
		if sp.inst.Status != "ready" {
			continue
		}
		if sp.inst.OpencodeSessionID == "" {
			// No session yet → nowhere to inject. Broadcasts for
			// this agent will sit in the queue until a session
			// binds (spawn retry or the next round of auth).
			continue
		}
		targets = append(targets, target{
			agentID:    sp.inst.AgentID,
			sessionID:  sp.inst.OpencodeSessionID,
			port:       sp.inst.Port,
			providerID: sp.inst.OpencodeProviderID,
			modelID:    sp.inst.OpencodeModelID,
		})
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, tg := range targets {
		wg.Add(1)
		go func(tg target) {
			defer wg.Done()
			m.consumeAgent(ctx, tg.agentID, tg.sessionID, tg.port, tg.providerID, tg.modelID)
		}(tg)
	}
	wg.Wait()
}

func (m *Manager) consumeAgent(ctx context.Context, agentID, sessionID string, port int, providerID, modelID string) {
	events, err := m.broadcastConsumer.FetchEvents(ctx, agentID)
	if err != nil {
		log.Printf("[Pool] broadcast fetch failed for %s: %v", agentID, err)
		return
	}
	if len(events) == 0 {
		return
	}
	log.Printf("[Pool] drained %d events for %s", len(events), agentID)

	serveURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Re-read session id under lock for every inject — the context
	// watcher could have rotated the session between the snapshot
	// above and here. Without this, the first post-rotation event
	// would hit the archived session which is closed.
	for _, e := range events {
		// POOL_SESSION_ARCHIVE is pool-internal plumbing. The
		// watcher already updated Instance.OpencodeSessionID when
		// it produced this event; we never want it to land in the
		// LLM's transcript (it would confuse the agent about
		// "which session am I on" since opencode has already
		// created the new one for us).
		if e.Type == "POOL_SESSION_ARCHIVE" {
			continue
		}

		// Refresh the session id. Cheap to do under lock because
		// we're already iterating serially per agent.
		m.mu.Lock()
		sp, ok := m.instances[instanceIDFromAgent(m.instances, agentID)]
		m.mu.Unlock()
		currentSession := sessionID
		if ok && sp.inst.OpencodeSessionID != "" {
			currentSession = sp.inst.OpencodeSessionID
		}

		if m.broadcastInjector == nil {
			continue
		}
		text := RenderBroadcastText(e)
		if err := m.broadcastInjector.InjectMessage(ctx, serveURL, currentSession, text, providerID, modelID); err != nil {
			log.Printf("[Pool] inject %s to %s (%s) failed: %v", e.Type, agentID, currentSession, err)
		} else {
			log.Printf("[Pool] injected %s into %s (session=%s)", e.Type, agentID, currentSession)
		}
	}
}

// instanceIDFromAgent walks the pool map to find the subprocess
// hosting the given agentID. Linear scan, fine at pool sizes
// we expect (< 50). Returns "" if not found, which the caller
// treats as "use the snapshot id".
func instanceIDFromAgent(instances map[string]*subprocess, agentID string) string {
	for id, sp := range instances {
		if sp.inst.AgentID == agentID {
			return id
		}
	}
	return ""
}

// RenderBroadcastText formats a broadcast event into the plain
// text we feed opencode. Exported so tests can assert on the
// exact string the LLM sees.
//
// Shape:
//
//	[broadcast/TYPE id=MSGID] {json payload}
//
// The bracket prefix makes it easy for the agent's prompt rules
// to recognise "this is a platform-injected turn, not a human
// user". JSON payload is compact — opencode + the LLM tokenise
// whitespace poorly, and we want tiny broadcasts to stay tiny.
func RenderBroadcastText(e BroadcastEvent) string {
	payload := "{}"
	if e.Payload != nil {
		if b, err := json.Marshal(e.Payload); err == nil {
			payload = string(b)
		}
	}
	id := e.MessageID
	if id == "" {
		id = "-"
	}
	return fmt.Sprintf("[broadcast/%s id=%s] %s", e.Type, id, payload)
}
