package middleware

// Tests pin the per-agent token bucket contract:
//   - first <burst> requests pass instantly; the next gets 429,
//   - acceptance recovers after refill,
//   - different agents have independent buckets,
//   - "human" sentinel skips the bucket,
//   - missing agent_id falls through (test below uses that fallthrough).

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestRouter builds a gin engine with a stub middleware that
// injects agent_id into c, then chains AgentRateLimit in front of a
// /test handler that returns 200.
func newTestRouter(rps float64, burst int, agentID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if agentID != "" {
			c.Set("agent_id", agentID)
		}
		c.Next()
	})
	r.GET("/test", AgentRateLimit("test", rps, burst), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func TestAgentRateLimit_BurstAllowed(t *testing.T) {
	// burst=3 means three immediate hits should pass; the fourth in
	// the same instant should be 429 because the bucket is empty.
	r := newTestRouter(0.1, 3, "agent_burst")

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("request %d: expected 200, got %d (body=%s)", i, w.Code, w.Body.String())
		}
	}

	// 4th: bucket empty, refill rate is too slow to top up between
	// these calls.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("burst+1: expected 429, got %d (body=%s)", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Errorf("expected Retry-After header on 429, got none")
	}
}

func TestAgentRateLimit_Recovers(t *testing.T) {
	// rps=10 → bucket refills once every 100ms. After bursting we
	// wait long enough for one refill and confirm the next request
	// passes.
	r := newTestRouter(10, 2, "agent_recover")

	// Drain.
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("setup: drain %d failed: %d", i, w.Code)
		}
	}

	// Empty bucket — should 429.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 right after drain, got %d", w.Code)
	}

	// Wait > 110ms to let the bucket refill at least one token.
	time.Sleep(150 * time.Millisecond)

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("after refill window: expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestAgentRateLimit_PerAgentIsolation(t *testing.T) {
	// Each agent has its own bucket. Drain agent A, then agent B
	// should still be allowed at full burst.
	rA := newTestRouter(0.1, 1, "agent_A")
	rB := newTestRouter(0.1, 1, "agent_B")

	// Use the SAME middleware state by switching agent_id on the
	// fly. Trick: the limiter is created fresh per newTestRouter
	// call, so we need a single router serving both agents to check
	// real isolation.
	r := gin.New()
	current := "agent_X"
	r.Use(func(c *gin.Context) {
		c.Set("agent_id", current)
		c.Next()
	})
	r.GET("/iso", AgentRateLimit("iso", 0.1, 1), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{})
	})

	// Drain agent_X.
	current = "agent_X"
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/iso", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("agent_X first: expected 200, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/iso", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("agent_X second: expected 429, got %d", w.Code)
	}

	// Switch to agent_Y — fresh bucket, should pass.
	current = "agent_Y"
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/iso", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("agent_Y first: expected 200, got %d (per-agent isolation broken)", w.Code)
	}

	// (rA, rB unused — keeping them for future cross-router tests
	// that need fresh state)
	_ = rA
	_ = rB
}

func TestAgentRateLimit_HumanIsExempt(t *testing.T) {
	// "human" sentinel — operator dashboard runs as human and
	// doesn't get bucketed. We hammer it well past burst and
	// expect every request to pass.
	r := newTestRouter(0.001, 1, "human")
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("human request %d: expected 200, got %d", i, w.Code)
		}
	}
}

func TestAgentRateLimit_MissingAgentIDFallsThrough(t *testing.T) {
	// If AuthMiddleware never set agent_id (route was wired without
	// auth), AgentRateLimit must fail-open. The global RateLimit
	// middleware in main.go is the safety net for unauth'd traffic.
	r := newTestRouter(0.001, 1, "")
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("missing agent_id request %d: expected 200, got %d", i, w.Code)
		}
	}
}

func TestFormatRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"sub-second rounds up to 1", 100 * time.Millisecond, "1"},
		{"exact 1s", time.Second, "1"},
		{"1.5s rounds up to 2", 1500 * time.Millisecond, "2"},
		{"5s", 5 * time.Second, "5"},
		{"60s", 60 * time.Second, "60"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatRetryAfter(tc.in)
			if got != tc.want {
				t.Errorf("formatRetryAfter(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
