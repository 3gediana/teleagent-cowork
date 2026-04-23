// Package agentpool — per-instance metric ring buffers.
//
// The dashboard wants two things the backend already knew about
// but wasn't recording: (1) how a pool agent's token footprint has
// moved over the last ~30 minutes so we can draw a sparkline under
// its card, and (2) a chronological log of lifecycle events so
// operators can see "archived at 14:22, auto-woken at 14:35" without
// tailing server.log.
//
// Design choices:
//
//   - In-process ring buffers. No new DB tables, no Redis writes on
//     the hot path. Platform restart wipes history — fine for a
//     debugging surface; the dashboard will show blanks until the
//     first fresh sample lands.
//   - Fixed capacity per instance. Token samples: 120 slots (context
//     watcher ticks every 30s → 60 minutes of history). Events: 50
//     slots. Bounded so a runaway pool can't blow memory.
//   - Recorded from the existing tick sites: context_watch.go on
//     successful probe, rotateSession / enterDormancy / Wake for
//     events. No extra timers.
//   - Lock granularity is per-ring (not the Manager's big mu) so
//     the metrics read path can't starve the rest of the pool.
//
// Exposed shape (see metrics_snapshot.go → handler):
//
//   TokenSample  { at_ms: int64, tokens: int, session_id: string }
//   PoolEvent    { at_ms: int64, type: string, detail: string }

package agentpool

import (
	"sync"
	"time"
)

// tokenCap and eventCap are the fixed per-instance ring sizes.
// Chosen so both arrays fit comfortably in a couple of KB per
// agent even under worst-case load. If tuned, bump the interval
// comment on context_watch.go too — the sparkline resolution
// depends on the interplay.
const (
	tokenCap = 120
	eventCap = 50
)

// TokenSample is one data point on the token-usage sparkline. at_ms
// is unix milliseconds so the frontend can stamp it into a Date
// without reformatting.
type TokenSample struct {
	AtMS      int64  `json:"at_ms"`
	Tokens    int    `json:"tokens"`
	SessionID string `json:"session_id,omitempty"`
}

// PoolEvent is a log row. `type` is one of:
//
//	"spawn_ready"   — first transition to ready
//	"rotate"        — context watcher rotated the session
//	"dormancy"      — entered dormant state
//	"wake"          — left dormant state
//	"crash"         — subprocess exited unexpectedly
//	"shutdown"      — operator / pool tore it down
//
// `detail` carries a short human string (old→new session ids,
// token count at rotation, crash code etc.). Intentionally not
// structured JSON: the dashboard renders this verbatim.
type PoolEvent struct {
	AtMS   int64  `json:"at_ms"`
	Type   string `json:"type"`
	Detail string `json:"detail,omitempty"`
}

// instanceMetrics is the per-instance ring pair. Held in the
// Manager's metrics map keyed by Instance.ID.
type instanceMetrics struct {
	mu      sync.Mutex
	tokens  []TokenSample
	tokHead int // next write index; ring wraps when tokHead == cap
	tokFull bool
	events  []PoolEvent
	evHead  int
	evFull  bool
}

func newInstanceMetrics() *instanceMetrics {
	return &instanceMetrics{
		tokens: make([]TokenSample, tokenCap),
		events: make([]PoolEvent, eventCap),
	}
}

func (im *instanceMetrics) appendToken(s TokenSample) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.tokens[im.tokHead] = s
	im.tokHead = (im.tokHead + 1) % tokenCap
	if im.tokHead == 0 {
		im.tokFull = true
	}
}

func (im *instanceMetrics) appendEvent(e PoolEvent) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.events[im.evHead] = e
	im.evHead = (im.evHead + 1) % eventCap
	if im.evHead == 0 {
		im.evFull = true
	}
}

// tokenSnapshot returns the samples in chronological (oldest-first)
// order. Safe to call concurrently with appendToken.
func (im *instanceMetrics) tokenSnapshot() []TokenSample {
	im.mu.Lock()
	defer im.mu.Unlock()
	// Walk from the oldest element. When ring isn't full, that's
	// index 0 → tokHead; otherwise it's tokHead → tokHead-1 wrapping.
	if !im.tokFull {
		out := make([]TokenSample, im.tokHead)
		copy(out, im.tokens[:im.tokHead])
		return out
	}
	out := make([]TokenSample, tokenCap)
	copy(out, im.tokens[im.tokHead:])
	copy(out[tokenCap-im.tokHead:], im.tokens[:im.tokHead])
	return out
}

func (im *instanceMetrics) eventSnapshot() []PoolEvent {
	im.mu.Lock()
	defer im.mu.Unlock()
	if !im.evFull {
		out := make([]PoolEvent, im.evHead)
		copy(out, im.events[:im.evHead])
		return out
	}
	out := make([]PoolEvent, eventCap)
	copy(out, im.events[im.evHead:])
	copy(out[eventCap-im.evHead:], im.events[:im.evHead])
	return out
}

// MetricsSnapshot bundles both rings for a single instance. Shape
// matches the JSON wire format of /agentpool/metrics/:id so the
// handler can hand the struct straight to gin.JSON.
type MetricsSnapshot struct {
	Tokens []TokenSample `json:"tokens"`
	Events []PoolEvent   `json:"events"`
}

// MetricsFor returns whatever the manager has recorded for the
// given instance id. Unknown id → empty snapshot, not an error.
// Thread-safe.
func (m *Manager) MetricsFor(instanceID string) MetricsSnapshot {
	m.mu.Lock()
	im, ok := m.metrics[instanceID]
	m.mu.Unlock()
	if !ok {
		return MetricsSnapshot{}
	}
	return MetricsSnapshot{
		Tokens: im.tokenSnapshot(),
		Events: im.eventSnapshot(),
	}
}

// recordTokenReading is called by context_watch.go every time the
// probe returns a fresh value (even if unchanged — the flat line
// is informative too). The instanceMetrics struct is created lazily
// on first write so Manager.instances stays the sole canonical set.
func (m *Manager) recordTokenReading(instanceID, sessionID string, tokens int) {
	m.mu.Lock()
	im, ok := m.metrics[instanceID]
	if !ok {
		im = newInstanceMetrics()
		m.metrics[instanceID] = im
	}
	m.mu.Unlock()
	im.appendToken(TokenSample{
		AtMS:      time.Now().UnixMilli(),
		Tokens:    tokens,
		SessionID: sessionID,
	})
}

// recordEvent is called from the lifecycle transitions. Same lazy-
// allocate story as recordTokenReading.
func (m *Manager) recordEvent(instanceID, eventType, detail string) {
	m.mu.Lock()
	im, ok := m.metrics[instanceID]
	if !ok {
		im = newInstanceMetrics()
		m.metrics[instanceID] = im
	}
	m.mu.Unlock()
	im.appendEvent(PoolEvent{
		AtMS:   time.Now().UnixMilli(),
		Type:   eventType,
		Detail: detail,
	})
}

// purgeMetrics drops an instance's rings when it's hard-removed
// from the pool (Manager.Purge). Leaving them behind would slowly
// leak memory for operators who spawn+purge many test agents.
func (m *Manager) purgeMetrics(instanceID string) {
	m.mu.Lock()
	delete(m.metrics, instanceID)
	m.mu.Unlock()
}
