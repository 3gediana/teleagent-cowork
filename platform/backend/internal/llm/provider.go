package llm

// Provider is the interface every LLM backend adapter implements.
// Registry holds the live set of Entry records keyed by endpoint id
// (the DB primary key from model.LLMEndpoint) and routes ChatStream
// calls from the agent runner to the right adapter.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Provider is the contract every adapter implements. It is deliberately
// small: one streaming method + static introspection. Retry / cost are
// handled inside each adapter (they need provider-specific error
// categorisation — 429 means different things in different APIs).
type Provider interface {
	// ID identifies the wire format (anthropic | openai). Endpoint
	// identity lives on the Registry entry, not here — a single
	// adapter type serves every deployment of its format.
	ID() ProviderID

	// Name is a human-readable label (e.g. "Anthropic", "MiniMax").
	Name() string

	// Models returns the list of model IDs this provider is configured
	// for. Used by the dashboard's model picker. The list must include
	// pricing + capability metadata so UI can tag rows without a
	// follow-up call.
	Models() []ModelInfo

	// ChatStream starts a streaming chat completion. The returned
	// channel receives StreamEvents in order; it closes exactly once
	// after an EvMessageStop or EvError event has been delivered, so
	// consumers can `for ev := range ch { ... }` without worrying about
	// missing the terminal event. Cancellation is driven by the
	// supplied context — on ctx.Done() the provider emits an EvError
	// (ctx.Err) and closes the channel promptly.
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}

// ProviderConfig is the per-deployment knob bag passed to an adapter
// constructor. Seeded today from model.LLMEndpoint rows (via loader.go);
// config.yaml seeding is also supported for bootstrap scenarios where
// the DB doesn't yet exist (e.g. first-launch migration).
type ProviderConfig struct {
	ID      ProviderID  `yaml:"id"`       // wire format
	Name    string      `yaml:"name"`     // display label
	BaseURL string      `yaml:"base_url,omitempty"`
	APIKey  string      `yaml:"api_key"`
	Models  []ModelInfo `yaml:"models"`

	// Timeout is how long a single streaming attempt may run before
	// the provider aborts. Default 300 s matches opencode's budget.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// MaxRetries applies to the initial POST (pre-stream). Once the
	// stream is open, retries happen at the caller level because mid-
	// stream failures can't safely replay without duplicating tokens.
	MaxRetries int `yaml:"max_retries,omitempty"`

	// Extra is an escape hatch for provider-specific settings we
	// haven't promoted to first-class fields yet (e.g. Azure
	// deployment name, Bedrock region).
	Extra map[string]string `yaml:"extra,omitempty"`
}

// Entry binds one endpoint-id (as stored in model.LLMEndpoint.ID) to
// its live Provider adapter + metadata the handler/loader layers want
// to surface without a second DB round-trip.
type Entry struct {
	EndpointID   string      // model.LLMEndpoint.ID — stable string key
	EndpointName string      // human label for dashboards
	Format       ProviderID  // anthropic | openai
	DefaultModel string      // model to use when caller leaves ChatRequest.Model empty
	Provider     Provider    // live adapter, ready for ChatStream
}

// Registry is the process-wide endpoint pool. Keyed by endpoint id so
// operators can register multiple endpoints of the same wire format
// (prod + staging MiniMax, two different Anthropic orgs, a local
// Ollama for dev, etc.). Thread-safe; favours read-heavy workloads
// through sync.RWMutex.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry // endpoint_id → Entry
}

// DefaultRegistry is the process-wide singleton. Other packages
// (handler, agent runner) use it directly — matches the convention
// used by model.DB, agent.DefaultManager elsewhere in the codebase.
var DefaultRegistry = &Registry{entries: map[string]*Entry{}}

// Register installs or overwrites an entry. Overwrite is the intended
// behaviour when an operator edits an endpoint in the dashboard: the
// loader calls Register with the same id and the runtime picks up
// new BaseURL/APIKey without a service restart.
func (r *Registry) Register(e *Entry) {
	if e == nil || e.EndpointID == "" {
		return
	}
	r.mu.Lock()
	r.entries[e.EndpointID] = e
	r.mu.Unlock()
}

