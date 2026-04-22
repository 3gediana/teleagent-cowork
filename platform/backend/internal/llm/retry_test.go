package llm

// Retry policy unit tests. The fake transport lets us pin the
// decision tree without hitting a real provider.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stubResp is a minimal response builder.
func stubResp(status int, body, retryAfter string) *http.Response {
	hdr := http.Header{}
	if retryAfter != "" {
		hdr.Set("Retry-After", retryAfter)
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     hdr,
	}
}

func TestDoWithRetry_SuccessFirstTry(t *testing.T) {
	var calls int
	pol := RetryPolicy{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond}
	resp, err := DoWithRetry(context.Background(), pol, func(attempt int) (*http.Response, error) {
		calls++
		return stubResp(200, "ok", ""), nil
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if calls != 1 {
		t.Errorf("want 1 call, got %d", calls)
	}
	if resp.StatusCode != 200 {
		t.Errorf("want 200, got %d", resp.StatusCode)
	}
}

func TestDoWithRetry_RetriesOn429ThenSucceeds(t *testing.T) {
	var calls int
	pol := RetryPolicy{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}
	resp, err := DoWithRetry(context.Background(), pol, func(attempt int) (*http.Response, error) {
		calls++
		if attempt < 2 {
			return stubResp(429, "throttled", ""), nil
		}
		return stubResp(200, "ok", ""), nil
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if calls != 2 {
		t.Errorf("want 2 calls, got %d", calls)
	}
	if resp.StatusCode != 200 {
		t.Errorf("want 200, got %d", resp.StatusCode)
	}
}

func TestDoWithRetry_DoesNotRetryOn401(t *testing.T) {
	var calls int
	pol := RetryPolicy{MaxAttempts: 5, BaseDelay: 1 * time.Millisecond}
	resp, err := DoWithRetry(context.Background(), pol, func(attempt int) (*http.Response, error) {
		calls++
		return stubResp(401, "bad key", ""), nil
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if calls != 1 {
		t.Errorf("401 should not retry; got %d calls", calls)
	}
	if resp.StatusCode != 401 {
		t.Errorf("want 401, got %d", resp.StatusCode)
	}
}

func TestDoWithRetry_ExhaustedReturnsLastError(t *testing.T) {
	var calls int
	pol := RetryPolicy{MaxAttempts: 2, BaseDelay: 1 * time.Millisecond}
	_, err := DoWithRetry(context.Background(), pol, func(attempt int) (*http.Response, error) {
		calls++
		return nil, errors.New("i/o timeout")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 2 {
		t.Errorf("want 2 attempts, got %d", calls)
	}
}

func TestDoWithRetry_RespectsContextCancel(t *testing.T) {
	pol := RetryPolicy{MaxAttempts: 10, BaseDelay: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	done := make(chan error, 1)
	go func() {
		_, err := DoWithRetry(ctx, pol, func(attempt int) (*http.Response, error) {
			calls++
			return stubResp(503, "down", ""), nil
		})
		done <- err
	}()
	// Let at least one attempt happen, then cancel.
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("DoWithRetry did not return after cancel")
	}
}

func TestParseRetryAfter_Numeric(t *testing.T) {
	if d := parseRetryAfter("42"); d != 42*time.Second {
		t.Errorf("numeric form: got %v, want 42s", d)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	future := time.Now().Add(60 * time.Second).UTC().Format(http.TimeFormat)
	d := parseRetryAfter(future)
	// Should be roughly 60s, give or take a few seconds for test jitter.
	if d < 55*time.Second || d > 62*time.Second {
		t.Errorf("HTTP-date form: got %v, want ~60s", d)
	}
}

func TestParseRetryAfter_Garbage(t *testing.T) {
	if d := parseRetryAfter("nonsense"); d != 0 {
		t.Errorf("garbage should parse to 0, got %v", d)
	}
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("empty should parse to 0, got %v", d)
	}
}

func TestBackoff_GrowsExponentiallyWithinCap(t *testing.T) {
	pol := RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: 1 * time.Second, JitterFrac: 0}
	got := []time.Duration{
		backoff(pol, 1),
		backoff(pol, 2),
		backoff(pol, 3),
		backoff(pol, 10),
	}
	for i, d := range got {
		fmt.Printf("attempt %d: %v\n", i+1, d)
	}
	if got[0] != 100*time.Millisecond {
		t.Errorf("attempt 1: want 100ms, got %v", got[0])
	}
	if got[1] != 200*time.Millisecond {
		t.Errorf("attempt 2: want 200ms, got %v", got[1])
	}
	// High attempts must cap at MaxDelay.
	if got[3] != 1*time.Second {
		t.Errorf("attempt 10 should cap at MaxDelay=1s, got %v", got[3])
	}
}
