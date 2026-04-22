package llm

// Anthropic Messages API adapter.
//
// Why handwritten: the Anthropic streaming protocol is a tight grammar
// (eight event types) and we benefit from passing tool_use content
// through as raw JSON without a round-trip through an SDK's internal
// types. The SDK would also bring in a large surface of unrelated
// helpers we don't need.
//
// Feature coverage:
//   - streaming text (message_start → content_block_delta(text) → message_stop)
//   - streaming thinking (content_block_delta(thinking_delta))
//   - native tool_use (input_json_delta accumulated into complete JSON)
//   - prompt caching (cache_control on system + the last user message)
//   - vision (base64 image blocks)
//   - reasoning budget (maps ReasoningEffort → budget_tokens)
//   - mid-stream error blocks
//   - context cancellation → EvError(ctx.Err) + clean close

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AnthropicProvider is the adapter. One instance per API-key/base-URL
// combination. Provider instances are registered into DefaultRegistry
// at startup.
type AnthropicProvider struct {
	cfg    ProviderConfig
	models []ModelInfo
	apiURL string
	// Version header sent with every request. Pinned to a date that
	// supports all the features we exercise (tool_use, thinking,
	// prompt caching, vision). Bump only with an explicit migration.
	version string
}

// NewAnthropicProvider constructs the adapter. Empty base URL defaults
// to Anthropic's hosted endpoint; non-empty lets us point at Bedrock-
// style proxies or in-house replays for tests.
func NewAnthropicProvider(cfg ProviderConfig) *AnthropicProvider {
	apiURL := cfg.BaseURL
	if apiURL == "" {
		apiURL = "https://api.anthropic.com/v1/messages"
	} else {
		// Allow base URL OR full endpoint URL for flexibility.
		if !strings.HasSuffix(apiURL, "/messages") {
			apiURL = strings.TrimRight(apiURL, "/") + "/messages"
		}
	}
	// Enrich each configured model with builtin pricing/capabilities so
	// operators who only list model IDs still get meaningful metadata
	// in the dashboard.
	models := make([]ModelInfo, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		models = append(models, MergePricing(m))
	}
	if len(models) == 0 {
		// Fall back to the three flagship IDs so registration isn't
		// gated on operators knowing model IDs up front.
		for _, id := range []string{"claude-opus-4-5-20251015", "claude-sonnet-4-5-20250929", "claude-haiku-4-5-20251001"} {
			models = append(models, LookupPricing(id))
		}
	}
	return &AnthropicProvider{
		cfg:     cfg,
		models:  models,
		apiURL:  apiURL,
		version: "2023-06-01",
	}
}

func (p *AnthropicProvider) ID() ProviderID  { return ProviderAnthropic }
func (p *AnthropicProvider) Name() string    { return firstNonEmpty(p.cfg.Name, "Anthropic") }
func (p *AnthropicProvider) Models() []ModelInfo {
	out := make([]ModelInfo, len(p.models))
	copy(out, p.models)
	return out
}

// ChatStream implements Provider. Returns the channel after the HTTP
// stream is open (or the initial POST failed); if the POST itself
// errors, no goroutine is spawned.
func (p *AnthropicProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	reqBody, err := p.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}

	retryPol := DefaultRetryPolicy
	if p.cfg.MaxRetries > 0 {
		retryPol.MaxAttempts = p.cfg.MaxRetries
	}

	httpResp, err := DoWithRetry(ctx, retryPol, func(attempt int) (*http.Response, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL, bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("anthropic-version", p.version)
		// Prompt caching is a beta header as of 2025-Q1. Safe to send
		// even when cache_control isn't used — the server just ignores.
		httpReq.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
		httpReq.Header.Set("x-api-key", p.cfg.APIKey)
		return sharedHTTPClient.Do(httpReq)
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: POST: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		defer httpResp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024))
		return nil, fmt.Errorf("anthropic: status %d: %s", httpResp.StatusCode, string(body))
	}

	// Buffered so a fast producer doesn't block the decode goroutine
	// on consumer pauses. Size is a guess — LLM streams deliver
	// deltas at ~50–200 events/s peak, consumers can usually keep up.
	ch := make(chan StreamEvent, 64)
	modelInfo := p.modelInfoFor(req.Model)

	go p.decodeStream(ctx, httpResp.Body, ch, modelInfo)
	return ch, nil
}

