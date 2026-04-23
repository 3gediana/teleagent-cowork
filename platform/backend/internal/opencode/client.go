// Package opencode is a thin HTTP client for opencode serve's JSON
// API, used by the platform to manage the long-running session
// attached to each pool agent. Scope is deliberately narrow:
//
//   - Create a session (with a title we pick) and get back its id.
//   - Inspect the most recent assistant message on a session so we
//     can read `tokens.input + tokens.cache.read` — that's our
//     current-context-size signal for the archive decision.
//   - Send a prompt on a session, either synchronously (hold the HTTP
//     connection until the assistant reply lands) or asynchronously
//     (fire and forget; poll for the assistant message later).
//
// What this client intentionally does NOT do:
//
//   - Manage the opencode subprocess lifecycle (see agentpool.Manager).
//   - Parse SSE streams or transcripts (we only care about the
//     committed message objects, not the step-by-step events).
//   - Do any retry / backoff logic (callers decide).
//
// Keeping the surface tight makes the package reviewable and lets us
// swap in a fake in tests without recreating half of opencode.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to a single opencode serve instance, identified by
// baseURL (e.g. "http://127.0.0.1:4097"). The HTTP client is shared
// across all calls; callers that need per-call timeouts should pass
// a context.Context with a deadline.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client pointing at the given opencode serve. The
// timeout is applied to every request that doesn't carry its own
// context deadline, so we fail fast on a dead server.
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Model identifies an opencode provider/model pair. Required on any
// request that triggers an LLM call (prompt_async, message) — opencode
// refuses to route those without an explicit model.
type Model struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// SessionInfo is the subset of opencode's /session payload we
// actually use.
type SessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

// TokenCounts mirrors opencode's assistant-message token breakdown.
// We care about Input + Cache.Read because that's what gets re-fed
// to the model on the NEXT turn — the cap we don't want to blow past.
type TokenCounts struct {
	Total     int `json:"total"`
	Input     int `json:"input"`
	Output    int `json:"output"`
	Reasoning int `json:"reasoning"`
	Cache     struct {
		Write int `json:"write"`
		Read  int `json:"read"`
	} `json:"cache"`
}

// MessageInfo is the shape of one entry in the /session/:id/message
// response. Fields we don't inspect are omitted; json.Unmarshal
// silently ignores them.
type MessageInfo struct {
	ID      string      `json:"id"`
	Role    string      `json:"role"`
	ModelID string      `json:"modelID"`
	Tokens  TokenCounts `json:"tokens"`
	Cost    float64     `json:"cost"`
	Time    struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

// MessageEnvelope wraps a message's info + parts. We don't decode
// parts here — consumers that need the actual text of a reply can
// poll /session/:id/message and read m.Parts themselves.
type MessageEnvelope struct {
	Info  MessageInfo       `json:"info"`
	Parts []json.RawMessage `json:"parts"`
}

// CreateSession creates a fresh empty session on the serve and
// returns its id. Opencode also accepts `{"title":"..."}` so we give
// each session a human-readable title derived from the caller's
// intent ("archive after 156K tokens", "initial spawn", etc.) to help
// operators browsing sessions in the dashboard.
func (c *Client) CreateSession(ctx context.Context, title string) (SessionInfo, error) {
	body, err := json.Marshal(map[string]string{"title": title})
	if err != nil {
		return SessionInfo{}, fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session", bytes.NewReader(body))
	if err != nil {
		return SessionInfo{}, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return SessionInfo{}, fmt.Errorf("create session: %d %s", resp.StatusCode, raw)
	}

	var out SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SessionInfo{}, fmt.Errorf("decode session: %w", err)
	}
	return out, nil
}

// ListSessions returns every session the serve currently knows
// about, oldest last. Useful when the platform restarts and needs
// to re-attach to whatever session the pool agent was using.
func (c *Client) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/session", nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list sessions: %d %s", resp.StatusCode, raw)
	}

	var out []SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}
	return out, nil
}

// ListMessages returns the full (info+parts) message objects for a
// session. Limit=0 means "serve default" (currently 50). Oldest
// first — caller picks the last assistant entry when reading tokens.
func (c *Client) ListMessages(ctx context.Context, sessionID string, limit int) ([]MessageEnvelope, error) {
	url := fmt.Sprintf("%s/session/%s/message", c.baseURL, sessionID)
	if limit > 0 {
		url = fmt.Sprintf("%s?limit=%d", url, limit)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list messages: %d %s", resp.StatusCode, raw)
	}
	var out []MessageEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}
	return out, nil
}

// ContextSize returns the number of input + cache.read tokens on the
// most recent assistant message in the session. Zero if no assistant
// message has landed yet (freshly-created session). This is the
// number we watch for the archive threshold — it's what the *next*
// prompt will have to replay, so it tracks "how full is this
// conversation" rather than "how much did the last turn cost".
func (c *Client) ContextSize(ctx context.Context, sessionID string) (int, error) {
	msgs, err := c.ListMessages(ctx, sessionID, 0)
	if err != nil {
		return 0, err
	}
	// Scan from newest backwards for the last assistant turn.
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Info.Role == "assistant" {
			t := msgs[i].Info.Tokens
			return t.Input + t.Cache.Read, nil
		}
	}
	return 0, nil
}

// PromptAsync enqueues a new user turn on the session without waiting
// for the assistant reply. Returns as soon as opencode has accepted
// the prompt. Callers poll ListMessages / ContextSize for the result.
//
// Every prompt MUST carry a model — opencode refuses to route a
// prompt to the LLM otherwise. We surface this as a required argument
// rather than a struct field so the caller can't forget it.
func (c *Client) PromptAsync(ctx context.Context, sessionID string, text string, model Model) error {
	body, err := json.Marshal(map[string]any{
		"parts": []map[string]any{
			{"type": "text", "text": text},
		},
		"model": model,
	})
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}
	url := fmt.Sprintf("%s/session/%s/prompt_async", c.baseURL, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("prompt_async: %d %s", resp.StatusCode, raw)
	}
	return nil
}

// Message sends a user turn and waits for the full assistant reply to
// be committed. Unlike PromptAsync this holds the HTTP connection
// until opencode has flushed the reply, so the caller needs a long
// enough context timeout to accommodate the LLM roundtrip.
func (c *Client) Message(ctx context.Context, sessionID string, text string, model Model) (*MessageEnvelope, error) {
	body, err := json.Marshal(map[string]any{
		"parts": []map[string]any{
			{"type": "text", "text": text},
		},
		"model": model,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	url := fmt.Sprintf("%s/session/%s/message", c.baseURL, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("message: %d %s", resp.StatusCode, raw)
	}
	var out MessageEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode assistant message: %w", err)
	}
	return &out, nil
}

// Health hits /global/health. Cheap pre-flight for "is this serve
// reachable" checks before a real call.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/global/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health: status %d", resp.StatusCode)
	}
	return nil
}
