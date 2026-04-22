package llm

// Unified message / content / streaming types shared by every Provider.
//
// Design choice: we model content as Anthropic-style typed blocks
// (text / tool_use / tool_result / thinking) rather than OpenAI's flat
// "message.content + message.tool_calls" split. Anthropic's shape is a
// strict superset that round-trips cleanly through OpenAI's API; the
// opposite direction loses information (OpenAI can't express
// interleaved text + tool_use + text in one assistant turn). Keeping
// the richer shape here means providers translate *down* on egress,
// never *up* on ingress — fewer lossy conversions.

import (
	"context"
	"encoding/json"
	"time"
)

// Role is the speaker of a message. "tool" is a virtual role: tool
// results are sent back inside a user-turn ContentBlockToolResult
// block, but callers conceptually think of them as a separate role;
// providers fold them into user messages at serialization time.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ContentBlockType tags a ContentBlock. Using a string discriminator
// (not a sealed-interface pattern) because we have to JSON-round-trip
// these into provider payloads and Go JSON unmarshaling is easier on
// discriminator strings than on interface hierarchies.
type ContentBlockType string

const (
	BlockText       ContentBlockType = "text"
	BlockToolUse    ContentBlockType = "tool_use"
	BlockToolResult ContentBlockType = "tool_result"
	BlockThinking   ContentBlockType = "thinking"
	BlockImage      ContentBlockType = "image"
)

// ContentBlock is one typed chunk inside a message. Exactly one of the
// fields is set per Type; the rest are zero. Callers should use the
// constructor helpers (NewTextBlock, NewToolUseBlock, ...) rather than
// assembling these by hand.
type ContentBlock struct {
	Type ContentBlockType `json:"type"`

	// Text: populated for Type=text or Type=thinking.
	Text string `json:"text,omitempty"`

	// ToolUseID is the unique id the assistant picked when emitting a
	// tool_use block; the matching tool_result block carries the same
	// id so the API can pair them.
	ToolUseID string `json:"tool_use_id,omitempty"`

	// ToolName is the name of the tool being invoked (tool_use blocks).
	ToolName string `json:"tool_name,omitempty"`

	// ToolInput is the raw JSON args the assistant generated. Kept as
	// json.RawMessage so we pass it to our tool executor verbatim —
	// parsing here would force us to re-encode for logging/tracing.
	ToolInput json.RawMessage `json:"tool_input,omitempty"`

	// ToolResult payload. A string for plain text, or serialized JSON
	// for structured tool outputs. The IsError flag lets the LLM
	// differentiate "tool ran fine" from "tool returned a reported
	// failure" without parsing semantics itself.
	ToolResult string `json:"tool_result,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`

	// ImageSource is a base64-encoded image (media_type + data) for
	// multimodal models. Only used when the selected model advertises
	// image input support.
	ImageMediaType string `json:"image_media_type,omitempty"`
	ImageData      string `json:"image_data,omitempty"`
}

// Constructor helpers — cheaper to read than composite literals, and
// guarantee the discriminator matches the populated fields.

func NewTextBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockText, Text: text}
}

func NewThinkingBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockThinking, Text: text}
}

func NewToolUseBlock(id, name string, input json.RawMessage) ContentBlock {
	return ContentBlock{Type: BlockToolUse, ToolUseID: id, ToolName: name, ToolInput: input}
}

func NewToolResultBlock(id, content string, isError bool) ContentBlock {
	return ContentBlock{Type: BlockToolResult, ToolUseID: id, ToolResult: content, IsError: isError}
}

func NewImageBlock(mediaType, base64Data string) ContentBlock {
	return ContentBlock{Type: BlockImage, ImageMediaType: mediaType, ImageData: base64Data}
}

// Message is a single turn in a conversation. System messages carry
// their content in Content[0].Text by convention (providers that don't
// accept system messages in the `messages` array — e.g. Anthropic —
// hoist it into the top-level `system` field).
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// NewUserText is the most common message constructor.
func NewUserText(text string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{NewTextBlock(text)}}
}

func NewAssistantText(text string) Message {
	return Message{Role: RoleAssistant, Content: []ContentBlock{NewTextBlock(text)}}
}

// ToolDef describes a tool to the model. Schema is a JSON Schema object
// (opaque to the LLM layer — provider adapters pass it through). Using
// map[string]any rather than a typed Schema struct because JSON Schema
// has too many shapes to usefully model in Go, and every provider's
// tool-calling API takes an opaque JSON Schema anyway.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
}

// ReasoningEffort controls how much "thinking" the model does before
// answering. Supported by reasoning-class models (Claude Opus 4.5,
// MiniMax-M2.7 via `reasoning_effort`, OpenAI o-series).
type ReasoningEffort string

const (
	ReasoningOff    ReasoningEffort = ""
	ReasoningLow    ReasoningEffort = "low"
	ReasoningMedium ReasoningEffort = "medium"
	ReasoningHigh   ReasoningEffort = "high"
)

