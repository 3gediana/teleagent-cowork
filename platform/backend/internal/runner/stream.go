package runner

// Streaming hooks.
//
// Every interesting runner event (text delta, tool call, turn summary,
// final reply) flows through StreamEmitter. At startup, main.go wires
// StreamEmitter to service.SSEManager.BroadcastToProject so the
// dashboard sees native-runtime sessions the same way it sees
// opencode sessions today. Tests stub StreamEmitter with a recorder
// to assert on event sequencing.
//
// Also handles ToolCallTrace DB persistence — the tool-observability
// table opencode populates for every call. We replicate here so audit
// queries don't need two different code paths depending on which
// runtime produced the row.

import (
	"encoding/json"
	"log"
	"time"

	"github.com/a3c/platform/internal/model"
)

// StreamEmitter is the callback the runner uses to push events
// upstream. Signature matches service.SSEManager.BroadcastToProject
// minus the targetAgentID parameter (we always broadcast to every
// connected client of the project — no per-agent filtering).
//
// Wired in cmd/server/main.go at startup. Default is a no-op so
// tests don't need to set it unless they want to observe events.
var StreamEmitter func(projectID, eventType string, payload map[string]interface{}) = noopEmitter

func noopEmitter(projectID, eventType string, payload map[string]interface{}) {}

// Event-type constants. The first two mirror opencode's wire names
// so the existing frontend renders native-runtime sessions without
// any UI changes. The rest are new to the native runtime — frontend
// handlers that don't know about them just drop them, which is the
// right default behaviour.
const (
	// EventChatUpdate carries the full assembled assistant reply at
	// end-of-turn. Shape: {role: "agent", content: string}. Matches
	// opencode's CHAT_UPDATE so the existing chat panel renders it.
	EventChatUpdate = "CHAT_UPDATE"

	// EventToolCall fires once per tool invocation. Shape:
	// {session_id, tool, args}. Matches opencode's TOOL_CALL.
	EventToolCall = "TOOL_CALL"

	// EventAgentTextDelta streams token-level text as it arrives.
	// Shape: {session_id, delta}. Native-runtime only — opencode
	// never emitted this. Frontend can subscribe to show a live
	// typewriter effect; clients that don't handle it see the final
	// CHAT_UPDATE at end-of-turn and render the same text once.
	EventAgentTextDelta = "AGENT_TEXT_DELTA"

	// EventAgentTurn is a per-iteration summary of the loop.
	// Shape: {session_id, iteration, input_tokens, output_tokens,
	// tool_count}. Useful for dashboards that want to plot progress.
	EventAgentTurn = "AGENT_TURN"

	// EventAgentDone fires exactly once when Run() exits cleanly.
	// Shape: {session_id, iterations, input_tokens, output_tokens,
	// cost_usd}. Gives the frontend a "run finished" signal so it
	// can flip a session card from "running" to "complete" without
	// polling.
	EventAgentDone = "AGENT_DONE"

	// EventAgentError fires when the loop aborts mid-run.
	// Shape: {session_id, error}. Frontend shows an error banner.
	EventAgentError = "AGENT_ERROR"
)

// emit is the internal helper every loop event funnels through.
// Swallows the emission entirely when projectID is empty (defensive
// — background tasks or stand-alone CLI uses of Run() shouldn't blow
// up just because no dashboard is listening).
func emit(projectID, eventType string, payload map[string]interface{}) {
	if projectID == "" || StreamEmitter == nil {
		return
	}
	StreamEmitter(projectID, eventType, payload)
}

// recordToolCallTrace persists one ToolCallTrace row so the
// observability view shows native-runtime tool calls alongside
// opencode ones. Best-effort: failures log but don't abort the run.
// The summary is clipped to ~500 chars to match opencode's convention.
func recordToolCallTrace(sessionID, projectID, toolName string, input json.RawMessage, result string, success bool) {
	summary := result
	if len(summary) > 500 {
		summary = summary[:500] + "… (truncated)"
	}
	trace := &model.ToolCallTrace{
		ID:            model.GenerateID("trace"),
		SessionID:     sessionID,
		ProjectID:     projectID,
		ToolName:      toolName,
		Args:          string(input),
		ResultSummary: summary,
		Success:       success,
		CreatedAt:     time.Now(),
	}
	// Fire-and-forget. The tool already returned to the model; this
	// is book-keeping that mustn't block the hot path.
	go func() {
		if model.DB == nil {
			return // test env without DB
		}
		if err := model.DB.Create(trace).Error; err != nil {
			log.Printf("[runner/trace] persist failed for %s: %v", toolName, err)
		}
	}()
}
