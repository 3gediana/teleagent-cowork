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
	if serveURL == "" {
		return "", fmt.Errorf("pool session creator: empty serveURL")
	}
	client := New(serveURL)
	// Title shape: "pool:<agent-name>" keeps the browser dashboard
	// readable (session rows are sortable by title in opencode's own
	// UI too) without leaking uuids. Archive rotations can add a
	// "-v2, -v3" suffix later.
	title := "pool:" + agentName
	if agentName == "" {
		title = "pool:unnamed"
	}

	// Apply our default timeout iff the caller didn't already supply
	// one — respecting an existing deadline lets cmd/server cut
	// session creation off as part of a broader spawn timeout.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}

	info, err := client.CreateSession(ctx, title)
	if err != nil {
		return "", fmt.Errorf("create session on %s: %w", serveURL, err)
	}
	return info.ID, nil
}