// ChatRequest is the provider-agnostic request shape. Model is a
// provider-local identifier (e.g. "claude-sonnet-4-5-20250929",
// "MiniMax-M2.7") — the Registry uses ProviderID to pick the adapter.
//
// MaxTokens defaults to 4096 when zero; providers override to their
// own defaults only when strictly necessary.
type ChatRequest struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []ToolDef
	MaxTokens   int
	Temperature float64
	TopP        float64
	StopSeqs    []string
	Reasoning   ReasoningEffort

	// ToolChoice: "" (auto), "any" (must call some tool), "none" (no
	// tools), or a specific tool name to force. Maps to each provider's
	// native field.
	ToolChoice string

	// Metadata flows through to providers that support user/session
	// tagging (Anthropic's metadata.user_id, OpenAI's user field).
	UserID string
}

// StopReason is normalized across providers. The long tail of native
// stop reasons folds into one of these five categories.
type StopReason string

const (
	StopEnd      StopReason = "end_turn"   // natural end of assistant turn
	StopMaxTok   StopReason = "max_tokens" // hit max_tokens ceiling
	StopToolUse  StopReason = "tool_use"   // assistant emitted tool_use — loop caller must dispatch tools
	StopStopSeq  StopReason = "stop_sequence"
	StopErrored  StopReason = "error"
	StopAborted  StopReason = "aborted" // client context cancelled mid-stream
)

// Usage tracks token accounting + computed cost. Providers that don't
// expose cache_read / cache_create (OpenAI today) leave those zero.
type Usage struct {
	InputTokens          int     `json:"input_tokens"`
	OutputTokens         int     `json:"output_tokens"`
	CacheCreationTokens  int     `json:"cache_creation_input_tokens,omitempty"`
	CacheReadTokens      int     `json:"cache_read_input_tokens,omitempty"`
	ReasoningTokens      int     `json:"reasoning_tokens,omitempty"` // for thinking models
	USD                  float64 `json:"usd"`
}

// EventType discriminates StreamEvent. Using sentinel strings rather
// than a sum type so the channel can pass the single Event struct by
// value — cheaper than boxing interfaces on every delta.
type EventType string

const (
	EvMessageStart EventType = "message_start"
	EvTextDelta    EventType = "text_delta"
	EvThinkingDelta EventType = "thinking_delta"
	EvToolUseStart EventType = "tool_use_start"
	EvToolUseDelta EventType = "tool_use_delta" // partial JSON input
	EvToolUseEnd   EventType = "tool_use_end"
	EvMessageStop  EventType = "message_stop"
	EvError        EventType = "error"
	EvPing         EventType = "ping" // keepalive — consumers usually ignore
)

// StreamEvent is one chunk from a ChatStream channel. Fields are
// populated sparsely per Type; consumers switch on Type first.
type StreamEvent struct {
	Type EventType
	At   time.Time

	// Text deltas: set for EvTextDelta and EvThinkingDelta.
	TextDelta string

	// Tool-use assembly fields.
	ToolUseID   string          // set on EvToolUseStart/Delta/End
	ToolName    string          // set on EvToolUseStart
	ToolInput   json.RawMessage // set on EvToolUseEnd (complete input)
	InputDelta  string          // set on EvToolUseDelta (partial JSON)

	// Terminal fields — set on EvMessageStop.
	StopReason StopReason
	Usage      Usage

	// EvError carries the wrapped error; consumers should treat this
	// as the stream's terminal event (no further events follow).
	Err error
}

// ProviderID names the wire format an adapter speaks. Only two shapes
// exist because every real-world endpoint follows one of them: native
// Anthropic Messages API, or OpenAI's /chat/completions schema (which
// MiniMax / DeepSeek / Moonshot / Groq / xAI / OpenRouter / Together /
// Fireworks / Ollama / Gemini's OpenAI-compat endpoint all implement).
//
// An individual deployment ("endpoint") is identified by a DB-assigned
// endpoint ID, not by ProviderID — the Registry keys by endpoint ID so
// users can register multiple endpoints of the same format (e.g. a
// MiniMax prod key + a MiniMax dev key).
type ProviderID string

const (
	ProviderAnthropic ProviderID = "anthropic"
	ProviderOpenAI    ProviderID = "openai"
)

// ModelInfo describes one model exposed by a provider. Used by the
// dashboard's model picker and by cost calc.
type ModelInfo struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	ContextWindow   int     `json:"context_window"`
	MaxOutputTokens int     `json:"max_output_tokens"`
	SupportsTools   bool    `json:"supports_tools"`
	SupportsVision  bool    `json:"supports_vision"`
	SupportsReasoning bool  `json:"supports_reasoning"`
	InputPricePerMTok   float64 `json:"input_price_per_mtok,omitempty"`
	OutputPricePerMTok  float64 `json:"output_price_per_mtok,omitempty"`
	CacheReadPricePerMTok   float64 `json:"cache_read_price_per_mtok,omitempty"`
	CacheCreatePricePerMTok float64 `json:"cache_create_price_per_mtok,omitempty"`
}

// Ensure StreamEvent is non-nil on return channels when context
// cancels: providers emit EvError or EvMessageStop with StopAborted
// rather than just closing the channel, so consumers can always tell
// "stream finished cleanly" from "stream was killed".
var _ context.Context // keep the import in case a future type wants it
