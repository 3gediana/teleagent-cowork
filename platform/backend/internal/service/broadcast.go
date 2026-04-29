package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/a3c/platform/internal/agentpool"
	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/model"
	"github.com/gin-gonic/gin"
)

type SSEMessage struct {
	Header  SSEHeader `json:"header"`
	Payload gin.H     `json:"payload"`
	Meta    gin.H     `json:"meta"`
}

type SSEHeader struct {
	MessageID     string `json:"messageId"`
	Type          string `json:"type"`
	Version       string `json:"version"`
	Timestamp     string `json:"timestamp"`
	TargetAgentID string `json:"target_agent_id,omitempty"` // empty = all agents in project
}

type SSEClient struct {
	ID        string // unique per-connection ID
	ProjectID string // project this client is subscribed to
	Channel   chan SSEMessage
	Quit      chan struct{}
}

type SSEManagerStruct struct {
	clients map[string]*SSEClient // keyed by unique client ID, NOT by projectID
	mu      sync.RWMutex
}

var SSEManager = &SSEManagerStruct{
	clients: make(map[string]*SSEClient),
}

// AddClient creates a new SSE client subscribed to a project. The returned
// client.ID is a unique connection ID so multiple clients can subscribe to
// the same project simultaneously (previously a new connection overwrote any
// existing one for the same project).
func (m *SSEManagerStruct) AddClient(projectID string) *SSEClient {
	client := &SSEClient{
		ID:        model.GenerateID("sse"),
		ProjectID: projectID,
		Channel:   make(chan SSEMessage, 10),
		Quit:      make(chan struct{}),
	}
	m.mu.Lock()
	m.clients[client.ID] = client
	m.mu.Unlock()
	return client
}

func (m *SSEManagerStruct) RemoveClient(clientID string) {
	m.mu.Lock()
	if client, ok := m.clients[clientID]; ok {
		close(client.Quit)
		delete(m.clients, clientID)
	}
	m.mu.Unlock()
}

