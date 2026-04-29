package middleware

// Per-agent token-bucket rate limit.
//
// The existing RateLimitMiddleware in recovery.go is a *global* 100-rps
// cap — it stops a hot loop from saturating the box, but it doesn't
// stop one misbehaving MCP client from monopolising the entire LLM
// budget. This middleware fixes that gap for the small set of routes
// that actually spawn LLM work (or that an attacker with a stolen
// access_key could rack up cost on).
//
// Design notes:
//   - One token bucket per agent_id, lazy-allocated. agent_id comes
//     from AuthMiddleware (c.Get("agent_id")), so this MUST be applied
//     AFTER AuthMiddleware. Anonymous / unauth requests fall through
//     uncounted; that's intentional — they're already 401-rejected
//     by AuthMiddleware before reaching here.
//   - golang.org/x/time/rate.Limiter is the standard implementation;
//     it gives us steady-state rate + a burst window without us
//     having to write our own ring buffer. Its Allow() is lock-free
//     internally, so we can safely call it under our outer lock
//     reservation.
//   - Each route group gets to pick its own (rps, burst) so we can
//     be lenient on /poll-style chatty paths and strict on
//     /chief/chat-style expensive paths.
//   - Buckets are kept in memory only; a process restart wipes the
//     state. That's fine — the worst case is "an attacker who waits
//     for restart" which gives them another <burst> requests, not
//     ongoing access.
//
// Janitor:
//   - We never unbounded-grow the map: a goroutine sweeps idle
//     buckets every 10 minutes. An agent that hasn't been seen in
//     60 minutes gets its bucket dropped; if they come back later,
//     they get a fresh bucket (full burst available). This bounds
//     memory at "active agents in the last hour", which for any
//     real deployment is a small number.

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// AgentRateLimit returns a middleware that gates requests by per-agent
// token bucket. rps = sustained allowed rate (requests per second);
// burst = peak allowed in a single tick. A typical "expensive LLM
// route" tuning is rps=0.5 (one every 2 seconds), burst=4 (small
// retry budget).
//
// The label is included in 429 responses so dashboards can tell which
// route group rejected (e.g. "chief", "refinery").
func AgentRateLimit(label string, rps float64, burst int) gin.HandlerFunc {
	state := newAgentLimiterState(rps, burst)
	state.startJanitor()

	return func(c *gin.Context) {
		idRaw, ok := c.Get("agent_id")
		if !ok {
			// Should be unreachable behind AuthMiddleware, but if
			// someone wires this in front of an open route, fail open
			// rather than 500 (the global RateLimitMiddleware still
			// applies as a coarse safety net).
			c.Next()
			return
		}
		agentID, _ := idRaw.(string)
		if agentID == "" || agentID == "human" {
			// "human" is a sentinel for the operator dashboard which
			// is human-driven and self-rate-limited. Don't bucket it.
			c.Next()
			return
		}

		lim := state.limiter(agentID)
		if !lim.Allow() {
			retryAfter := time.Duration(float64(time.Second) / state.rps)
			c.Header("Retry-After", formatRetryAfter(retryAfter))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"success": false,
				"error": gin.H{
					"code":    "RATE_LIMIT",
					"message": "rate limit exceeded for " + label + " (per-agent bucket)",
					"label":   label,
					"retry_after_seconds": int(retryAfter.Seconds() + 0.5),
				},
			})
			return
		}
		c.Next()
	}
}

// agentLimiterState owns the per-agent bucket map and the janitor
// that prunes idle entries.
type agentLimiterState struct {
	rps   float64
	burst int

	mu       sync.Mutex
	buckets  map[string]*agentBucket
	lastSeen map[string]time.Time
}

type agentBucket struct {
	limiter *rate.Limiter
}

func newAgentLimiterState(rps float64, burst int) *agentLimiterState {
	if rps <= 0 {
		rps = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &agentLimiterState{
		rps:      rps,
		burst:    burst,
		buckets:  make(map[string]*agentBucket),
		lastSeen: make(map[string]time.Time),
	}
}

func (s *agentLimiterState) limiter(agentID string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buckets[agentID]
	if !ok {
		b = &agentBucket{
			limiter: rate.NewLimiter(rate.Limit(s.rps), s.burst),
		}
		s.buckets[agentID] = b
	}
	s.lastSeen[agentID] = time.Now()
	return b.limiter
}

// startJanitor kicks off a goroutine that drops buckets unused in the
// last 60 minutes. We start it once per AgentRateLimit call (one per
// route group) which is fine — they each manage their own state.
//
// Janitor intentionally has no Stop method: the platform's main
// goroutine outlives all of these, and on shutdown the OS reclaims.
// If we ever wire this into a unit test that needs deterministic
// cleanup, we'd want to add one then.
func (s *agentLimiterState) startJanitor() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.sweep()
		}
	}()
}

func (s *agentLimiterState) sweep() {
	cutoff := time.Now().Add(-60 * time.Minute)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, last := range s.lastSeen {
		if last.Before(cutoff) {
			delete(s.buckets, id)
			delete(s.lastSeen, id)
		}
	}
}

// formatRetryAfter writes a value suitable for the HTTP Retry-After
// header. We round up to the next second since the header doesn't
// take fractional seconds.
func formatRetryAfter(d time.Duration) string {
	if d < time.Second {
		return "1"
	}
	secs := int((d + 999*time.Millisecond) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return itoa(secs)
}

func itoa(n int) string {
	// Tiny manual itoa to avoid pulling in strconv just for the header.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
