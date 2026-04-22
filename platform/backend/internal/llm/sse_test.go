package llm

// SSE reader unit tests. Fast to run, no network — exercises the
// framing grammar against known-good fixtures for both Anthropic-style
// (event: + data:) and OpenAI-style (data: only) streams.

import (
	"io"
	"strings"
	"testing"
)

func TestSSEReader_AnthropicFraming(t *testing.T) {
	// Two events separated by a blank line; first has `event:`, second
	// doesn't. Third event ends the stream without a trailing blank
	// line — covers the "flush partial on EOF" path.
	raw := "event: message_start\n" +
		"data: {\"type\":\"message_start\"}\n" +
		"\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n" +
		"\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n"

	r := NewSSEReader(strings.NewReader(raw))
	var names []string
	for {
		ev, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		names = append(names, ev.Event)
	}
	want := []string{"message_start", "content_block_delta", "message_stop"}
	if len(names) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(names), len(want), names)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("event %d: got %q want %q", i, n, want[i])
		}
	}
}

func TestSSEReader_OpenAIFraming(t *testing.T) {
	// OpenAI only emits data: lines. Terminal [DONE] is a sentinel —
	// the reader itself doesn't know about it; consumers handle.
	raw := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n" +
		"data: [DONE]\n\n"

	r := NewSSEReader(strings.NewReader(raw))
	var count int
	var last string
	for {
		ev, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if ev.Event != "" {
			t.Errorf("OpenAI frames shouldn't have event names; got %q", ev.Event)
		}
		count++
		last = ev.Data
	}
	if count != 3 {
		t.Errorf("want 3 events, got %d", count)
	}
	if last != "[DONE]" {
		t.Errorf("last event should be [DONE], got %q", last)
	}
}

func TestSSEReader_CommentsAndKeepalive(t *testing.T) {
	// OpenRouter and some OpenAI edge hosts send `: keepalive` lines
	// between real events. These must be dropped without emitting an
	// empty event or confusing the decoder.
	raw := ": OPENROUTER PROCESSING\n" +
		"data: {\"x\":1}\n" +
		"\n" +
		": heartbeat\n" +
		"\n" +
		"data: {\"x\":2}\n" +
		"\n"
	r := NewSSEReader(strings.NewReader(raw))
	var datas []string
	for {
		ev, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		datas = append(datas, ev.Data)
	}
	want := []string{`{"x":1}`, `{"x":2}`}
	if len(datas) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(datas), len(want), datas)
	}
}

func TestSSEReader_MultilineDataJoins(t *testing.T) {
	// SSE spec: multiple data lines join with \n. Rare in LLM streams
	// but covered by the grammar — and `curl`-replayed fixtures often
	// line-wrap long JSON, so we must handle it.
	raw := "data: {\"a\":\n" +
		"data: 1}\n" +
		"\n"
	r := NewSSEReader(strings.NewReader(raw))
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ev.Data != "{\"a\":\n1}" {
		t.Errorf("wrong multiline join: %q", ev.Data)
	}
}

func TestSSEReader_LargePayload(t *testing.T) {
	// Tool calls can carry 100 KiB+ of JSON arguments in one event.
	// Reader must not truncate on the default 64 KiB bufio buffer —
	// we explicitly raised the max-token size to 1 MiB.
	big := strings.Repeat("x", 200*1024)
	raw := "data: " + big + "\n\n"
	r := NewSSEReader(strings.NewReader(raw))
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(ev.Data) != len(big) {
		t.Errorf("payload truncated: got %d bytes, want %d", len(ev.Data), len(big))
	}
}
