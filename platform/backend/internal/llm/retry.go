package llm

// Retry policy for the initial chat-completion POST (pre-stream).
//
// We deliberately do NOT retry mid-stream failures: once any tokens
// have been delivered to the caller, replaying would duplicate them
// (and tool-use ids would collide). The loop caller handles mid-stream
// failures at the conversation level (treat as an abort + re-ask).

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RetryPolicy is deliberately minimal. The knobs that matter:
//   MaxAttempts — total tries including the first (1 = no retry).
//   BaseDelay / MaxDelay — exponential backoff bounds.
//   JitterFrac — [0, 1] fraction of each delay randomised to break
//                thundering-herd patterns when many sessions retry at
//                the same provider 429.
//   RespectRetryAfter — if true, prefer the server's Retry-After over
//                       our computed backoff. Default on.
type RetryPolicy struct {
	MaxAttempts       int
	BaseDelay         time.Duration
	MaxDelay          time.Duration
	JitterFrac        float64
	RespectRetryAfter bool
}

// DefaultRetryPolicy is the one providers use unless overridden.
// 3 attempts × (1s, 2s, 4s) with ±30% jitter — matches the opencode
// behaviour we're replacing, slightly tuned down from its 5 attempts
// since we now retry at the agent-loop level too.
var DefaultRetryPolicy = RetryPolicy{
	MaxAttempts:       3,
	BaseDelay:         1 * time.Second,
	MaxDelay:          30 * time.Second,
	JitterFrac:        0.3,
	RespectRetryAfter: true,
}

// DoWithRetry wraps a request-sending closure with the policy. `send`
// must return the raw *http.Response (not yet drained) so the retry
// decision can peek at the status; on retry we drain + close before
// the next attempt.
//
// The closure receives the attempt number (1-based) so it can stamp
// telemetry / request IDs differently per attempt.
func DoWithRetry(ctx context.Context, pol RetryPolicy, send func(attempt int) (*http.Response, error)) (*http.Response, error) {
	if pol.MaxAttempts < 1 {
		pol.MaxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= pol.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := send(attempt)
		if err == nil && !shouldRetryStatus(resp.StatusCode) {
			return resp, nil
		}

		// Decide whether to retry. For network errors (err != nil) we
		// retry only on transient causes; for HTTP responses we retry
		// on 408/429/5xx.
		retryable := err != nil && isTransientNetErr(err)
		if err == nil {
			retryable = shouldRetryStatus(resp.StatusCode)
		}
		if !retryable || attempt == pol.MaxAttempts {
			if err != nil {
				return nil, err
			}
			return resp, nil // propagate non-retryable HTTP status upward
		}

		// Compute wait: Retry-After wins if set and enabled, else
		// exponential backoff with jitter.
		wait := backoff(pol, attempt)
		if err == nil && pol.RespectRetryAfter {
			if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
				wait = ra
			}
		}

		// Drain the failed response so the connection can be reused.
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		lastErr = err

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = errors.New("llm: retry exhausted")
	}
	return nil, lastErr
}

// shouldRetryStatus covers the "definitely transient" codes. We don't
// retry 401/403 (auth doesn't heal with a sleep) or 400 (bad input).
// 404 is included because some providers return it transiently when
// the serving pod has been scaled down mid-rollout.
func shouldRetryStatus(code int) bool {
	switch code {
	case 408, 409, 425, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// isTransientNetErr classifies Go net errors. Timeout and connection-
// reset style errors are retried; TLS handshake failures, DNS
// resolution failures are usually not transient in practice and we let
// them surface.
func isTransientNetErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false // caller-driven; do not retry on caller's behalf
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// Net lib wraps as *net.OpError in many cases — the Temporary()
	// interface is deprecated but still present on these types.
	type temporary interface{ Temporary() bool }
	if t, ok := err.(temporary); ok && t.Temporary() {
		return true
	}
	// Fall back to string sniffing for wrapped errors. Ugly but the
	// only portable way to catch "i/o timeout" through err wrapping.
	msg := err.Error()
	if strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF") {
		return true
	}
	return false
}

// backoff returns the next wait duration with jitter applied. attempt
// is 1-based — the first retry waits BaseDelay, the second 2×, etc.
//
// MaxDelay == 0 is interpreted as "no cap" (rather than "cap at zero"),
// which matches the natural reading of "set the ceiling I care about;
// leave unset if I don't care". This prevents a zero-value RetryPolicy
// from firing every attempt instantly.
func backoff(pol RetryPolicy, attempt int) time.Duration {
	d := pol.BaseDelay << (attempt - 1)
	if d < 0 {
		d = pol.MaxDelay
	} else if pol.MaxDelay > 0 && d > pol.MaxDelay {
		d = pol.MaxDelay
	}
	if pol.JitterFrac > 0 {
		// Symmetric jitter: ±JitterFrac × delay. The sign is random so
		// half of retries wait slightly less, spreading load.
		jitter := time.Duration(float64(d) * pol.JitterFrac * (rand.Float64()*2 - 1))
		d += jitter
		if d < 0 {
			d = 0
		}
	}
	return d
}

// parseRetryAfter handles both formats of the Retry-After header:
//   "120"            → 120 seconds
//   "Wed, ... GMT"   → absolute HTTP-date, wait until that instant
// Returns 0 when unparseable.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	// Numeric form first (most common).
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