func (m *SSEManagerStruct) BroadcastToProject(projectID string, eventType string, payload gin.H, targetAgentID string) {
	msg := SSEMessage{
		Header: SSEHeader{
			MessageID:     model.GenerateID("msg"),
			Type:          eventType,
			Version:       "1.0",
			Timestamp:     time.Now().Format(time.RFC3339),
			TargetAgentID: targetAgentID,
		},
		Payload: payload,
		Meta: gin.H{
			"project_id": projectID,
		},
	}

	jsonData, _ := json.Marshal(msg)
	msgKey := "a3c:broadcast:" + projectID

	// Redis is used for cross-process replay (resume-from-last-id,
	// multi-replica broadcast). When RDB is nil — dev boxes without
	// Redis, unit / e2e tests, offline tools — skip the persistence
	// path and fall through to the in-memory fanout so SSE still
	// works locally. Production deployments always have RDB wired.
	if model.RDB != nil {
		ctx := context.Background()
		model.RDB.LPush(ctx, msgKey, string(jsonData))
		model.RDB.LTrim(ctx, msgKey, 0, 99)
		model.RDB.Expire(ctx, msgKey, 24*time.Hour)

		// Ack key TTL: the actual key is created later by SAdd in GetUnackedBroadcasts
		// or AckAllBroadcasts; each SAdd call resets the TTL so the set eventually
		// expires when no more agents acknowledge. The Expire here is harmless if
		// the key doesn't exist yet.
		ackKey := fmt.Sprintf("a3c:broadcast:%s:%s:acked", projectID, msg.Header.MessageID)
		model.RDB.Expire(ctx, ackKey, 24*time.Hour)
		_ = ackKey
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, client := range m.clients {
		if client.ProjectID == projectID {
			select {
			case client.Channel <- msg:
			default:
			}
		}
	}
}

func (m *SSEManagerStruct) GetRecentBroadcasts(projectID string, limit int64) []SSEMessage {
	if model.RDB == nil {
		return nil
	}
	key := "a3c:broadcast:" + projectID
	ctx := context.Background()
	data, err := model.RDB.LRange(ctx, key, 0, limit-1).Result()
	if err != nil {
		return nil
	}
	messages := make([]SSEMessage, 0, len(data))
	for _, d := range data {
		var msg SSEMessage
		if json.Unmarshal([]byte(d), &msg) == nil {
			messages = append(messages, msg)
		}
	}
	return messages
}

// GetRecentSince returns all buffered messages produced after the given
// message ID, oldest-first (ready to replay). If lastEventID is empty,
// returns the last few messages as a cold-start snapshot. Caller can use
// this for SSE resume-from-id semantics.
func (m *SSEManagerStruct) GetRecentSince(projectID string, lastEventID string) []SSEMessage {
	if model.RDB == nil {
		return nil
	}
	key := "a3c:broadcast:" + projectID
	ctx := context.Background()
	data, err := model.RDB.LRange(ctx, key, 0, 99).Result()
	if err != nil {
		return nil
	}
	// Redis list has newest-first (LPush). Reverse to chronological order.
	reversed := make([]SSEMessage, 0, len(data))
	for i := len(data) - 1; i >= 0; i-- {
		var msg SSEMessage
		if json.Unmarshal([]byte(data[i]), &msg) == nil {
			reversed = append(reversed, msg)
		}
	}
	if lastEventID == "" {
		// Cold start: return only the last 5 for a quick primer.
		start := 0
		if len(reversed) > 5 {
			start = len(reversed) - 5
		}
		return reversed[start:]
	}
	// Resume: return everything produced strictly after lastEventID.
	out := make([]SSEMessage, 0, len(reversed))
	found := false
	for _, msg := range reversed {
		if !found {
			if msg.Header.MessageID == lastEventID {
				found = true
			}
			continue
		}
		out = append(out, msg)
	}
	// If the last ID wasn't in the buffer (e.g. evicted by LTRIM), fall
	// back to sending the full buffered history so the client at least
	// has continuity within our retention window.
	if !found {
		return reversed
	}
	return out
}

func (m *SSEManagerStruct) GetUnackedBroadcasts(projectID string, agentID string) []SSEMessage {
	if model.RDB == nil {
		return nil
	}
	key := "a3c:broadcast:" + projectID
	ctx := context.Background()
	data, err := model.RDB.LRange(ctx, key, 0, 49).Result()
	if err != nil {
		return nil
	}

	// State events: only keep latest per type (e.g. MILESTONE_UPDATE)
	// Incremental events: keep all (e.g. CHAT_UPDATE, TOOL_CALL)
	stateEventTypes := map[string]bool{
		"MILESTONE_UPDATE": true,
		"DIRECTION_CHANGE": true,
		"VERSION_UPDATE":   true,
		"VERSION_ROLLBACK": true,
		"MILESTONE_SWITCH": true,
	}

	seen := map[string]bool{}
	var result []SSEMessage
	for _, d := range data {
		var msg SSEMessage
		if json.Unmarshal([]byte(d), &msg) == nil {
			if stateEventTypes[msg.Header.Type] {
				if seen[msg.Header.Type] {
					continue
				}
				seen[msg.Header.Type] = true
			}

			ackKey := fmt.Sprintf("a3c:broadcast:%s:%s:acked", projectID, msg.Header.MessageID)
			acked, _ := model.RDB.SIsMember(ctx, ackKey, agentID).Result()
			if acked {
				continue
			}

			// Filter by target agent: if target_agent_id is set, only deliver to that agent
			if msg.Header.TargetAgentID != "" && msg.Header.TargetAgentID != agentID {
				continue
			}

			result = append(result, msg)
			model.RDB.SAdd(ctx, ackKey, agentID)
			// Set TTL on the set each time we add to it. Without this, the key
			// created by SAdd has no expiry and leaks in Redis forever.
			model.RDB.Expire(ctx, ackKey, 24*time.Hour)
		}
	}
	return result
}

func BroadcastEvent(projectID string, eventType string, payload gin.H, targetAgentID ...string) {
	target := ""
	if len(targetAgentID) > 0 {
		target = targetAgentID[0]
	}
	SSEManager.BroadcastToProject(projectID, eventType, payload, target)
}

// BroadcastDirected sends a message directly to a specific agent's Redis queue
// The MCP poller for that agent will pick it up and inject it into the OpenCode session
func BroadcastDirected(agentID string, eventType string, payload gin.H) {
	if model.RDB == nil {
		// Directed queues are Redis-only — no in-memory fallback. In test
		// contexts without Redis we silently drop.
		return
	}
	ctx := context.Background()
	key := fmt.Sprintf("a3c:directed:%s", agentID)

	msg := gin.H{
		"header": gin.H{
			"type":      eventType,
			"messageID": model.GenerateID("dir"),
			"timestamp": time.Now().UnixMilli(),
			"target":    agentID,
		},
		"payload": payload,
	}

	data, _ := json.Marshal(msg)
	model.RDB.RPush(ctx, key, string(data))
	model.RDB.Expire(ctx, key, 10*time.Minute)

	log.Printf("[Broadcast] Directed %s to agent %s", eventType, agentID)

	// Auto-wake: if the target is a dormant pool agent, kick off a
	// Wake in the background. The broadcast is already in Redis so
	// the pool's own consumer will deliver it as soon as the agent
	// is back to ready; this call just shortens the stall window
	// from "next manual wake" to "immediately". No-op for external
	// agents (not in the pool) or agents already running.
	if config.IsAutopilotEnabled() {
		if pm := agentpool.GetDefault(); pm != nil {
			_ = pm.EnsureReadyByAgentID(agentID)
		}
	}
}

// GetDirectedMessages returns the directed messages currently queued
// for the given agent. The queue itself is NOT modified — entries are
// only removed when the consumer (the MCP poller, via /poll's
// `acked_directed_ids` field) explicitly acks them. See
// AckDirectedMessages below for the LREM path.
//
// Until v0.3 this used to LRange + Del, which silently dropped any
// message the MCP-side inject path failed to deliver (no OpenCode
// session yet, network hiccup, restart between fetch and inject).
// Since AUDIT_RESULT / CHANGE_PENDING_CONFIRM / VERSION_UPDATE all
// flow through this queue, that bug meant external agents would
// occasionally never learn the verdict on their own PR.
//
// The 10-minute Redis TTL on the queue (see BroadcastDirected) is
// the safety net that bounds growth if a client never acks.
func GetDirectedMessages(agentID interface{}) []gin.H {
	if model.RDB == nil {
		return nil
	}
	ctx := context.Background()
	idStr := fmt.Sprintf("%v", agentID)
	key := fmt.Sprintf("a3c:directed:%s", idStr)

	data, err := model.RDB.LRange(ctx, key, 0, -1).Result()
	if err != nil || len(data) == 0 {
		return nil
	}

	var messages []gin.H
	for _, d := range data {
		var msg gin.H
		if json.Unmarshal([]byte(d), &msg) == nil {
			messages = append(messages, msg)
		}
	}

	return messages
}

// AckDirectedMessages removes from the per-agent directed queue every
// entry whose header.messageID is in messageIDs. Called from the
// /poll handler when the MCP client confirms it has injected those
// messages into its OpenCode session. Idempotent: re-acking a
// messageID that's already gone (TTL expired, double-ack) is a no-op.
//
// Implementation note: Redis LREM matches on exact value, so we
// LRange the queue, find the JSON blobs whose decoded header.messageID
// is in the requested set, and LREM each one by exact-blob match.
// The per-agent queue is bounded (small handful of pending events
// in steady state), so this O(n) scan per ack call is fine.
func AckDirectedMessages(agentID interface{}, messageIDs []string) {
	if model.RDB == nil || len(messageIDs) == 0 {
		return
	}
	ctx := context.Background()
	idStr := fmt.Sprintf("%v", agentID)
	key := fmt.Sprintf("a3c:directed:%s", idStr)

	data, err := model.RDB.LRange(ctx, key, 0, -1).Result()
	if err != nil || len(data) == 0 {
		return
	}

	wanted := make(map[string]bool, len(messageIDs))
	for _, id := range messageIDs {
		if id != "" {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return
	}

	for _, blob := range data {
		var env struct {
			Header struct {
				MessageID string `json:"messageID"`
			} `json:"header"`
		}
		if err := json.Unmarshal([]byte(blob), &env); err != nil {
			continue
		}
		if env.Header.MessageID == "" || !wanted[env.Header.MessageID] {
			continue
		}
		// LREM count=1 because each message is enqueued exactly once.
		// If multiple identical blobs exist (would require an exact
		// JSON-equal duplicate, including timestamp), removing one
		// here is the right behaviour — the others remain and will
		// be redelivered until the client acks them too.
		if remErr := model.RDB.LRem(ctx, key, 1, blob).Err(); remErr != nil {
			log.Printf("[Broadcast] LRem failed for ack of %s on %s: %v",
				env.Header.MessageID, idStr, remErr)
		}
	}
}

func (m *SSEManagerStruct) AckAllBroadcasts(projectID string, agentID string) {
	if model.RDB == nil {
		return
	}
	key := "a3c:broadcast:" + projectID
	ctx := context.Background()
	data, err := model.RDB.LRange(ctx, key, 0, 49).Result()
	if err != nil {
		return
	}
	for _, d := range data {
		var msg SSEMessage
		if json.Unmarshal([]byte(d), &msg) != nil {
			continue
		}
		ackKey := fmt.Sprintf("a3c:broadcast:%s:%s:acked", projectID, msg.Header.MessageID)
		model.RDB.SAdd(ctx, ackKey, agentID)
		model.RDB.Expire(ctx, ackKey, 24*time.Hour)
	}
}
