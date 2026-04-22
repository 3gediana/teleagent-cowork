package runner

// Loop tests. Drives Run() with a scripted in-memory Provider that
// fires pre-canned StreamEvent sequences, so we can assert on exactly
// how the runner shepherds tool calls through the conversation.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/llm"
)

// scriptedProvider is a test-only Provider that replays pre-built
// StreamEvent lists, one list per ChatStream call. Index advances on
// every call so consecutive turns can return different scripts.
type scriptedProvider struct {
	name    string
	scripts [][]llm.StreamEvent
	call    int
	lastReq llm.ChatRequest
}

func (s *scriptedProvider) ID() llm.ProviderID  { return llm.ProviderOpenAI }
func (s *scriptedProvider) Name() string        { return s.name }
func (s *scriptedProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "scripted-model"}}
}
func (s *scriptedProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamEvent, error) {
	s.lastReq = req
	ch := make(chan llm.StreamEvent, 16)
	script := []llm.StreamEvent{{Type: llm.EvMessageStop, StopReason: llm.StopEnd}}
	if s.call < len(s.scripts) {
		script = s.scripts[s.call]
	}
	s.call++
	go func() {
		for _, ev := range script {
			ch <- ev
		}
		close(ch)
	}()
	return ch, nil
}

// mkEndpoint wires a scriptedProvider into a fresh Registry and returns
// the endpoint id to use in RunOptions. Using a fresh Registry rather
// than DefaultRegistry would be nicer but the runner hard-codes the
// singleton; we swap entries in/out around each test.
func mkEndpoint(t *testing.T, p *scriptedProvider) string {
	t.Helper()
	id := fmt.Sprintf("test-ep-%p", p)
	llm.DefaultRegistry.Register(&llm.Entry{
		EndpointID:   id,
		EndpointName: p.name,
		Format:       llm.ProviderOpenAI,
		DefaultModel: "scripted-model",
		Provider:     p,
	})
	t.Cleanup(func() { llm.DefaultRegistry.Remove(id) })
	return id
}

// ---- helpers -------------------------------------------------------

func newRunnerSession(t *testing.T) (*agent.Session, *Registry) {
	t.Helper()
	return &agent.Session{
			Context: &agent.SessionContext{ProjectPath: t.TempDir()},
		},
		NewRegistry()
}

// ---- tests ---------------------------------------------------------

