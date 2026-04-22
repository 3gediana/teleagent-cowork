package llm

// OpenAI-compatible chat/completions adapter.
//
// Covers the whole "Bearer-auth + /v1/chat/completions + SSE" family:
// MiniMax (our current default), OpenAI proper, DeepSeek, Moonshot,
// Groq, xAI, OpenRouter, Together, Fireworks, local Ollama, etc.
//
// Differences from Anthropic worth surfacing:
//   - Tool-call spec: flat tool_calls[] on message delta, not typed
//     content blocks. We reassemble into our unified ContentBlock
//     shape so the rest of the platform can't tell providers apart.
//   - Stop reason vocabulary differs (`stop` / `length` / `tool_calls`).
//   - Usage is opt-in via stream_options.include_usage=true; without
//     that flag, streaming responses omit token counts entirely.
//   - Prompt caching is emergent: DeepSeek + Moonshot report
//     prompt_cache_hit_tokens in usage; others don't. We capture when
//     present, ignore when absent.

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

type OpenAIProvider struct {
	cfg    ProviderConfig
	models []ModelInfo
	apiURL string
}

// NewOpenAIProvider builds an adapter for any endpoint speaking OpenAI's
// /chat/completions schema. When cfg.BaseURL is empty, falls back to
// OpenAI's hosted URL; otherwise uses whatever /v1 root the caller
// supplies (MiniMax → https://api.minimaxi.com/v1 , DeepSeek, Moonshot,
// Groq, xAI, OpenRouter, Together, Ollama, Gemini's /openai shim, ...).
// The server-side of the request is identical across all of them — only
// base URL + api key + model IDs differ, which is exactly what a
// runtime LLMEndpoint row captures.
func NewOpenAIProvider(cfg ProviderConfig) *OpenAIProvider {
	apiURL := cfg.BaseURL
	if apiURL == "" {
		apiURL = "https://api.openai.com/v1/chat/completions"
	} else if !strings.HasSuffix(apiURL, "/chat/completions") {
		apiURL = strings.TrimRight(apiURL, "/") + "/chat/completions"
	}
	models := make([]ModelInfo, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		models = append(models, MergePricing(m))
	}
	return &OpenAIProvider{cfg: cfg, models: models, apiURL: apiURL}
}

func (p *OpenAIProvider) ID() ProviderID { return ProviderOpenAI }
func (p *OpenAIProvider) Name() string {
	return firstNonEmpty(p.cfg.Name, "OpenAI-compatible")
}
func (p *OpenAIProvider) Models() []ModelInfo {
	out := make([]ModelInfo, len(p.models))
	copy(out, p.models)
	return out
}

func (p *OpenAIProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	reqBody, err := p.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
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
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		// Some OpenAI-compat providers require an org header; plumb via
		// Extra for deployments that need it.
		if org := p.cfg.Extra["organization"]; org != "" {
			httpReq.Header.Set("OpenAI-Organization", org)
		}
		return sharedHTTPClient.Do(httpReq)
	})
	if err != nil {
		return nil, fmt.Errorf("openai: POST: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		defer httpResp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024))
		return nil, fmt.Errorf("openai: status %d: %s", httpResp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 64)
	go p.decodeStream(ctx, httpResp.Body, ch, p.modelInfoFor(req.Model))
	return ch, nil
}

func (p *OpenAIProvider) buildRequestBody(req ChatRequest) ([]byte, error) {
	if req.Model == "" {
		return nil, errors.New("openai: ChatRequest.Model is required")
	}
	body := map[string]any{
		"model":  req.Model,
		"stream": true,
		// Ask providers to include usage in the final chunk. Graceful
		// on servers that ignore unknown fields (all of them do).
		"stream_options": map[string]any{"include_usage": true},
	}
	if req.MaxTokens > 0 {
		// OpenAI-spec: max_tokens (legacy) vs max_completion_tokens
		// (o-series). Sending both is harmless; the server uses the
		// one it understands.
		body["max_tokens"] = req.MaxTokens
		body["max_completion_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if req.TopP > 0 {
		body["top_p"] = req.TopP
	}
	if len(req.StopSeqs) > 0 {
		body["stop"] = req.StopSeqs
	}
	if req.UserID != "" {
		body["user"] = req.UserID
	}
	if req.Reasoning != ReasoningOff {
		// OpenAI o-series + MiniMax-M2.7 accept reasoning_effort.
		// Other compat servers silently ignore.
		body["reasoning_effort"] = string(req.Reasoning)
	}

	msgs, err := p.encodeMessages(req.System, req.Messages)
	if err != nil {
		return nil, err
	}
	body["messages"] = msgs

	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Schema,
				},
			})
		}
		body["tools"] = tools
		switch req.ToolChoice {
		case "", "auto":
			body["tool_choice"] = "auto"
		case "any":
			body["tool_choice"] = "required"
		case "none":
			body["tool_choice"] = "none"
		default:
			body["tool_choice"] = map[string]any{
				"type":     "function",
				"function": map[string]string{"name": req.ToolChoice},
			}
		}
	}

	return json.Marshal(body)
}

