package llm

// OpenAI-compatible adapter tests. Fixtures modelled after real MiniMax +
// OpenAI streaming payloads — key edge is per-index tool_call fragment
// reassembly.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAI_BuildsChatCompletionsURL(t *testing.T) {
	// Providers pass either a /v1 root or the full URL; both must land
	// on /chat/completions.
	p := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: "https://api.minimaxi.com/v1"})
	if !strings.HasSuffix(p.apiURL, "/chat/completions") {
		t.Errorf("apiURL: got %q", p.apiURL)
	}
	p2 := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: "https://api.minimaxi.com/v1/chat/completions"})
	if p2.apiURL != "https://api.minimaxi.com/v1/chat/completions" {
		t.Errorf("apiURL: got %q", p2.apiURL)
	}
}

func TestOpenAI_SystemFoldsIntoMessagesArray(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(serveSSE("data: [DONE]\n\n", &captured))
	defer srv.Close()
	p := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: srv.URL})
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model: "gpt-4o", MaxTokens: 50,
		System:   "be terse",
		Messages: []Message{NewUserText("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	var body map[string]any
	_ = json.Unmarshal(captured, &body)
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user); got %v", msgs)
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be terse" {
		t.Errorf("system message wrong: %+v", first)
	}
}

func TestOpenAI_StreamsContentDeltas(t *testing.T) {
	body := `data: {"choices":[{"delta":{"role":"assistant","content":""},"index":0}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"Hello "},"index":0}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"world"},"index":0}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}` + "\n\n" +
		`data: {"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10},"choices":[]}` + "\n\n" +
		`data: [DONE]` + "\n\n"
	srv := httptest.NewServer(serveSSE(body, nil))
	defer srv.Close()
	p := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: srv.URL})
	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "gpt-4o", MaxTokens: 100, Messages: []Message{NewUserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	var stop StreamEvent
	for ev := range ch {
		if ev.Type == EvTextDelta {
			text.WriteString(ev.TextDelta)
		}
		if ev.Type == EvMessageStop {
			stop = ev
		}
	}
	if text.String() != "Hello world" {
		t.Errorf("got %q", text.String())
	}
	if stop.StopReason != StopEnd {
		t.Errorf("stop: got %v", stop.StopReason)
	}
	if stop.Usage.InputTokens != 8 || stop.Usage.OutputTokens != 2 {
		t.Errorf("usage: %+v", stop.Usage)
	}
}

func TestOpenAI_ReassemblesToolCallFragments(t *testing.T) {
	// Classic OpenAI tool-call streaming: id + name in first chunk,
	// arguments split across many events.
	body := `data: {"choices":[{"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_xyz","type":"function","function":{"name":"read_file","arguments":""}}]},"index":0}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]},"index":0}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\""}}]},"index":0}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"main.go\"}"}}]},"index":0}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}` + "\n\n" +
		`data: [DONE]` + "\n\n"
	srv := httptest.NewServer(serveSSE(body, nil))
	defer srv.Close()
	p := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: srv.URL})
	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "gpt-4o", MaxTokens: 50, Messages: []Message{NewUserText("go")}})
	if err != nil {
		t.Fatal(err)
	}
	var start, end int
	var input string
	var stop StreamEvent
	for ev := range ch {
		switch ev.Type {
		case EvToolUseStart:
			start++
			if ev.ToolName != "read_file" || ev.ToolUseID != "call_xyz" {
				t.Errorf("start fields: name=%q id=%q", ev.ToolName, ev.ToolUseID)
			}
		case EvToolUseEnd:
			end++
			input = string(ev.ToolInput)
		case EvMessageStop:
			stop = ev
		}
	}
	if start != 1 || end != 1 {
		t.Errorf("start=%d end=%d (want 1/1)", start, end)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		t.Fatalf("reassembled input not JSON: %v (got %q)", err, input)
	}
	if parsed["path"] != "main.go" {
		t.Errorf("path: got %q", parsed["path"])
	}
	if stop.StopReason != StopToolUse {
		t.Errorf("stop: %v", stop.StopReason)
	}
}

func TestOpenAI_MiniMaxReasoningContentPromotesToThinking(t *testing.T) {
	// DeepSeek R1 / MiniMax emit reasoning_content deltas for their
	// thinking stream. Our adapter forwards them as EvThinkingDelta.
	body := `data: {"choices":[{"delta":{"role":"assistant","reasoning_content":"Let me think..."}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"reasoning_content":" step by step"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"answer"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: [DONE]` + "\n\n"
	srv := httptest.NewServer(serveSSE(body, nil))
	defer srv.Close()
	p := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: srv.URL})
	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "MiniMax-M2.7", MaxTokens: 100, Messages: []Message{NewUserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var thinking bytes.Buffer
	for ev := range ch {
		if ev.Type == EvThinkingDelta {
			thinking.WriteString(ev.TextDelta)
		}
	}
	if thinking.String() != "Let me think... step by step" {
		t.Errorf("thinking: got %q", thinking.String())
	}
}

func TestOpenAI_ToolRoleGetsOwnEntry(t *testing.T) {
	// Tool results must be flattened to role=tool messages, not nested
	// inside a user message — OpenAI rejects nested shapes.
	var captured []byte
	srv := httptest.NewServer(serveSSE("data: [DONE]\n\n", &captured))
	defer srv.Close()
	p := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: srv.URL})
	ch, _ := p.ChatStream(context.Background(), ChatRequest{
		Model: "gpt-4o", MaxTokens: 10,
		Messages: []Message{
			NewUserText("call the tool"),
			{Role: RoleAssistant, Content: []ContentBlock{
				NewTextBlock("sure"),
				NewToolUseBlock("call_1", "read", json.RawMessage(`{"path":"a.go"}`)),
			}},
			{Role: RoleUser, Content: []ContentBlock{
				NewToolResultBlock("call_1", "file contents", false),
				NewTextBlock("anything else?"),
			}},
		},
	})
	for range ch {
	}
	var body struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id,omitempty"`
			ToolCalls  []any  `json:"tool_calls,omitempty"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(captured, &body)

	// Expected role sequence: user (call the tool), assistant (sure + tool_calls), tool (result), user (anything else).
	roles := make([]string, 0, len(body.Messages))
	for _, m := range body.Messages {
		roles = append(roles, m.Role)
	}
	want := []string{"user", "assistant", "tool", "user"}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Errorf("roles: got %v want %v", roles, want)
	}
	// Tool message must carry tool_call_id.
	for _, m := range body.Messages {
		if m.Role == "tool" && m.ToolCallID != "call_1" {
			t.Errorf("tool_call_id missing or wrong: %+v", m)
		}
	}
	// Assistant turn must carry tool_calls with the right id.
	for _, m := range body.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) == 0 {
			t.Errorf("assistant tool_calls missing")
		}
	}
}

func TestOpenAI_401ErrorNoRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"invalid"}}`)
	}))
	defer srv.Close()
	p := NewOpenAIProvider(ProviderConfig{APIKey: "bad", BaseURL: srv.URL, MaxRetries: 3})
	_, err := p.ChatStream(context.Background(), ChatRequest{Model: "gpt-4o", MaxTokens: 10, Messages: []Message{NewUserText("hi")}})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("401 should not retry; got %d calls", calls)
	}
}
