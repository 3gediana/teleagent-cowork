package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateSession_RoundTrip verifies we POST the correct body and
// decode the serve's JSON response into SessionInfo. Using httptest
// instead of a real opencode keeps this test fast (<10ms) and
// runnable on CI without npm.
func TestCreateSession_RoundTrip(t *testing.T) {
	var receivedBody map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"ses_test_123","title":"archive resume","directory":"D:/work","time":{"created":1,"updated":1}}`))
	}))
	defer ts.Close()

	c := New(ts.URL)
	info, err := c.CreateSession(context.Background(), "archive resume")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if info.ID != "ses_test_123" {
		t.Errorf("unexpected session id: %s", info.ID)
	}
	if receivedBody["title"] != "archive resume" {
		t.Errorf("title not forwarded: got %q", receivedBody["title"])
	}
}

// TestContextSize_ReadsLatestAssistant exercises the heuristic behind
// our archive trigger: we want the sum input+cache.read on the most
// recent assistant turn, ignoring user turns and earlier assistants.
func TestContextSize_ReadsLatestAssistant(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/message") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Three turns: user, assistant (older), user, assistant (latest).
		// We should only read the LATEST assistant's tokens.
		w.Write([]byte(`[
			{"info":{"role":"user","id":"m1"},"parts":[]},
			{"info":{"role":"assistant","id":"m2","tokens":{"input":100,"output":50,"cache":{"read":200}}},"parts":[]},
			{"info":{"role":"user","id":"m3"},"parts":[]},
			{"info":{"role":"assistant","id":"m4","tokens":{"input":80,"output":30,"cache":{"read":45000}}},"parts":[]}
		]`))
	}))
	defer ts.Close()

	c := New(ts.URL)
	got, err := c.ContextSize(context.Background(), "ses_1")
	if err != nil {
		t.Fatalf("context size: %v", err)
	}
	want := 80 + 45000 // latest assistant's input + cache.read
	if got != want {
		t.Errorf("got %d tokens, want %d", got, want)
	}
}

// TestContextSize_EmptySessionReturnsZero: a brand-new session with
// no assistant turns yet should report 0 so the archive check
// doesn't fire spuriously.
func TestContextSize_EmptySessionReturnsZero(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	c := New(ts.URL)
	got, err := c.ContextSize(context.Background(), "ses_empty")
	if err != nil {
		t.Fatalf("context size: %v", err)
	}
	if got != 0 {
		t.Errorf("fresh session should be 0 tokens, got %d", got)
	}
}

// TestPoolSessionCreator_UsesAgentNameInTitle pins the naming
// convention pool sessions land on in opencode's own dashboard. If
// we ever break this we'll lose the ability to spot which pool agent
// a session belongs to at a glance.
func TestPoolSessionCreator_UsesAgentNameInTitle(t *testing.T) {
	var received map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"ses_pool_abc","title":"pool:alpha","directory":"."}`))
	}))
	defer ts.Close()

	sc := NewPoolSessionCreator(0)
	id, err := sc.CreateInitialSession(context.Background(), ts.URL, "alpha")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if id != "ses_pool_abc" {
		t.Errorf("unexpected id: %s", id)
	}
	if received["title"] != "pool:alpha" {
		t.Errorf("title not forwarded; got %q", received["title"])
	}
}

// TestPromptAsync_SendsModel checks that the Model we pass actually
// ends up in the request body. This is the specific regression guard
// for the bug we just debugged: opencode returns parts=0 when model
// is missing, so losing it here would silently re-break pool agents.
func TestPromptAsync_SendsModel(t *testing.T) {
	var body struct {
		Parts []map[string]any `json:"parts"`
		Model Model            `json:"model"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/prompt_async") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := New(ts.URL)
	err := c.PromptAsync(
		context.Background(),
		"ses_x",
		"do the thing",
		Model{ProviderID: "minimax-coding-plan", ModelID: "MiniMax-M2.7"},
	)
	if err != nil {
		t.Fatalf("prompt async: %v", err)
	}
	if body.Model.ProviderID != "minimax-coding-plan" || body.Model.ModelID != "MiniMax-M2.7" {
		t.Errorf("model not in body: %+v", body.Model)
	}
	if len(body.Parts) != 1 || body.Parts[0]["text"] != "do the thing" {
		t.Errorf("parts wrong: %+v", body.Parts)
	}
}