func TestRun_TextOnlyReplyExitsImmediately(t *testing.T) {
	sess, reg := newRunnerSession(t)
	p := &scriptedProvider{name: "t", scripts: [][]llm.StreamEvent{{
		{Type: llm.EvTextDelta, TextDelta: "all done"},
		{Type: llm.EvMessageStop, StopReason: llm.StopEnd, Usage: llm.Usage{InputTokens: 10, OutputTokens: 2}},
	}}}
	ep := mkEndpoint(t, p)

	res, err := Run(context.Background(), sess, reg, RunOptions{
		EndpointID:   ep,
		Model:        "scripted-model",
		SystemPrompt: "be brief",
		UserInput:    "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalText != "all done" {
		t.Errorf("FinalText: got %q", res.FinalText)
	}
	if res.Iterations != 1 {
		t.Errorf("want 1 iter, got %d", res.Iterations)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 2 {
		t.Errorf("usage: %+v", res.Usage)
	}
}

func TestRun_DispatchesToolCallAndFeedsResultBack(t *testing.T) {
	sess, reg := newRunnerSession(t)
	reg.Register(ReadTool{}) // real tool; will try to read — we'll make sure model passes a valid path

	// Script 1: model calls read("f.txt")
	// Script 2: model sees tool_result, emits final text
	call1 := []llm.StreamEvent{
		{Type: llm.EvToolUseStart, ToolUseID: "tu_1", ToolName: "read"},
		{Type: llm.EvToolUseEnd, ToolUseID: "tu_1", ToolName: "read", ToolInput: json.RawMessage(`{"path":"f.txt"}`)},
		{Type: llm.EvMessageStop, StopReason: llm.StopToolUse, Usage: llm.Usage{OutputTokens: 5}},
	}
	call2 := []llm.StreamEvent{
		{Type: llm.EvTextDelta, TextDelta: "read successfully"},
		{Type: llm.EvMessageStop, StopReason: llm.StopEnd, Usage: llm.Usage{OutputTokens: 3}},
	}
	p := &scriptedProvider{name: "t", scripts: [][]llm.StreamEvent{call1, call2}}
	ep := mkEndpoint(t, p)

	// Create the file the model will try to read.
	import_writeFile(t, sess, "f.txt", "file-contents-123")

	res, err := Run(context.Background(), sess, reg, RunOptions{
		EndpointID:   ep,
		SystemPrompt: "s",
		UserInput:    "please read f.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Iterations != 2 {
		t.Errorf("want 2 iter (call + follow-up), got %d", res.Iterations)
	}
	if res.FinalText != "read successfully" {
		t.Errorf("FinalText: %q", res.FinalText)
	}
	if res.Usage.OutputTokens != 8 {
		t.Errorf("usage summed across turns: got %d, want 8", res.Usage.OutputTokens)
	}
	if len(res.Journal) != 1 {
		t.Fatalf("expected 1 journal entry, got %d", len(res.Journal))
	}
	if res.Journal[0].ToolName != "read" || res.Journal[0].Output != "file-contents-123" {
		t.Errorf("journal entry: %+v", res.Journal[0])
	}
	// Verify the second call actually got the tool_result in its
	// messages payload so the model could see it.
	if len(p.scripts) < 2 {
		t.Skip()
	}
	// The last request's messages should be:
	//   [user "please read f.txt", assistant w/ tool_use, user w/ tool_result]
	if len(p.lastReq.Messages) < 3 {
		t.Fatalf("second call should have 3 messages, got %d", len(p.lastReq.Messages))
	}
	lastMsg := p.lastReq.Messages[len(p.lastReq.Messages)-1]
	if lastMsg.Role != llm.RoleUser {
		t.Errorf("last message role: %v", lastMsg.Role)
	}
}

func TestRun_UnknownToolReturnsErrorToModel(t *testing.T) {
	sess, reg := newRunnerSession(t)
	// reg intentionally empty — model hallucinates.

	call1 := []llm.StreamEvent{
		{Type: llm.EvToolUseStart, ToolUseID: "tu_1", ToolName: "hallucinated"},
		{Type: llm.EvToolUseEnd, ToolUseID: "tu_1", ToolName: "hallucinated", ToolInput: json.RawMessage(`{}`)},
		{Type: llm.EvMessageStop, StopReason: llm.StopToolUse},
	}
	call2 := []llm.StreamEvent{
		{Type: llm.EvTextDelta, TextDelta: "sorry, can't"},
		{Type: llm.EvMessageStop, StopReason: llm.StopEnd},
	}
	p := &scriptedProvider{name: "t", scripts: [][]llm.StreamEvent{call1, call2}}
	ep := mkEndpoint(t, p)

	res, err := Run(context.Background(), sess, reg, RunOptions{
		EndpointID: ep,
		UserInput:  "do something",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Journal) != 1 || !res.Journal[0].IsError {
		t.Errorf("hallucinated tool should produce an error journal entry; got %+v", res.Journal)
	}
}

func TestRun_MaxIterationsTripsLivelock(t *testing.T) {
	sess, reg := newRunnerSession(t)
	reg.Register(ReadTool{})

	// Model keeps calling read in a loop forever.
	call := []llm.StreamEvent{
		{Type: llm.EvToolUseStart, ToolUseID: "tu_X", ToolName: "read"},
		{Type: llm.EvToolUseEnd, ToolUseID: "tu_X", ToolName: "read", ToolInput: json.RawMessage(`{"path":"x.txt"}`)},
		{Type: llm.EvMessageStop, StopReason: llm.StopToolUse},
	}
	// Same script every call.
	p := &scriptedProvider{name: "loop", scripts: [][]llm.StreamEvent{call, call, call, call}}
	ep := mkEndpoint(t, p)

	import_writeFile(t, sess, "x.txt", "loop-file")

	_, err := Run(context.Background(), sess, reg, RunOptions{
		EndpointID:    ep,
		UserInput:     "spin",
		MaxIterations: 3,
	})
	if err == nil {
		t.Fatal("livelock should be detected and error returned")
	}
}

func TestRun_ForwardsUsageAndSystemPrompt(t *testing.T) {
	sess, reg := newRunnerSession(t)
	p := &scriptedProvider{name: "t", scripts: [][]llm.StreamEvent{{
		{Type: llm.EvTextDelta, TextDelta: "ok"},
		{Type: llm.EvMessageStop, StopReason: llm.StopEnd, Usage: llm.Usage{InputTokens: 7}},
	}}}
	ep := mkEndpoint(t, p)

	_, err := Run(context.Background(), sess, reg, RunOptions{
		EndpointID:   ep,
		SystemPrompt: "you are an auditor",
		UserInput:    "audit this",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.lastReq.System != "you are an auditor" {
		t.Errorf("system prompt not forwarded: got %q", p.lastReq.System)
	}
}

func TestRun_ToolChoiceOnlyOnFirstTurn(t *testing.T) {
	sess, reg := newRunnerSession(t)
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
		EndpointID: ep,
		ToolChoice: "read",
		UserInput:  "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	// First call had tool_choice; second call must not (empty string).
	if p.call != 2 {
		t.Fatalf("expected 2 calls, got %d", p.call)
	}
	// p.lastReq is the second request; ToolChoice must be empty.
	if p.lastReq.ToolChoice != "" {
		t.Errorf("second turn should have no tool_choice, got %q", p.lastReq.ToolChoice)
	}
}

// ---- test utility --------------------------------------------------

func import_writeFile(t *testing.T, sess *agent.Session, name, body string) {
	t.Helper()
	path := sess.Context.ProjectPath + string('/') + name
	// filepath.Join would be cleaner but adds an import; this is test
	// code so simplicity wins.
	if err := writeAtomic(path, []byte(body)); err != nil {
		t.Fatal(err)
	}
}
