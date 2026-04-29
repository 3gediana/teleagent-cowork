package service

// Regression tests for the directed-broadcast queue path.
//
// These tests exist because of an actual production bug: until the v0.3
// fix, GetDirectedMessages used to LRange-then-Del the per-agent queue
// in one shot. Any time the MCP-side inject failed (no OpenCode session
// yet, network hiccup, restart between fetch and inject), the message
// was silently dropped — including AUDIT_RESULT, CHANGE_PENDING_CONFIRM
// and VERSION_UPDATE. Audit-session-pending-forever was the most visible
// symptom. The fix made the protocol explicit-ack:
//
//   * GetDirectedMessages no longer mutates the queue.
//   * The MCP client confirms successful injection by passing
//     `acked_directed_ids` on its next /poll, which calls
//     AckDirectedMessages → LREM-by-messageID.
//
// We pin all four contract points here so a future refactor can't
// silently revert to consume-on-read:
//
//   - BroadcastDirected enqueues and stamps a unique messageID.
//   - GetDirectedMessages returns ALL queued messages without deletion.
//   - Repeated GetDirectedMessages without acks returns the same set.
//   - AckDirectedMessages with a known messageID LREMs exactly that one.
//   - AckDirectedMessages with an unknown ID is a no-op (idempotent).
//   - Empty agentID / nil RDB do not panic.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/a3c/platform/internal/model"
)

// withMiniRedis wires a fresh miniredis into model.RDB for the duration
// of the test, restoring the previous global on cleanup. Tests that
// don't share state can run in parallel with this helper because each
// gets its own server.
func withMiniRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	mr := miniredis.RunT(t)
	prev := model.RDB
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("miniredis ping failed: %v", err)
	}
	model.RDB = rdb
	t.Cleanup(func() {
		_ = rdb.Close()
		model.RDB = prev
	})
	return mr
}

func messageIDOf(t *testing.T, m gin.H) string {
	t.Helper()
	hdr, ok := m["header"].(map[string]interface{})
	if !ok {
		t.Fatalf("message has no header map: %#v", m)
	}
	id, _ := hdr["messageID"].(string)
	if id == "" {
		t.Fatalf("message header has no messageID: %#v", hdr)
	}
	return id
}

func TestBroadcastDirected_EnqueuesWithMessageID(t *testing.T) {
	withMiniRedis(t)

	BroadcastDirected("agent_test", "AUDIT_RESULT", gin.H{"verdict": "L0"})

	msgs := GetDirectedMessages("agent_test")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %#v", len(msgs), msgs)
	}
	id := messageIDOf(t, msgs[0])
	if id == "" {
		t.Errorf("messageID was empty")
	}
}

// TestGetDirectedMessages_DoesNotConsume is the headline regression.
// Before the v0.3 fix this test would FAIL: GetDirectedMessages used
// to LRange + Del, so the second call would return zero messages.
// The current contract is: pure read, ack is a separate call.
func TestGetDirectedMessages_DoesNotConsume(t *testing.T) {
	withMiniRedis(t)

	BroadcastDirected("agent_keepalive", "EVENT_A", gin.H{"n": 1})
	BroadcastDirected("agent_keepalive", "EVENT_B", gin.H{"n": 2})

	first := GetDirectedMessages("agent_keepalive")
	if len(first) != 2 {
		t.Fatalf("first read: expected 2 messages, got %d", len(first))
	}

	second := GetDirectedMessages("agent_keepalive")
	if len(second) != 2 {
		t.Fatalf("second read (no acks): expected 2 messages, got %d", len(second))
	}

	// Same messageIDs both times — the queue must not have rotated.
	for i, m := range first {
		if messageIDOf(t, m) != messageIDOf(t, second[i]) {
			t.Errorf("messageID at idx %d changed between reads: %s -> %s",
				i, messageIDOf(t, m), messageIDOf(t, second[i]))
		}
	}
}

