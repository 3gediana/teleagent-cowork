package opencode

// This adapter lets agentpool.Manager create opencode sessions at
// spawn time without importing the HTTP client directly. Keeping it
// in the opencode package (rather than agentpool) means agentpool
// stays dependency-free and the adapter can grow if the session-
// creation contract ever needs more than one call (e.g. prime
// initial message, subscribe to SSE, etc.).

import (
	"context"
	"fmt"
	"time"
)

// NewPoolSessionCreator returns an adapter that implements
// agentpool.SessionCreator by calling Client.CreateSession against
// whichever opencode serve the pool points us at. Each spawn gets a
// fresh short-lived Client so HTTP connection state doesn't leak
// between agents that ultimately point at different ports.
//
// `creatorTimeout` bounds how long we wait for the opencode serve to
// accept a CreateSession call. Zero falls back to 10s — plenty for a
// local subprocess that just passed /global/health.
func NewPoolSessionCreator(creatorTimeout time.Duration) *PoolSessionCreator {
	if creatorTimeout == 0 {
		creatorTimeout = 10 * time.Second
	}
	return &PoolSessionCreator{timeout: creatorTimeout}
}

// PoolSessionCreator is exported so cmd/server can pass it to
// agentpool.Manager.WithSessionCreator. It has no per-instance
// state — the serveURL comes in on each call.
type PoolSessionCreator struct {
	timeout time.Duration
}

// CreateInitialSession is the agentpool.SessionCreator contract.
// We build the Client inline because each pool agent has its own
// serveURL; caching wouldn't help and would add lifecycle concerns
// to the pool shutdown path.
func (p *PoolSessionCreator) CreateInitialSession(ctx context.Context, serveURL, agentName string) (string, error) {
	return p.createSessionWithTitle(ctx, serveURL, sessionTitle(agentName, 0))
}

// CreateArchiveSession builds a replacement session after a context
// rotation, using a title shape that makes the version visible in
// opencode's own session list ("pool:alpha#2").
func (p *PoolSessionCreator) CreateArchiveSession(ctx context.Context, serveURL, agentName string, rotation int) (string, error) {
	return p.createSessionWithTitle(ctx, serveURL, sessionTitle(agentName, rotation))
}

// ContextSize queries the latest assistant turn on the session for
// its input + cache.read token total, the number the context watcher
// compares against the archive threshold. Implemented here so
// PoolSessionCreator doubles as an agentpool.ContextProbe — callers
// that want both only wire one object.
func (p *PoolSessionCreator) ContextSize(ctx context.Context, serveURL, sessionID string) (int, error) {
	if serveURL == "" {
		return 0, fmt.Errorf("pool session creator: empty serveURL")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	return New(serveURL).ContextSize(ctx, sessionID)
}

// sessionTitle stamps the rotation count into the session title so
// operators browsing opencode's session list see a clear lineage.
// Rotation=0 yields the bare "pool:<name>" title used for the
// initial session; rotation>=1 adds "#N" so sorting keeps them in
// chronological order.
func sessionTitle(agentName string, rotation int) string {
	name := agentName
	if name == "" {
		name = "unnamed"
	}
	base := "pool:" + name
	if rotation <= 0 {
		return base
	}
	return fmt.Sprintf("%s#%d", base, rotation+1) // rotation=1 → suffix "#2"
}

func (p *PoolSessionCreator) createSessionWithTitle(ctx context.Context, serveURL, title string) (string, error) {
	if serveURL == "" {
		return "", fmt.Errorf("pool session creator: empty serveURL")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	info, err := New(serveURL).CreateSession(ctx, title)
	if err != nil {
		return "", fmt.Errorf("create session on %s: %w", serveURL, err)
	}
	return info.ID, nil
}

// ---------------------------------------------------------------
// BroadcastInjector adapter
// ---------------------------------------------------------------

// NewPoolBroadcastInjector returns an adapter that satisfies
// agentpool.BroadcastInjector by posting to the opencode serve's
// /session/:id/prompt_async endpoint. We pick async over sync
// (/message) because the pool's broadcast consumer is a single
// goroutine per agent and the injection should fan out fast — the
// assistant reply lands on opencode's own timeline and the
// context watcher picks up the resulting token growth on its
// next poll. Caller hands us provider+model per call since the
// pool agent's model can in principle change between events
// (dashboard edits the opencode provider/model on an existing
// instance).
//
// `injectTimeout` bounds each HTTP call. Zero falls back to 20s,
// which is comfortably above opencode 1.14.21's observed latency
// for accepting a prompt_async (~1s) but short enough that a
// wedged serve doesn't stall every subsequent broadcast.
func NewPoolBroadcastInjector(injectTimeout time.Duration) *PoolBroadcastInjector {
	if injectTimeout == 0 {
		injectTimeout = 20 * time.Second
	}
	return &PoolBroadcastInjector{timeout: injectTimeout}
}

// PoolBroadcastInjector is a stateless adapter; the timeout is
// the only configuration. Exported so main.go can thread it
// into agentpool.Manager.WithBroadcastInjector.
type PoolBroadcastInjector struct {
	timeout time.Duration
}

// InjectMessage is the agentpool.BroadcastInjector contract.
// Empty provider or model is rejected up-front: opencode silently
// returns parts=0 in that case, which means the message lands in
// the session as an empty turn and the LLM happily hallucinates
// a reply without ever having seen the broadcast text. Hard-fail
// here so the caller can log it instead.
func (i *PoolBroadcastInjector) InjectMessage(ctx context.Context, serveURL, sessionID, text, providerID, modelID string) error {
	if serveURL == "" {
		return fmt.Errorf("pool broadcast injector: empty serveURL")
	}
	if sessionID == "" {
		return fmt.Errorf("pool broadcast injector: empty sessionID")
	}
	if providerID == "" || modelID == "" {
		return fmt.Errorf("pool broadcast injector: empty provider/model (would produce parts=0)")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, i.timeout)
		defer cancel()
	}
	return New(serveURL).PromptAsync(ctx, sessionID, text, Model{
		ProviderID: providerID,
		ModelID:    modelID,
	})
}