// encodeMessages flattens our unified content into OpenAI's shape.
// OpenAI requires:
//   - system message as its own entry with role=system
//   - tool results as role=tool messages (NOT nested inside user)
//   - tool calls as an assistant message with a tool_calls array
// Multi-part assistant content (text + tool_use interleaved) is mapped
// to separate flat fields: content string + tool_calls array.
func (p *OpenAIProvider) encodeMessages(system string, in []Message) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(in)+1)
	if strings.TrimSpace(system) != "" {
		out = append(out, map[string]any{"role": "system", "content": system})
	}
	for _, m := range in {
		if m.Role == RoleSystem {
			continue // already emitted
		}
		switch m.Role {
		case RoleAssistant:
			// Collect text and tool_use blocks.
			var textParts []string
			var toolCalls []map[string]any
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					textParts = append(textParts, b.Text)
				case BlockThinking:
					// OpenAI doesn't round-trip thinking; drop. Some
					// providers error if you include unknown block
					// types in messages[].
				case BlockToolUse:
					args := string(b.ToolInput)
					if args == "" {
						args = "{}"
					}
					toolCalls = append(toolCalls, map[string]any{
						"id":   b.ToolUseID,
						"type": "function",
						"function": map[string]any{
							"name":      b.ToolName,
							"arguments": args,
						},
					})
				}
			}
			entry := map[string]any{
				"role":    "assistant",
				"content": strings.Join(textParts, ""),
			}
			if len(toolCalls) > 0 {
				entry["tool_calls"] = toolCalls
			}
			out = append(out, entry)

		case RoleUser, RoleTool:
			// Tool-result blocks need their own role=tool entries.
			// Text/image blocks stay in one user entry.
			var userParts []map[string]any
			for _, b := range m.Content {
				switch b.Type {
				case BlockToolResult:
					// Flush any pending user parts first to maintain
					// message order (tool result must follow assistant
					// tool_calls and precede the next user turn).
					if len(userParts) > 0 {
						out = append(out, map[string]any{"role": "user", "content": userParts})
						userParts = nil
					}
					tm := map[string]any{
						"role":         "tool",
						"tool_call_id": b.ToolUseID,
						"content":      b.ToolResult,
					}
					out = append(out, tm)
				case BlockText:
					userParts = append(userParts, map[string]any{"type": "text", "text": b.Text})
				case BlockImage:
					userParts = append(userParts, map[string]any{
						"type": "image_url",
						"image_url": map[string]string{
							"url": fmt.Sprintf("data:%s;base64,%s", b.ImageMediaType, b.ImageData),
						},
					})
				}
			}
			if len(userParts) > 0 {
				// Collapse single text parts to the flat string form
				// for compatibility with servers that require it
				// (older DeepSeek endpoints). Multi-part content is
				// sent as array form.
				if len(userParts) == 1 && userParts[0]["type"] == "text" {
					out = append(out, map[string]any{"role": "user", "content": userParts[0]["text"]})
				} else {
					out = append(out, map[string]any{"role": "user", "content": userParts})
				}
			}
		}
	}
	return out, nil
}

func (p *OpenAIProvider) modelInfoFor(id string) ModelInfo {
	for _, m := range p.models {
		if m.ID == id {
			return m
		}
	}
	return MergePricing(ModelInfo{ID: id})
}

// --- Stream decoding -----------------------------------------------------

// openAIChunk is the envelope every streaming data: line carries.
// Finish lines use `"finish_reason": "..."` on one of the choices.
type openAIChunk struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
}

type openAIChoice struct {
	Index        int          `json:"index"`
	Delta        openAIDelta  `json:"delta"`
	FinishReason *string      `json:"finish_reason"`
}

type openAIDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string OR array OR null
	ToolCalls []openAIToolCallDelta `json:"tool_calls,omitempty"`
	// Reasoning/thinking deltas are non-standard but common:
	//   OpenAI o-series: `reasoning` field
	//   DeepSeek R1: `reasoning_content` field
	//   MiniMax: `reasoning_content` field
	Reasoning         string `json:"reasoning,omitempty"`
	ReasoningContent  string `json:"reasoning_content,omitempty"`
}

type openAIToolCallDelta struct {
	Index    int     `json:"index"`
	ID       string  `json:"id,omitempty"`
	Type     string  `json:"type,omitempty"`
	Function openAIFuncDelta `json:"function"`
}

type openAIFuncDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// Some providers (DeepSeek/Moonshot) surface cache stats here.
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens,omitempty"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (p *OpenAIProvider) decodeStream(ctx context.Context, body io.ReadCloser, out chan<- StreamEvent, m ModelInfo) {
	defer close(out)
	defer body.Close()
	reader := NewSSEReader(body)

	type toolAccum struct {
		id        string
		name      string
		args      bytes.Buffer
		announced bool // EvToolUseStart emitted?
	}
	// Tool calls streaming uses per-index accumulators — OpenAI can
	// emit multiple tool calls at different indices concurrently.
	tools := map[int]*toolAccum{}

	emitted := false
	var cumUsage Usage
	stopReason := StopEnd

	emit := func(ev StreamEvent) bool {
		ev.At = time.Now()
		emitted = true
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	messageStartSent := false
	flushToolEnd := func(idx int, t *toolAccum) {
		if !t.announced {
			// Empty tool call (no fragments) — synthesize start so
			// downstream gets a paired start/end even for zero-arg
			// tools (common with OpenAI when arguments={}).
			emit(StreamEvent{Type: EvToolUseStart, ToolUseID: t.id, ToolName: t.name})
			t.announced = true
		}
		input := json.RawMessage(t.args.Bytes())
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		emit(StreamEvent{Type: EvToolUseEnd, ToolUseID: t.id, ToolName: t.name, ToolInput: input})
	}

	for {
		ev, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			emit(StreamEvent{Type: EvError, Err: fmt.Errorf("openai stream: %w", err)})
			return
		}
		if ctx.Err() != nil {
			emit(StreamEvent{Type: EvError, Err: ctx.Err(), StopReason: StopAborted})
			return
		}

		data := strings.TrimSpace(ev.Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Corrupt/partial line — some proxies fragment badly. Skip.
			continue
		}

		if !messageStartSent {
			emit(StreamEvent{Type: EvMessageStart})
			messageStartSent = true
		}

		// Usage sometimes arrives in a chunk with empty choices (final
		// event). Capture whenever present.
		if chunk.Usage != nil {
			cumUsage.InputTokens = chunk.Usage.PromptTokens
			cumUsage.OutputTokens = chunk.Usage.CompletionTokens
			cumUsage.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
			cumUsage.CacheReadTokens = chunk.Usage.PromptCacheHitTokens
		}

		for _, ch := range chunk.Choices {
			// Reasoning/thinking text deltas.
			if ch.Delta.Reasoning != "" {
				emit(StreamEvent{Type: EvThinkingDelta, TextDelta: ch.Delta.Reasoning})
			}
			if ch.Delta.ReasoningContent != "" {
				emit(StreamEvent{Type: EvThinkingDelta, TextDelta: ch.Delta.ReasoningContent})
			}
			// Regular content delta — may be string or array of parts.
			if txt := extractOpenAIContent(ch.Delta.Content); txt != "" {
				emit(StreamEvent{Type: EvTextDelta, TextDelta: txt})
			}

			// Tool call deltas. Each delta can carry id, name, and
			// arguments fragment(s), possibly spread over many events.
			for _, tc := range ch.Delta.ToolCalls {
				t, ok := tools[tc.Index]
				if !ok {
					t = &toolAccum{}
					tools[tc.Index] = t
				}
				if tc.ID != "" {
					t.id = tc.ID
				}
				if tc.Function.Name != "" {
					t.name = tc.Function.Name
				}
				// Announce start as soon as we know both id AND name.
				// Some providers stream them one event apart.
				if !t.announced && t.id != "" && t.name != "" {
					emit(StreamEvent{Type: EvToolUseStart, ToolUseID: t.id, ToolName: t.name})
					t.announced = true
				}
				if tc.Function.Arguments != "" {
					t.args.WriteString(tc.Function.Arguments)
					if t.announced {
						emit(StreamEvent{Type: EvToolUseDelta, ToolUseID: t.id, InputDelta: tc.Function.Arguments})
					}
				}
			}

			if ch.FinishReason != nil {
				// Flush any open tool calls before signalling stop.
				for idx, t := range tools {
					flushToolEnd(idx, t)
				}
				tools = map[int]*toolAccum{}
				stopReason = normalizeOpenAIStop(*ch.FinishReason)
			}
		}
	}

	// Terminal event. Even when emitted==false (empty stream) we still
	// emit message_stop so consumers' range loops terminate cleanly.
	_ = emitted
	// Defensive: some servers close the stream without a finish_reason.
	// Flush any tool accumulators that never got a finish line.
	for idx, t := range tools {
		flushToolEnd(idx, t)
	}
	cumUsage = AttachCost(cumUsage, m)
	emit(StreamEvent{Type: EvMessageStop, StopReason: stopReason, Usage: cumUsage})
}

// extractOpenAIContent handles the three shapes the OpenAI API returns
// for `delta.content`: null, a JSON string, or an array of parts. We
// concatenate all text parts; non-text parts (rare in assistant
// deltas) are skipped.
func extractOpenAIContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// String form (fast path, 99% of calls).
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return ""
	}
	if raw[0] == '[' {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &parts); err != nil {
			return ""
		}
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func normalizeOpenAIStop(s string) StopReason {
	switch s {
	case "stop":
		return StopEnd
	case "length":
		return StopMaxTok
	case "tool_calls", "function_call":
		return StopToolUse
	case "content_filter":
		return StopEnd
	}
	return StopEnd
}