func TestAckDirectedMessages_RemovesMatchingID(t *testing.T) {
	withMiniRedis(t)

	BroadcastDirected("agent_ack", "EVENT_A", gin.H{"n": 1})
	BroadcastDirected("agent_ack", "EVENT_B", gin.H{"n": 2})
	BroadcastDirected("agent_ack", "EVENT_C", gin.H{"n": 3})

	all := GetDirectedMessages("agent_ack")
	if len(all) != 3 {
		t.Fatalf("setup: expected 3 messages, got %d", len(all))
	}
	idA := messageIDOf(t, all[0])
	idB := messageIDOf(t, all[1])
	idC := messageIDOf(t, all[2])

	// Ack the middle one.
	AckDirectedMessages("agent_ack", []string{idB})

	remaining := GetDirectedMessages("agent_ack")
	if len(remaining) != 2 {
		t.Fatalf("after ack: expected 2 messages, got %d", len(remaining))
	}
	gotA := messageIDOf(t, remaining[0])
	gotC := messageIDOf(t, remaining[1])
	if gotA != idA {
		t.Errorf("expected first remaining = %s (event A), got %s", idA, gotA)
	}
	if gotC != idC {
		t.Errorf("expected second remaining = %s (event C), got %s", idC, gotC)
	}
}

func TestAckDirectedMessages_BatchAck(t *testing.T) {
	withMiniRedis(t)

	BroadcastDirected("agent_batch", "E1", gin.H{})
	BroadcastDirected("agent_batch", "E2", gin.H{})
	BroadcastDirected("agent_batch", "E3", gin.H{})

	msgs := GetDirectedMessages("agent_batch")
	ids := []string{messageIDOf(t, msgs[0]), messageIDOf(t, msgs[1]), messageIDOf(t, msgs[2])}

	// Ack first and last in one call — middle should remain.
	AckDirectedMessages("agent_batch", []string{ids[0], ids[2]})

	remaining := GetDirectedMessages("agent_batch")
	if len(remaining) != 1 {
		t.Fatalf("expected exactly 1 message remaining, got %d", len(remaining))
	}
	if messageIDOf(t, remaining[0]) != ids[1] {
		t.Errorf("expected middle id %s to remain, got %s", ids[1], messageIDOf(t, remaining[0]))
	}
}

func TestAckDirectedMessages_UnknownIDIsNoop(t *testing.T) {
	withMiniRedis(t)

	BroadcastDirected("agent_unknown", "E1", gin.H{})
	BroadcastDirected("agent_unknown", "E2", gin.H{})

	// Ack a phantom ID (already-expired / never-existed). Must not
	// drop unrelated messages or crash.
	AckDirectedMessages("agent_unknown", []string{"dir_definitely_not_real"})

	remaining := GetDirectedMessages("agent_unknown")
	if len(remaining) != 2 {
		t.Fatalf("phantom ack should be a no-op, got %d remaining (expected 2)", len(remaining))
	}
}

func TestAckDirectedMessages_DoubleAckIsIdempotent(t *testing.T) {
	withMiniRedis(t)

	BroadcastDirected("agent_double", "E1", gin.H{})
	BroadcastDirected("agent_double", "E2", gin.H{})

	msgs := GetDirectedMessages("agent_double")
	idToAck := messageIDOf(t, msgs[0])

	AckDirectedMessages("agent_double", []string{idToAck})
	AckDirectedMessages("agent_double", []string{idToAck}) // double-ack
	AckDirectedMessages("agent_double", []string{idToAck}) // triple-ack

	remaining := GetDirectedMessages("agent_double")
	if len(remaining) != 1 {
		t.Fatalf("double/triple ack should leave 1 message, got %d", len(remaining))
	}
	if messageIDOf(t, remaining[0]) == idToAck {
		t.Errorf("acked id %s should be gone but is still here", idToAck)
	}
}

func TestGetDirectedMessages_EmptyQueueReturnsEmpty(t *testing.T) {
	withMiniRedis(t)

	msgs := GetDirectedMessages("agent_with_no_traffic")
	if len(msgs) != 0 {
		t.Errorf("empty queue should return 0 messages, got %d", len(msgs))
	}
}