// buildRequestBody serializes our ChatRequest into Anthropic's JSON
// shape. The mapping is mostly 1:1; the interesting choices are:
//   - system: extracted to top-level field (Anthropic's spec requires
//     it there, not in messages[])
//   - prompt caching: cache_control:{type:ephemeral} on system and on
//     the last user message — matches Anthropic's recommended pattern
//     for iterative coding assistants
//   - thinking: reasoning effort mapped to concrete budget_tokens
func (p *AnthropicProvider) buildRequestBody(req ChatRequest) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("anthropic: ChatRequest.Model is required")
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}

	body := map[string]any{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"stream":     true,
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if req.TopP > 0 {
		body["top_p"] = req.TopP
	}
	if len(req.StopSeqs) > 0 {
		body["stop_sequences"] = req.StopSeqs
	}
	if req.UserID != "" {
		body["metadata"] = map[string]string{"user_id": req.UserID}
	}

	// System: send as cached content block so repeated turns reuse
	// the same cache entry instead of re-billing tokens per call.
	if strings.TrimSpace(req.System) != "" {
		body["system"] = []map[string]any{
			{
				"type":          "text",
				"text":          req.System,
				"cache_control": map[string]string{"type": "ephemeral"},
			},
		}
	}

	// Messages: convert our unified shape.
	msgs, err := p.encodeMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	body["messages"] = msgs

	// Tools.
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.Schema,
			})
		}
		body["tools"] = tools

		// tool_choice mapping.
		switch req.ToolChoice {
		case "", "auto":
			body["tool_choice"] = map[string]string{"type": "auto"}
		case "any":
			body["tool_choice"] = map[string]string{"type": "any"}
		case "none":
			// Anthropic models "none" by simply omitting tools, but we
			// already included them for schema coverage. Explicit auto
			// with no tools is the closest; this branch is rare.
			delete(body, "tools")
		default:
			body["tool_choice"] = map[string]any{"type": "tool", "name": req.ToolChoice}
		}
	}

	// Reasoning / thinking.
	if req.Reasoning != ReasoningOff {
		budget := reasoningBudget(req.Reasoning, req.MaxTokens)
		body["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": budget,
		}
		// Per API spec, temperature must be 1 or unset when thinking
		// is on. Drop our override silently — warning here would be
		// noise.
		delete(body, "temperature")
	}

	return json.Marshal(body)
}

// encodeMessages converts unified Messages to Anthropic's array.
// Assistant tool_use blocks are emitted verbatim; tool_result blocks
// are always embedded inside a user-role message per Anthropic spec.
func (p *AnthropicProvider) encodeMessages(in []Message) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(in))
	lastUserIdx := -1
	for i, m := range in {
		if m.Role == RoleUser {
			lastUserIdx = i
		}
	}
	for i, m := range in {
		if m.Role == RoleSystem {
			// System should have been passed via the top-level field.
			// Skip rather than error so callers can pass a uniform
			// Messages slice regardless of provider.
			continue
		}
		content := make([]map[string]any, 0, len(m.Content))
		for _, b := range m.Content {
			block, err := encodeAnthropicBlock(b)
			if err != nil {
				return nil, err
			}
			content = append(content, block)
		}
		role := string(m.Role)
		if m.Role == RoleTool {
			role = "user" // tool results are user-turn content
		}
		// Cache the last user message to reuse KV cache across calls.
		if i == lastUserIdx && len(content) > 0 && role == "user" {
			last := content[len(content)-1]
			last["cache_control"] = map[string]string{"type": "ephemeral"}
		}
		out = append(out, map[string]any{"role": role, "content": content})
	}
	return out, nil
}

func encodeAnthropicBlock(b ContentBlock) (map[string]any, error) {
	switch b.Type {
	case BlockText:
		return map[string]any{"type": "text", "text": b.Text}, nil
	case BlockThinking:
		// Replayed thinking blocks keep the model's own trace intact.
		// Required field is `signature` which the API emits on the way
		// out; we preserve via the Text field if we captured it.
		return map[string]any{"type": "thinking", "thinking": b.Text}, nil
	case BlockToolUse:
		var input any
		if len(b.ToolInput) > 0 {
			if err := json.Unmarshal(b.ToolInput, &input); err != nil {
				return nil, fmt.Errorf("tool_use %q: bad input JSON: %w", b.ToolName, err)
			}
		} else {
			input = map[string]any{}
		}
		return map[string]any{
			"type":  "tool_use",
			"id":    b.ToolUseID,
			"name":  b.ToolName,
			"input": input,
		}, nil
	case BlockToolResult:
		m := map[string]any{
			"type":         "tool_result",
			"tool_use_id":  b.ToolUseID,
			"content":      b.ToolResult,
		}
		if b.IsError {
			m["is_error"] = true
		}
		return m, nil
	case BlockImage:
		return map[string]any{
			"type": "image",
			"source": map[string]string{
				"type":       "base64",
				"media_type": b.ImageMediaType,
				"data":       b.ImageData,
			},
		}, nil
	}
	return nil, fmt.Errorf("anthropic: unsupported content block type %q", b.Type)
}

