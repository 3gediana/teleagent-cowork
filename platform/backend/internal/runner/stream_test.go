package runner

// Stream emission tests. Verify that every runner path fires the
// right SSE events with the right payloads. These are the
// frontend's contract — breaking the event shape means silently
// breaking the dashboard.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/llm"
)

// eventRecorder captures every StreamEmitter invocation, in order.
type eventRecorder struct {
	mu     sync.Mutex
	Events []recordedEvent
}

type recordedEvent struct {
	ProjectID string
	Type      string
	Payload   map[string]interface{}
}

func (r *eventRecorder) Emit(projectID, eventType string, payload map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Events = append(r.Events, recordedEvent{projectID, eventType, payload})
}

func (r *eventRecorder) byType(t string) []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []recordedEvent{}
	for _, e := range r.Events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// installRecorder swaps StreamEmitter for the test's duration. Safe
// to compose with other helpers that install sinks — they key off
// different globals.
func installRecorder(t *testing.T) *eventRecorder {
	t.Helper()
	prev := StreamEmitter
	rec := &eventRecorder{}
	StreamEmitter = rec.Emit
	t.Cleanup(func() { StreamEmitter = prev })
	return rec
}

// newRunnerSessionWithProject extends newRunnerSession (loop_test.go)
// by populating ProjectID so stream events actually fire. A session
// without a ProjectID is legitimate (standalone CLI invocations, unit
// tests) and simply emits nothing — verified by TestStream_NoProjectIDSilent.
func newRunnerSessionWithProject(t *testing.T, projectID string) (*agent.Session, *Registry) {
	t.Helper()
	sess, reg := newRunnerSession(t)
	sess.ID = "sess-" + t.Name()
	sess.ProjectID = projectID
	return sess, reg
}

// ---- tests ---------------------------------------------------------