func TestAckDirectedMessages_EmptyArgsIsNoop(t *testing.T) {
	withMiniRedis(t)

	BroadcastDirected("agent_empty", "E1", gin.H{})

	// Both nil and empty slice must be safe — exercises the early-return.
	AckDirectedMessages("agent_empty", nil)
	AckDirectedMessages("agent_empty", []string{})

	remaining := GetDirectedMessages("agent_empty")
	if len(remaining) != 1 {
		t.Errorf("empty ack list should preserve queue, got %d (expected 1)", len(remaining))
	}
}

func TestBroadcastDirected_SetsTTL(t *testing.T) {
	mr := withMiniRedis(t)

	BroadcastDirected("agent_ttl", "E1", gin.H{})

	ttl := mr.TTL("a3c:directed:agent_ttl")
	if ttl <= 0 {
		t.Errorf("expected positive TTL on directed queue, got %v", ttl)
	}
	// The implementation sets 10 minutes; we just sanity-check it's
	// in the right ballpark. Loose bound (≤ 11 min) tolerates any
	// future tweak that's still in the "minutes, not hours" regime.
	if ttl > 11*60_000_000_000 { // 11 min in ns (Duration is int64 ns)
		t.Errorf("TTL %v looks too long — expected ≤ 11 min", ttl)
	}
}

// TestRDBNilSafety pins the early-return guards in BroadcastDirected /
// GetDirectedMessages / AckDirectedMessages. We exercise them with a
// nil global; the contract is "no panic, no work done".
func TestRDBNilSafety(t *testing.T) {
	prev := model.RDB
	model.RDB = nil
	t.Cleanup(func() { model.RDB = prev })

	// All three must be safe to call.
	BroadcastDirected("nobody", "EVT", gin.H{})
	if msgs := GetDirectedMessages("nobody"); msgs != nil {
		t.Errorf("expected nil from GetDirectedMessages with RDB=nil, got %#v", msgs)
	}
	AckDirectedMessages("nobody", []string{"any"}) // must not panic
}

// TestMessageShape pins the JSON envelope that the MCP poller and the
// server-side LREM both depend on. If these field names change, the
// MCP client's extractMsgId() (which checks header.messageID, MessageID,
// message_id) and the server's AckDirectedMessages (which JSON-decodes
// header.messageID) must change in lockstep.
func TestMessageShape(t *testing.T) {
	withMiniRedis(t)

	BroadcastDirected("agent_shape", "AUDIT_RESULT", gin.H{
		"change_id": "chg_abc",
		"status":    "approved",
	})

	msgs := GetDirectedMessages("agent_shape")
	if len(msgs) != 1 {
		t.Fatalf("expected 1, got %d", len(msgs))
	}

	// Round-trip through JSON to confirm what the wire actually looks
	// like (gin.H is just map[string]any, but nesting from json.Unmarshal
	// uses map[string]any too — so the test below mirrors what the MCP
	// client sees after axios JSON-parses the /poll response).
	raw, err := json.Marshal(msgs[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	header, ok := decoded["header"].(map[string]any)
	if !ok {
		t.Fatalf("header missing or wrong shape: %#v", decoded)
	}
	for _, k := range []string{"type", "messageID", "timestamp", "target"} {
		if _, ok := header[k]; !ok {
			t.Errorf("header missing %q: %#v", k, header)
		}
	}
	if got, _ := header["type"].(string); got != "AUDIT_RESULT" {
		t.Errorf("expected type=AUDIT_RESULT, got %q", got)
	}
	if got, _ := header["target"].(string); got != "agent_shape" {
		t.Errorf("expected target=agent_shape, got %q", got)
	}
	payload, ok := decoded["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing or wrong shape: %#v", decoded)
	}
	if got, _ := payload["change_id"].(string); got != "chg_abc" {
		t.Errorf("expected payload.change_id=chg_abc, got %q", got)
	}
}
