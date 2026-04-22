package llm

// Minimal server-sent-events decoder shared by every streaming
// provider. We could pull in an external lib (r3labs/sse, launchdarkly)
// but SSE is a tiny grammar — bufio.Scanner with a custom split is
// enough for correctness *and* leaves us in control of back-pressure.

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// SSEEvent is one event dispatched off the stream. Both fields can
// coexist on one event; providers typically use only one at a time.
type SSEEvent struct {
	// Event is the value of the `event:` line, if any. Anthropic sends
	// it; OpenAI does not (it signals event type inside the JSON data).
	Event string
	// Data is the concatenated `data:` lines with the leading spaces
	// stripped and multi-line payloads joined with a single LF (per
	// the SSE spec). For JSON streams this is the raw JSON body.
	Data string
}

// SSEReader wraps an io.Reader (typically the HTTP response body) and
// yields one SSEEvent at a time.
//
// Allocation profile: one SSEReader reuses a 64 KiB bufio scanner
// buffer; per-event it allocates two short strings. Adequate for the
// bursty, not-latency-critical nature of LLM streams.
type SSEReader struct {
	scanner *bufio.Scanner
	// buf accumulates lines of one event until we hit a blank line
	// (the event terminator in SSE grammar).
	buf    bytes.Buffer
	evName string
}

func NewSSEReader(r io.Reader) *SSEReader {
	s := bufio.NewScanner(r)
	// Default Scanner buffer is 64 KiB which is too small for some
	// providers that batch deltas into 128 KiB+ JSON blobs. Grow to
	// 1 MiB — plenty for any realistic tool-call argument payload.
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	return &SSEReader{scanner: s}
}

// Next advances to the next event. Returns (nil, io.EOF) when the
// stream ends cleanly, or a real error on transport failure.
// The returned *SSEEvent is owned by the caller — its string fields
// are copies, safe to retain after the next Next() call.
func (r *SSEReader) Next() (*SSEEvent, error) {
	r.buf.Reset()
	r.evName = ""

	for r.scanner.Scan() {
		line := r.scanner.Text()

		// A blank line terminates the current event. If we have
		// accumulated data, emit it; otherwise skip (stream of
		// standalone blank lines is a legitimate heartbeat pattern).
		if line == "" {
			if r.buf.Len() == 0 && r.evName == "" {
				continue
			}
			return &SSEEvent{Event: r.evName, Data: r.buf.String()}, nil
		}

		// Lines starting with ':' are comments per the SSE spec.
		// OpenAI uses ": OPENROUTER PROCESSING" as keepalive. Ignore.
		if strings.HasPrefix(line, ":") {
			continue
		}

		// "field: value" lines — we care about `event` and `data`.
		// Everything else (id, retry) we silently drop; no LLM provider
		// uses them meaningfully today.
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			// Field-only line with no value; SSE permits this but no
			// provider we care about sends it.
			continue
		}
		field := line[:idx]
		value := line[idx+1:]
		// Per spec, strip ONE leading space from the value; real
		// providers always add exactly one.
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}

		switch field {
		case "event":
			r.evName = value
		case "data":
			// Multiple data lines concatenate with a single LF.
			if r.buf.Len() > 0 {
				r.buf.WriteByte('\n')
			}
			r.buf.WriteString(value)
		}
	}

	if err := r.scanner.Err(); err != nil {
		return nil, err
	}
	// Scanner.Scan returned false with no error → EOF.
	// If we had a partial event buffered, flush it — some providers
	// (notably OpenAI when the connection drops cleanly mid-event) will
	// leave the final [DONE] without a trailing blank line.
	if r.buf.Len() > 0 || r.evName != "" {
		ev := &SSEEvent{Event: r.evName, Data: r.buf.String()}
		r.buf.Reset()
		r.evName = ""
		return ev, nil
	}
	return nil, io.EOF
}