// Remove detaches an endpoint from the live registry. The DB row may
// survive (status=disabled) for audit — registry just stops routing
// to it. Agents holding a RoleOverride that references the removed
// endpoint will fail at dispatch time with a clear "endpoint not
// registered" error, which is the right signal for a misconfiguration.
func (r *Registry) Remove(endpointID string) {
	r.mu.Lock()
	delete(r.entries, endpointID)
	r.mu.Unlock()
}

// Get looks up an entry by endpoint id. Missing ids are a
// configuration bug, not a transient failure — callers should surface
// the error rather than retry.
func (r *Registry) Get(endpointID string) (*Entry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[endpointID]
	if !ok {
		return nil, fmt.Errorf("llm: no endpoint registered for id=%s", endpointID)
	}
	return e, nil
}

// List returns all active entries, cheapest-to-consume snapshot.
// Ordering is non-deterministic — dashboards sort client-side.
func (r *Registry) List() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	return out
}

// ResolveRole picks the endpoint+model a given agent role should use.
// Precedence (highest → lowest):
//   1. Explicit RoleOverride row with both endpoint_id + model_id set
//   2. Explicit RoleOverride with endpoint_id only → use endpoint's
//      DefaultModel
//   3. First registered endpoint whose DefaultModel is non-empty
//      (fallback for fresh installs with no overrides yet)
//
// Returns ErrNoEndpoint when the registry is empty — callers should
// translate that to a 503 for user-facing requests.
func (r *Registry) ResolveRole(endpointID, modelID string) (*Entry, string, error) {
	if endpointID != "" {
		e, err := r.Get(endpointID)
		if err != nil {
			return nil, "", err
		}
		m := modelID
		if m == "" {
			m = e.DefaultModel
		}
		if m == "" {
			return nil, "", fmt.Errorf("llm: endpoint %q has no default model; caller must specify one", endpointID)
		}
		return e, m, nil
	}
	// Fallback path: first entry with a default model.
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		if e.DefaultModel != "" {
			return e, e.DefaultModel, nil
		}
	}
	return nil, "", ErrNoEndpoint
}

// ChatStream is the convenience entry point used by the agent runner.
// Resolves the endpoint, enforces a default model if the caller left
// one blank, and hands off to the underlying Provider.
func (r *Registry) ChatStream(ctx context.Context, endpointID string, req ChatRequest) (<-chan StreamEvent, error) {
	entry, model, err := r.ResolveRole(endpointID, req.Model)
	if err != nil {
		return nil, err
	}
	req.Model = model
	return entry.Provider.ChatStream(ctx, req)
}

// ErrNoEndpoint surfaces on ResolveRole when the registry is empty —
// a recoverable state during first-launch before any endpoint has
// been added.
var ErrNoEndpoint = fmt.Errorf("llm: no LLM endpoints configured; add one via the dashboard")

// sharedHTTPClient is used by every provider. One pooled client
// maximises connection reuse across adapters; each request sets its
// own per-call timeout via ctx.
//
// Timeout is set high because streaming responses can legitimately
// take minutes on large generations. The ctx-based deadline still
// bounds individual requests from the caller side.
var sharedHTTPClient = &http.Client{
	Timeout: 0, // no global timeout — rely on request contexts
	Transport: &http.Transport{
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     120 * time.Second,
		// Disable HTTP/2 ping interference with long streams — a few
		// providers (notably legacy Anthropic edge) have flaky H/2
		// behaviour on slow streams. Let Go pick HTTP/1.1 or HTTP/2
		// per handshake.
	},
}

// HTTPClient is exported for tests to swap in a transport that can
// replay recorded fixtures without touching the network. Production
// code should not touch this.
func HTTPClient() *http.Client { return sharedHTTPClient }

// SetHTTPClient is the counterpart used by tests. Not thread-safe —
// call it once at the start of a test file's TestMain.
func SetHTTPClient(c *http.Client) { sharedHTTPClient = c }