// reasoningBudget picks a thinking token budget. The ceiling is 80% of
// max_tokens — leaves room for the actual answer.
func reasoningBudget(r ReasoningEffort, maxTokens int) int {
	max := maxTokens * 4 / 5
	if max < 1024 {
		max = 1024
	}
	switch r {
	case ReasoningLow:
		if v := min(max, 2048); v > 1024 {
			return v
		}
		return 1024
	case ReasoningMedium:
		return min(max, 8192)
	case ReasoningHigh:
		return min(max, 32768)
	}
	return 0
}

// modelInfoFor picks the ModelInfo matching a runtime request.Model.
// Falls back to builtin pricing if the adapter wasn't configured with
// this model explicitly — keeps cost tracking honest across dashboard-
// added models the operator hasn't listed in config.yaml yet.
func (p *AnthropicProvider) modelInfoFor(id string) ModelInfo {
	for _, m := range p.models {
		if m.ID == id {
			return m
		}
	}
	return MergePricing(ModelInfo{ID: id})
}

// --- Stream decoding -----------------------------------------------------

// anthropicEventHeader is the shape shared by every event frame (each
// `data:` line is JSON starting with {"type": "..."}).
type anthropicEventHeader struct {
	Type string `json:"type"`
}

type anthropicMessageStart struct {
	Message struct {
		ID    string         `json:"id"`
		Model string         `json:"model"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
}

type anthropicUsage struct {
	InputTokens             int `json:"input_tokens"`
	OutputTokens            int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type anthropicBlockStart struct {
	Index        int                    `json:"index"`
	ContentBlock anthropicContentBlock  `json:"content_block"`
}

type anthropicContentBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Text  string          `json:"text,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicBlockDelta struct {
	Index int                  `json:"index"`
	Delta anthropicDeltaInner  `json:"delta"`
}

type anthropicDeltaInner struct {
	Type         string `json:"type"`
	Text         string `json:"text,omitempty"`
	PartialJSON  string `json:"partial_json,omitempty"`
	Thinking     string `json:"thinking,omitempty"`
}

type anthropicMessageDelta struct {
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthropicUsage `json:"usage"`
}

type anthropicError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *AnthropicProvider) decodeStream(ctx context.Context, body io.ReadCloser, out chan<- StreamEvent, m ModelInfo) {
	defer close(out)
	defer body.Close()

	reader := NewSSEReader(body)

	// blockState[idx] tracks what we know about the currently-open
	// content block at that stream index. Anthropic emits events in
	// index order but blocks of different types can interleave, so we
	// key on index rather than assuming "one block at a time".
	type blockState struct {
		kind    string // "text" | "tool_use" | "thinking"
		toolID  string
		toolName string
		jsonBuf  bytes.Buffer // accumulated partial_json for tool_use
	}
	blocks := map[int]*blockState{}

	var cumUsage Usage
	// Track finalized stop reason for the terminal event.
	stopReason := StopEnd

	emit := func(ev StreamEvent) bool {
		ev.At = time.Now()
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for {
		ev, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			emit(StreamEvent{Type: EvError, Err: fmt.Errorf("anthropic stream: %w", err)})
			return
		}
		if ctx.Err() != nil {
			emit(StreamEvent{Type: EvError, Err: ctx.Err(), StopReason: StopAborted})
			return
		}

		// Quick peek at the type discriminator inside the data JSON.
		var hdr anthropicEventHeader
		if err := json.Unmarshal([]byte(ev.Data), &hdr); err != nil {
			// Malformed event — provider-side bug or proxy corruption.
			// Do not abort; just skip this frame.
			continue
		}
		// The `event:` header and the JSON `"type"` should match, but
		// we trust the JSON (the only one Anthropic's docs guarantee).

		switch hdr.Type {
		case "ping":
			emit(StreamEvent{Type: EvPing})

		case "message_start":
			var ms anthropicMessageStart
			if err := json.Unmarshal([]byte(ev.Data), &ms); err == nil {
				cumUsage.InputTokens = ms.Message.Usage.InputTokens
				cumUsage.CacheCreationTokens = ms.Message.Usage.CacheCreationInputTokens
				cumUsage.CacheReadTokens = ms.Message.Usage.CacheReadInputTokens
			}
			emit(StreamEvent{Type: EvMessageStart})

		case "content_block_start":
			var bs anthropicBlockStart
			if err := json.Unmarshal([]byte(ev.Data), &bs); err != nil {
				continue
			}
			st := &blockState{kind: bs.ContentBlock.Type, toolID: bs.ContentBlock.ID, toolName: bs.ContentBlock.Name}
			blocks[bs.Index] = st
			if st.kind == "tool_use" {
				emit(StreamEvent{Type: EvToolUseStart, ToolUseID: st.toolID, ToolName: st.toolName})
			}

		case "content_block_delta":
			var bd anthropicBlockDelta
			if err := json.Unmarshal([]byte(ev.Data), &bd); err != nil {
				continue
			}
			st, ok := blocks[bd.Index]
			if !ok {
				continue
			}
			switch bd.Delta.Type {
			case "text_delta":
				if bd.Delta.Text != "" {
					emit(StreamEvent{Type: EvTextDelta, TextDelta: bd.Delta.Text})
				}
			case "thinking_delta":
				if bd.Delta.Thinking != "" {
					emit(StreamEvent{Type: EvThinkingDelta, TextDelta: bd.Delta.Thinking})
				}
			case "input_json_delta":
				st.jsonBuf.WriteString(bd.Delta.PartialJSON)
				emit(StreamEvent{Type: EvToolUseDelta, ToolUseID: st.toolID, InputDelta: bd.Delta.PartialJSON})
			case "signature_delta":
				// Thinking signature bytes — not propagated (we don't
				// replay thinking blocks in later turns; if we start,
				// this is where we'd capture them).
			}

		case "content_block_stop":
			var bd struct{ Index int `json:"index"` }
			_ = json.Unmarshal([]byte(ev.Data), &bd)
			st, ok := blocks[bd.Index]
			if !ok {
				continue
			}
			if st.kind == "tool_use" {
				// Collapse accumulated JSON to a single RawMessage.
				input := json.RawMessage(st.jsonBuf.Bytes())
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				emit(StreamEvent{Type: EvToolUseEnd, ToolUseID: st.toolID, ToolName: st.toolName, ToolInput: input})
			}
			delete(blocks, bd.Index)

		case "message_delta":
			var md anthropicMessageDelta
			if err := json.Unmarshal([]byte(ev.Data), &md); err == nil {
				cumUsage.OutputTokens = md.Usage.OutputTokens
				// InputTokens in message_delta is sometimes 0; rely on
				// the value captured at message_start.
				if md.Delta.StopReason != "" {
					stopReason = normalizeAnthropicStop(md.Delta.StopReason)
				}
			}

		case "message_stop":
			cumUsage = AttachCost(cumUsage, m)
			emit(StreamEvent{Type: EvMessageStop, StopReason: stopReason, Usage: cumUsage})
			return

		case "error":
			var er anthropicError
			_ = json.Unmarshal([]byte(ev.Data), &er)
			msg := er.Error.Message
			if msg == "" {
				msg = "anthropic: unknown streaming error"
			}
			emit(StreamEvent{Type: EvError, Err: fmt.Errorf("%s: %s", er.Error.Type, msg)})
			return
		}
	}

	// Body ended without a message_stop — treat as clean close.
	cumUsage = AttachCost(cumUsage, m)
	emit(StreamEvent{Type: EvMessageStop, StopReason: stopReason, Usage: cumUsage})
}

// normalizeAnthropicStop maps provider-specific stop reasons to our
// enum. Unknown values collapse to StopEnd rather than leaving a
// surprising zero-value "" on the terminal event.
func normalizeAnthropicStop(s string) StopReason {
	switch s {
	case "end_turn":
		return StopEnd
	case "max_tokens":
		return StopMaxTok
	case "stop_sequence":
		return StopStopSeq
	case "tool_use":
		return StopToolUse
	case "pause_turn":
		return StopEnd // treat pause as end; caller may resume via continue-API
	case "refusal":
		return StopEnd
	}
	return StopEnd
}

// min — Go 1.21+ has builtin min, keep a local alias for readability.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
