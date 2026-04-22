package llm

// Anthropic adapter tests using httptest. No network; no real API key.
// Fixtures use the canonical SSE shape Anthropic's docs describe.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serveSSE returns an http.Handler that writes a canned SSE body and
// closes. Used as the provider endpoint for unit tests.
func serveSSE(body string, captureReq *[]byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captureReq != nil {
			b, _ := io.ReadAll(r.Body)
			*captureReq = b
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte(body))
	})
}

func TestAnthropic_BuildsCorrectRequestBody(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(serveSSE("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", &captured))
	defer srv.Close()

	p := NewAnthropicProvider(ProviderConfig{
		APIKey: "test-key", BaseURL: srv.URL,
		Models: []ModelInfo{{ID: "claude-sonnet-4-5-20250929"}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := p.ChatStream(ctx, ChatRequest{
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 1024,
		System:    "you are a helpful coder",
		Messages:  []Message{NewUserText("hello")},
		Tools: []ToolDef{{
			Name: "ping", Description: "probe",
			Schema: map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	// Drain channel so server handler completes before we inspect.
	for range ch {
	}

	var body map[string]any
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	// System must be promoted to top-level (array form, cache_control).
	sys, ok := body["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("expected system array, got %T %v", body["system"], body["system"])
	}
	// Tools must be in the body.
	if _, ok := body["tools"]; !ok {
		t.Error("tools missing from request body")
	}
	// Stream must be true.
	if s, _ := body["stream"].(bool); !s {
		t.Error("stream=true expected")
	}
}

func TestAnthropic_StreamsTextDelta(t *testing.T) {
	body := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello \"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"world\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":0,\"output_tokens\":3}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	srv := httptest.NewServer(serveSSE(body, nil))
	defer srv.Close()
	p := NewAnthropicProvider(ProviderConfig{APIKey: "k", BaseURL: srv.URL})
	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "claude-sonnet-4-5-20250929", MaxTokens: 100, Messages: []Message{NewUserText("hi")}})
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
		t.Errorf("assembled text: got %q want %q", text.String(), "Hello world")
	}
	if stop.StopReason != StopEnd {
		t.Errorf("stop reason: got %v want %v", stop.StopReason, StopEnd)
	}
	if stop.Usage.InputTokens != 10 || stop.Usage.OutputTokens != 3 {
		t.Errorf("usage: got %+v", stop.Usage)
	}
}

func TestAnthropic_ReassemblesToolUseInput(t *testing.T) {
	body := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m2\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_abc\",\"name\":\"read_file\",\"input\":{}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"main.go\\\"}\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	srv := httptest.NewServer(serveSSE(body, nil))
	defer srv.Close()
	p := NewAnthropicProvider(ProviderConfig{APIKey: "k", BaseURL: srv.URL})
	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "claude-sonnet-4-5-20250929", MaxTokens: 50, Messages: []Message{NewUserText("read main.go")}})
	if err != nil {
		t.Fatal(err)
	}

	var startCount, endCount int
	var endInput string
	var stop StreamEvent
	for ev := range ch {
		switch ev.Type {
		case EvToolUseStart:
			startCount++
			if ev.ToolName != "read_file" {
				t.Errorf("tool name: got %q want read_file", ev.ToolName)
			}
		case EvToolUseEnd:
			endCount++
			endInput = string(ev.ToolInput)
		case EvMessageStop:
			stop = ev
		}
	}
	if startCount != 1 || endCount != 1 {
		t.Errorf("want 1 start and 1 end, got start=%d end=%d", startCount, endCount)
	}
	// The reassembled input should parse as valid JSON with path field.
	var got map[string]string
	if err := json.Unmarshal([]byte(endInput), &got); err != nil {
		t.Fatalf("assembled input not JSON: %v (got %q)", err, endInput)
	}
	if got["path"] != "main.go" {
		t.Errorf("input.path: got %q want main.go", got["path"])
	}
	if stop.StopReason != StopToolUse {
		t.Errorf("stop_reason: got %v want StopToolUse", stop.StopReason)
	}
}

func TestAnthropic_ErrorEventTerminates(t *testing.T) {
	body := "event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"retry later\"}}\n\n"
	srv := httptest.NewServer(serveSSE(body, nil))
	defer srv.Close()
	p := NewAnthropicProvider(ProviderConfig{APIKey: "k", BaseURL: srv.URL})
	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "claude-sonnet-4-5-20250929", MaxTokens: 10, Messages: []Message{NewUserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var sawErr bool
	for ev := range ch {
		if ev.Type == EvError {
			sawErr = true
			if !strings.Contains(ev.Err.Error(), "retry later") {
				t.Errorf("error not wrapped: %v", ev.Err)
			}
		}
	}
	if !sawErr {
		t.Error("expected EvError event")
	}
}

func TestAnthropic_NonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()
	p := NewAnthropicProvider(ProviderConfig{APIKey: "wrong", BaseURL: srv.URL, MaxRetries: 1})
	_, err := p.ChatStream(context.Background(), ChatRequest{Model: "claude-sonnet-4-5-20250929", MaxTokens: 10, Messages: []Message{NewUserText("hi")}})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error message should include status 401, got %v", err)
	}
}

func TestAnthropic_ReasoningPromotesToThinkingBudget(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(serveSSE("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", &captured))
	defer srv.Close()
	p := NewAnthropicProvider(ProviderConfig{APIKey: "k", BaseURL: srv.URL})
	ch, _ := p.ChatStream(context.Background(), ChatRequest{
		Model: "claude-sonnet-4-5-20250929", MaxTokens: 4096,
		Messages: []Message{NewUserText("plan")},
		Reasoning: ReasoningHigh,
	})
	for range ch {
	}
	var body map[string]any
	_ = json.Unmarshal(captured, &body)
	th, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking block missing; body: %s", string(captured))
	}
	if th["type"] != "enabled" {
		t.Errorf("thinking.type: %v", th["type"])
	}
	// Temperature must be dropped when thinking is enabled.
	if _, has := body["temperature"]; has {
		t.Error("temperature should be omitted when thinking is on")
	}
}