func TestStream_EmitsChatUpdateAndAgentDoneOnTextOnlyReply(t *testing.T) {
	rec := installRecorder(t)
	sess, reg := newRunnerSessionWithProject(t, "proj-xyz")
	p := &scriptedProvider{name: "t", scripts: [][]llm.StreamEvent{{
		{Type: llm.EvTextDelta, TextDelta: "hel"},
		{Type: llm.EvTextDelta, TextDelta: "lo"},
		{Type: llm.EvMessageStop, StopReason: llm.StopEnd, Usage: llm.Usage{InputTokens: 4, OutputTokens: 2}},
	}}}
	ep := mkEndpoint(t, p)

	_, err := Run(context.Background(), sess, reg, RunOptions{
		EndpointID: ep, UserInput: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Token-level deltas should have been forwarded verbatim.
	deltas := rec.byType(EventAgentTextDelta)
	if len(deltas) != 2 {
		t.Errorf("want 2 AGENT_TEXT_DELTA, got %d", len(deltas))
	}
	if deltas[0].Payload["delta"] != "hel" || deltas[1].Payload["delta"] != "lo" {
		t.Errorf("delta payloads wrong: %+v %+v", deltas[0].Payload, deltas[1].Payload)
	}

	// Exactly one CHAT_UPDATE with the final assembled text.
	chats := rec.byType(EventChatUpdate)
	if len(chats) != 1 {
		t.Fatalf("want 1 CHAT_UPDATE, got %d", len(chats))
	}
	if chats[0].Payload["content"] != "hello" {
		t.Errorf("chat content: got %v", chats[0].Payload["content"])
	}
	if chats[0].Payload["role"] != "agent" {
		t.Errorf("chat role should be 'agent', got %v", chats[0].Payload["role"])
	}

	// One AGENT_DONE at the very end.
	done := rec.byType(EventAgentDone)
	if len(done) != 1 {
		t.Fatalf("want 1 AGENT_DONE, got %d", len(done))
	}
	if got := done[0].Payload["iterations"]; got != 1 {
		t.Errorf("iterations: got %v", got)
	}
	if got := done[0].Payload["output_tokens"]; got != 2 {
		t.Errorf("output_tokens: got %v", got)
	}

	// Every event must carry the same project id as the session.
	for _, e := range rec.Events {
		if e.ProjectID != "proj-xyz" {
			t.Errorf("event %s routed to wrong project: %q", e.Type, e.ProjectID)
		}
	}
}

func TestStream_EmitsToolCallBeforeExecutionAndDoneAfter(t *testing.T) {
	rec := installRecorder(t)
	sess, reg := newRunnerSessionWithProject(t, "proj-1")
	reg.Register(ReadTool{})

	call1 := []llm.StreamEvent{
		{Type: llm.EvToolUseStart, ToolUseID: "tu_1", ToolName: "read"},
		{Type: llm.EvToolUseEnd, ToolUseID: "tu_1", ToolName: "read", ToolInput: json.RawMessage(`{"path":"f.txt"}`)},
		{Type: llm.EvMessageStop, StopReason: llm.StopToolUse},
	}
	call2 := []llm.StreamEvent{
		{Type: llm.EvTextDelta, TextDelta: "done"},
		{Type: llm.EvMessageStop, StopReason: llm.StopEnd},
	}
	p := &scriptedProvider{name: "t", scripts: [][]llm.StreamEvent{call1, call2}}
	ep := mkEndpoint(t, p)
	import_writeFile(t, sess, "f.txt", "x")

	_, err := Run(context.Background(), sess, reg, RunOptions{
		EndpointID: ep, UserInput: "go",
	})
	if err != nil {
		t.Fatal(err)
	}

	tools := rec.byType(EventToolCall)
	if len(tools) != 1 {
		t.Fatalf("want 1 TOOL_CALL, got %d", len(tools))
	}
	if tools[0].Payload["tool"] != "read" {
		t.Errorf("tool name in payload: %v", tools[0].Payload["tool"])
	}
	args, ok := tools[0].Payload["args"].(map[string]interface{})
	if !ok || args["path"] != "f.txt" {
		t.Errorf("tool args malformed: %+v", tools[0].Payload["args"])
	}
	// Two turns → two AGENT_TURN events.
	if got := len(rec.byType(EventAgentTurn)); got != 2 {
		t.Errorf("want 2 AGENT_TURN, got %d", got)
	}
	// One AGENT_DONE at the end, zero errors.
	if got := len(rec.byType(EventAgentDone)); got != 1 {
		t.Errorf("want 1 AGENT_DONE, got %d", got)
	}
	if got := len(rec.byType(EventAgentError)); got != 0 {
		t.Errorf("want 0 AGENT_ERROR, got %d", got)
	}
}

func TestStream_EmitsAgentErrorOnLivelock(t *testing.T) {
	rec := installRecorder(t)
	sess, reg := newRunnerSessionWithProject(t, "proj-err")
	reg.Register(ReadTool{})

	// Model loops forever on the same tool call.
	loopCall := []llm.StreamEvent{
		{Type: llm.EvToolUseStart, ToolUseID: "tu", ToolName: "read"},
		{Type: llm.EvToolUseEnd, ToolUseID: "tu", ToolName: "read", ToolInput: json.RawMessage(`{"path":"x"}`)},
		{Type: llm.EvMessageStop, StopReason: llm.StopToolUse},
	}
	p := &scriptedProvider{name: "loop", scripts: [][]llm.StreamEvent{loopCall, loopCall, loopCall}}
	ep := mkEndpoint(t, p)
	import_writeFile(t, sess, "x", "y")

	_, err := Run(context.Background(), sess, reg, RunOptions{
		EndpointID: ep, UserInput: "spin", MaxIterations: 2,
	})
	if err == nil {
		t.Fatal("expected livelock error")
	}
	errs := rec.byType(EventAgentError)
	if len(errs) != 1 {
		t.Fatalf("want 1 AGENT_ERROR, got %d", len(errs))
	}
	if got, ok := errs[0].Payload["error"].(string); !ok || got == "" {
		t.Errorf("AGENT_ERROR payload missing error field: %+v", errs[0].Payload)
	}
	// NO AGENT_DONE on error paths.
	if got := len(rec.byType(EventAgentDone)); got != 0 {
		t.Errorf("AGENT_DONE must not fire on error paths; got %d", got)
	}
}

func TestStream_NoProjectIDSilent(t *testing.T) {
	// Standalone Run() invocation without a ProjectID (e.g. a CLI
	// harness) must not blow up and must not emit anything.
	rec := installRecorder(t)
	sess, reg := newRunnerSession(t) // no project id
	p := &scriptedProvider{name: "t", scripts: [][]llm.StreamEvent{{
		{Type: llm.EvTextDelta, TextDelta: "ok"},
		{Type: llm.EvMessageStop, StopReason: llm.StopEnd},
	}}}
	ep := mkEndpoint(t, p)

	if _, err := Run(context.Background(), sess, reg, RunOptions{EndpointID: ep, UserInput: "hi"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.Events) != 0 {
		t.Errorf("expected zero events when ProjectID is empty; got %d", len(rec.Events))
	}
}

func TestStream_AgentTurnCarriesIterationAndToolCount(t *testing.T) {
	rec := installRecorder(t)
	sess, reg := newRunnerSessionWithProject(t, "proj-metrics")
	reg.Register(ReadTool{})

	call1 := []llm.StreamEvent{
		{Type: llm.EvToolUseStart, ToolUseID: "tu_1", ToolName: "read"},
		{Type: llm.EvToolUseEnd, ToolUseID: "tu_1", ToolName: "read", ToolInput: json.RawMessage(`{"path":"a"}`)},
		{Type: llm.EvToolUseStart, ToolUseID: "tu_2", ToolName: "read"},
		{Type: llm.EvToolUseEnd, ToolUseID: "tu_2", ToolName: "read", ToolInput: json.RawMessage(`{"path":"b"}`)},
		{Type: llm.EvMessageStop, StopReason: llm.StopToolUse, Usage: llm.Usage{InputTokens: 50, OutputTokens: 20}},
	}
	call2 := []llm.StreamEvent{
		{Type: llm.EvTextDelta, TextDelta: "done"},
		{Type: llm.EvMessageStop, StopReason: llm.StopEnd, Usage: llm.Usage{InputTokens: 60, OutputTokens: 10}},
	}
	p := &scriptedProvider{name: "t", scripts: [][]llm.StreamEvent{call1, call2}}
	ep := mkEndpoint(t, p)
	import_writeFile(t, sess, "a", "x")
	import_writeFile(t, sess, "b", "y")

	if _, err := Run(context.Background(), sess, reg, RunOptions{EndpointID: ep, UserInput: "go"}); err != nil {
		t.Fatal(err)
	}
	turns := rec.byType(EventAgentTurn)
	if len(turns) != 2 {
		t.Fatalf("want 2 AGENT_TURN, got %d", len(turns))
	}
	if got := turns[0].Payload["tool_count"]; got != 2 {
		t.Errorf("turn 1 tool_count: %v", got)
	}
	if got := turns[0].Payload["input_tokens"]; got != 50 {
		t.Errorf("turn 1 input_tokens: %v", got)
	}
	if got := turns[1].Payload["tool_count"]; got != 0 {
		t.Errorf("turn 2 tool_count: %v", got)
	}
	if got := turns[1].Payload["iteration"]; got != 2 {
		t.Errorf("turn 2 iteration: %v", got)
	}
}
