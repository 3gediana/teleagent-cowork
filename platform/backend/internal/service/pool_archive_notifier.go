package service

// PoolArchiveNotifier bridges agentpool's archive-rotation event
// into the existing directed-broadcast channel used by the MCP
// poller. Lives in `service` (not `agentpool`) because:
//
//   - BroadcastDirected already lives here (needs Redis access).
//   - agentpool deliberately stays free of platform-service deps so
//     its tests don't need Redis to spin up.
//
// The adapter is trivial: pack the rotation facts into a gin.H
// payload and shove it into the directed-message queue keyed by
// agent id. The MCP poller on that agent will pick it up on the
// next tick (5s cadence today), create a fresh opencode session
// using the new id, and swap its cached session lock.

import (
	"github.com/gin-gonic/gin"
)

const poolArchiveEventType = "POOL_SESSION_ARCHIVE"

// PoolArchiveNotifierImpl satisfies agentpool.ArchiveNotifier. Zero
// value is usable; the only state it needs is the directed-broadcast
// function which is a package-level global anyway.
type PoolArchiveNotifierImpl struct{}

// NewPoolArchiveNotifier returns a notifier suitable for passing
// into agentpool.Manager.WithArchiveNotifier.
func NewPoolArchiveNotifier() *PoolArchiveNotifierImpl {
	return &PoolArchiveNotifierImpl{}
}

// NotifyArchive pushes the rotation event onto the agent's directed
// queue. Fields mirror what the MCP poller needs to do the switch:
//
//   - new_session_id: what opencode session to lock onto now.
//   - old_session_id / tokens / reason: informational for logging
//     on the client side + future dashboard timeline entries.
//
// We include the Redis-backed event type POOL_SESSION_ARCHIVE so
// the MCP's broadcast dispatcher can route it by type without
// string-matching the payload.
func (n *PoolArchiveNotifierImpl) NotifyArchive(agentID, oldSessionID, newSessionID string, tokens int, reason string) {
	BroadcastDirected(agentID, poolArchiveEventType, gin.H{
		"old_session_id": oldSessionID,
		"new_session_id": newSessionID,
		"tokens":         tokens,
		"reason":         reason,
	})
}
