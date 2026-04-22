package runner

// Context compaction for long-running sessions.
//
// Borrowed heavily from Claude Code's services/compact/* (Apache 2.0
// equivalents in how these patterns are used across the industry),
// adapted to Go's message shape:
//
//   Tier 1 — microCompact (free, deterministic, no LLM call):
//            walk messages, replace tool_result bodies belonging to
//            earlier "ephemeral" tool calls (read/glob/grep) with a
//            short placeholder. Recent tool results are preserved in
//            case the model still needs them.
//
//   Tier 2 — summarize (paid, thorough, one LLM call):
//            when microCompact hasn't reclaimed enough, ship the
//            whole transcript to the same endpoint with a structured
//            summarization prompt. Replace the live transcript with
//            a single assistant message carrying the summary, plus a
//            marker so a second compaction won't re-compact the same
//            summary.
//
// The Loop invokes maybeCompact() before every LLM call once token
// usage on the previous turn crosses a threshold. Circuit-breaker
// counts consecutive failures — after 3, stop trying (Claude Code
// telemetry: skipping this lost ~250k wasted API calls/day globally).

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/a3c/platform/internal/llm"
)

// CompactionPolicy describes when the loop should try to compact.
// Defaults are derived from Claude Code's constants adapted to
// MiniMax's 200k window.
type CompactionPolicy struct {
	// ContextWindow is the total token budget the provider advertises
	// for this model. 0 disables compaction entirely (caller can opt
	// out when messages are known to be bounded).
	ContextWindow int

	// ReserveForSummary is the room kept for the summarization
	// response itself. 20k matches Claude Code's p99.99.
	ReserveForSummary int

	// AutoCompactBuffer is the safety margin below effective window
	// at which auto-compaction fires. 13k matches Claude Code.
	AutoCompactBuffer int

	// MaxConsecutiveFailures is the circuit breaker threshold. 3
	// matches Claude Code — any more and a broken compact path will
	// loop forever.
	MaxConsecutiveFailures int

	// MicroCompactKeepRecentTurns keeps the last N turns' tool
	// results intact during microcompact, so the model still has its
	// most recent context when summarising. 3 is generous enough to
	// avoid evicting state the model is mid-using.
	MicroCompactKeepRecentTurns int
}

// DefaultCompactionPolicy mirrors Claude Code's autocompact
// defaults. See services/compact/autoCompact.ts for the origin of
// each magic number.
var DefaultCompactionPolicy = CompactionPolicy{
	ContextWindow:               200_000,
	ReserveForSummary:           20_000,
	AutoCompactBuffer:           13_000,
	MaxConsecutiveFailures:      3,
	MicroCompactKeepRecentTurns: 3,
}

// CompactionState is the per-session scratch space the Loop threads
// through. Tracks failure counts for the circuit breaker and the
// "already microcompacted which tool_use_id" set so we don't re-work
// already-stripped entries.
type CompactionState struct {
	consecutiveFailures  int
	microCompactedIDs    map[string]bool
	summarizedUpToTurnIx int
}

func newCompactionState() *CompactionState {
	return &CompactionState{
		microCompactedIDs: map[string]bool{},
	}
}

// ---- public entry point -------------------------------------------------

// maybeCompact decides if the conversation is large enough to shrink
// and, if so, applies tier-1 then tier-2. Returns the (possibly
// rewritten) message slice + an indication of what happened.
//
// Called by the Loop before each LLM request. `lastTurnTokens` is
// the input_tokens reported on the previous turn's usage — used as a
// cheap proxy for "how full is context right now". On the first
// iteration (no prior usage), caller passes the rough estimate.
func maybeCompact(
	ctx context.Context,
	cs *CompactionState,
	pol CompactionPolicy,
	endpointID, model string,
	messages []llm.Message,
	lastTurnTokens int,
) ([]llm.Message, CompactionOutcome) {
	if pol.ContextWindow == 0 {
		return messages, CompactionOutcome{Action: "disabled"}
	}
	if cs.consecutiveFailures >= pol.MaxConsecutiveFailures {
		return messages, CompactionOutcome{Action: "circuit-broken"}
	}
	threshold := autoCompactThreshold(pol)
	// Use the larger of measured-tokens or a conservative estimate;
	// measured is more accurate but unavailable on iter 1.
	est := estimateMessageTokens(messages)
	tokens := est
	if lastTurnTokens > tokens {
		tokens = lastTurnTokens
	}
	if tokens < threshold {
		return messages, CompactionOutcome{Action: "below-threshold", Tokens: tokens}
	}

	// Tier 1: cheap. Strip old tool_results for ephemeral tools.
	micro, stripped := microCompact(cs, messages, pol.MicroCompactKeepRecentTurns)
	if stripped > 0 {
		newEst := estimateMessageTokens(micro)
		if newEst < threshold {
			cs.consecutiveFailures = 0
			return micro, CompactionOutcome{
				Action: "microcompact",
				Tokens: newEst,
				Before: tokens, StrippedResults: stripped,
			}
		}
		// microcompact helped but not enough — proceed to tier 2 on
		// the microcompacted transcript.
		messages = micro
	}

	// Tier 2: spend an LLM call.
	summary, summErr := summarizeConversation(ctx, endpointID, model, messages, pol.ReserveForSummary)
	if summErr != nil {
		cs.consecutiveFailures++
		log.Printf("[Compaction] summarize failed (streak=%d): %v",
			cs.consecutiveFailures, summErr)
		return messages, CompactionOutcome{Action: "summarize-failed", Error: summErr.Error()}
	}
	cs.consecutiveFailures = 0
	replaced := replaceWithSummary(messages, summary)
	return replaced, CompactionOutcome{
		Action:    "summarized",
		Tokens:    estimateMessageTokens(replaced),
		Before:    tokens,
		SummaryUSD: summary.Cost,
	}
}

// CompactionOutcome is what the Loop prints / broadcasts so operators
// can tell WHICH tier triggered and whether it reclaimed enough.
type CompactionOutcome struct {
	Action          string // disabled | below-threshold | microcompact | summarize-failed | summarized | circuit-broken
	Tokens          int    // token count AFTER compaction (estimated)
	Before          int    // token count before compaction
	StrippedResults int    // how many tool_result blocks were blanked by microcompact
	Error           string
	SummaryUSD      float64
}

// ---- thresholds ---------------------------------------------------------

// autoCompactThreshold: the token count at which we start compacting.
// Matches Claude Code's getAutoCompactThreshold:
//   effective_window = context_window − reserve_for_summary
//   threshold        = effective_window − autocompact_buffer
func autoCompactThreshold(pol CompactionPolicy) int {
	effective := pol.ContextWindow - pol.ReserveForSummary
	if effective <= 0 {
		return 0 // misconfigured; disables
	}
	return effective - pol.AutoCompactBuffer
}

// ---- tier 1: microcompact ----------------------------------------------

// ephemeralToolNames are the tools whose tool_result content can be
// stripped after a few turns. These produce bulky outputs (file
// contents, grep hits, dir listings) that the model already used
// once and rarely re-needs. Matches Claude Code's COMPACTABLE_TOOLS.
var ephemeralToolNames = map[string]bool{
	"read": true,
	"glob": true,
	"grep": true,
}

const strippedPlaceholder = "[Old tool result content cleared by compactor]"

// microCompact strips tool_result bodies for ephemeral tools whose
// tool_use is older than keepRecentTurns ago. A "turn" here is one
// assistant message; conversations go user → assistant → user → ...
// so keepRecentTurns=3 means "the last 3 assistant messages' tool
// results are preserved verbatim."
//
// Returns the rewritten messages + count of how many result blocks
// were stripped (for operator telemetry).
func microCompact(cs *CompactionState, messages []llm.Message, keepRecentTurns int) ([]llm.Message, int) {
	// Identify the cutoff: find the index of the Nth-most-recent
	// assistant message and preserve everything from there onward.
	cutoff := findKeepCutoff(messages, keepRecentTurns)

	// Build tool_use_id → tool_name for messages up to cutoff so we
	// know which tool_result blocks are ephemeral.
	ephemeralIDs := map[string]bool{}
	for i := 0; i < cutoff && i < len(messages); i++ {
		for _, blk := range messages[i].Content {
			if blk.Type == llm.BlockToolUse && ephemeralToolNames[blk.ToolName] {
				ephemeralIDs[blk.ToolUseID] = true
			}
		}
	}

	stripped := 0
	out := make([]llm.Message, len(messages))
	for i := range messages {
		if i >= cutoff {
			out[i] = messages[i]
			continue
		}
		newBlocks := make([]llm.ContentBlock, len(messages[i].Content))
		for j, blk := range messages[i].Content {
			if blk.Type == llm.BlockToolResult && ephemeralIDs[blk.ToolUseID] && !cs.microCompactedIDs[blk.ToolUseID] {
				// Strip body; mark so we don't re-strip on next pass
				// (a no-op but keeps stats accurate).
				cs.microCompactedIDs[blk.ToolUseID] = true
				newBlocks[j] = llm.NewToolResultBlock(blk.ToolUseID, strippedPlaceholder, false)
				stripped++
			} else {
				newBlocks[j] = blk
			}
		}
		out[i] = llm.Message{Role: messages[i].Role, Content: newBlocks}
	}
	return out, stripped
}

// findKeepCutoff returns the message index such that from this index
// onward there are `keep` assistant messages (inclusive). Earlier
// messages may have their tool_results stripped.
func findKeepCutoff(messages []llm.Message, keep int) int {
	if keep <= 0 {
		return len(messages) // strip nothing
	}
	assistantCount := 0
	// Walk backwards; the first position where we've accumulated
	// `keep` assistant messages is the cutoff.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleAssistant {
			assistantCount++
			if assistantCount >= keep {
				return i
			}
		}
	}
	return 0 // fewer than `keep` assistant messages — don't strip
}

// ---- tier 2: LLM summarization -----------------------------------------

// summaryResult carries the generated summary text + usage stats for
// telemetry. Intentionally narrow — the Loop doesn't need the raw
// StreamEvent stream.
type summaryResult struct {
	Text string
	Cost float64
}

// summarizeConversation dispatches one LLM call on the given
// endpoint/model to compress the transcript. The prompt mirrors
// Claude Code's BASE_COMPACT_PROMPT structure: 9 mandatory sections
// and an explicit "NO TOOL CALLS" preamble.
func summarizeConversation(
	ctx context.Context,
	endpointID, model string,
	messages []llm.Message,
	reserveOutput int,
) (*summaryResult, error) {
	// Transcript is rendered as one giant user message. Can't just
	// forward the messages/tools array — we DON'T want the compactor
	// to think it can call the tools from the transcript.
	transcript := renderTranscriptAsText(messages)
	req := llm.ChatRequest{
		Model:     model,
		System:    summarizeSystemPrompt,
		Messages:  []llm.Message{llm.NewUserText(transcript)},
		MaxTokens: reserveOutput,
		// No tools. Explicit.
	}
	stream, err := llm.DefaultRegistry.ChatStream(ctx, endpointID, req)
	if err != nil {
		return nil, fmt.Errorf("dispatch summarizer: %w", err)
	}
	var sb strings.Builder
	var usage llm.Usage
	var lastErr error
	for ev := range stream {
		switch ev.Type {
		case llm.EvTextDelta:
			sb.WriteString(ev.TextDelta)
		case llm.EvMessageStop:
			usage = ev.Usage
		case llm.EvError:
			lastErr = ev.Err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return nil, fmt.Errorf("summarizer returned empty text")
	}
	// Strip the <analysis> scratchpad — it's for the model's own
	// working, not the next-turn context.
	if start := strings.Index(text, "<summary>"); start >= 0 {
		if end := strings.Index(text, "</summary>"); end > start {
			text = strings.TrimSpace(text[start+len("<summary>") : end])
		}
	}
	return &summaryResult{Text: text, Cost: usage.USD}, nil
}

// replaceWithSummary swaps the old transcript for a single assistant
// message carrying the summary, prefixed by a marker so subsequent
// microCompact passes know there's nothing below this line to strip.
func replaceWithSummary(messages []llm.Message, summary *summaryResult) []llm.Message {
	marker := "[Compaction boundary — everything before this point has been summarised below.]\n\n"
	// Preserve the most recent user turn so the model sees the
	// immediate task it was just asked about. Claude Code keeps
	// "post-compact messages"; we do the narrow version: just the
	// latest user message if any.
	var tail []llm.Message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleUser {
			tail = append([]llm.Message{messages[i]}, tail...)
			break
		}
	}
	summaryMsg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			llm.NewTextBlock(marker + summary.Text),
		},
	}
	return append([]llm.Message{summaryMsg}, tail...)
}

// ---- token estimation --------------------------------------------------

// estimateMessageTokens is a cheap approximation used to decide
// whether to trigger compaction. Mirrors Claude Code's
// estimateMessageTokens: roughly 1 token per ~4 chars for English +
// a 4/3 safety pad at the end.
//
// This isn't a real tokenizer (we'd need model-specific BPE) but
// consistently over-estimates, which is the safe direction — better
// to compact a bit early than to discover the limit was already
// exceeded.
func estimateMessageTokens(messages []llm.Message) int {
	chars := 0
	for _, m := range messages {
		for _, blk := range m.Content {
			switch blk.Type {
			case llm.BlockText:
				chars += len(blk.Text)
			case llm.BlockToolUse:
				chars += len(blk.ToolName)
				chars += len(blk.ToolInput)
			case llm.BlockToolResult:
				chars += len(blk.ToolResult)
			case llm.BlockImage:
				chars += 2000 * 4 // matches Claude Code's 2000-token estimate
			case llm.BlockThinking:
				chars += len(blk.Text)
			}
		}
	}
	// Roughly 4 chars per token; pad by 4/3.
	raw := chars / 4
	return raw * 4 / 3
}

// renderTranscriptAsText turns the message array into a plain-text
// transcript the summarizer can process. Tool calls and results are
// rendered as labelled blocks so the model sees the structure.
func renderTranscriptAsText(messages []llm.Message) string {
	var sb strings.Builder
	sb.WriteString("Below is the conversation to summarise.\n\n")
	for _, m := range messages {
		switch m.Role {
		case llm.RoleUser:
			sb.WriteString("## USER\n")
		case llm.RoleAssistant:
			sb.WriteString("## ASSISTANT\n")
		case llm.RoleSystem:
			sb.WriteString("## SYSTEM\n")
		}
		for _, blk := range m.Content {
			switch blk.Type {
			case llm.BlockText:
				sb.WriteString(blk.Text)
				sb.WriteString("\n")
			case llm.BlockToolUse:
				sb.WriteString("[tool_use: ")
				sb.WriteString(blk.ToolName)
				sb.WriteString("]\n")
				sb.WriteString(string(blk.ToolInput))
				sb.WriteString("\n")
			case llm.BlockToolResult:
				sb.WriteString("[tool_result")
				if blk.IsError {
					sb.WriteString(" ERROR")
				}
				sb.WriteString("]\n")
				sb.WriteString(blk.ToolResult)
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// summarizeSystemPrompt follows Claude Code's BASE_COMPACT_PROMPT
// structure closely — the 9 mandatory sections below are
// battle-tested and cover the minimum the next assistant needs to
// pick up work without drift. The NO-TOOLS preamble matters because
// even with `Tools: nil` some models will try to call tools they
// remember from the transcript.
const summarizeSystemPrompt = `You are summarising a conversation so it fits in a smaller context. This is a bookkeeping task — you MUST respond with TEXT ONLY, wrapped in <analysis> and <summary> tags.

CRITICAL: DO NOT call any tools. The caller has disabled tool execution; attempting to call a tool will result in the summary being discarded and the call failing. Your entire output must be plain text.

Produce two parts:

1. <analysis>…</analysis>
   Your chronological working notes — for each section of the conversation identify:
     - The user's explicit requests and intents (direct quotes where possible).
     - What the assistant did in response.
     - Key technical concepts, patterns, file names, function signatures.
     - Errors encountered and how they were resolved.
     - User feedback, especially corrections or changed intent.

2. <summary>…</summary>
   The structured summary that will REPLACE the transcript. Must contain these nine sections in order:
     1. Primary Request and Intent: the user's overall goal + current focus.
     2. Key Technical Concepts: list of technologies, libraries, patterns discussed.
     3. Files and Code Sections: files read / modified, with short note on WHY each mattered. Include critical code snippets verbatim.
     4. Errors and fixes: each error + how it was resolved + relevant user feedback.
     5. Problem Solving: problems solved; ongoing troubleshooting.
     6. All user messages: every non-tool-result user message, in order. Preserve exact wording for the most recent.
     7. Pending Tasks: what the user explicitly asked for that is not yet done.
     8. Current Work: precisely what was being worked on immediately before this summary request, with file names and current state.
     9. Optional Next Step: the next specific action, aligned with the user's latest request. Include a verbatim quote from the most recent conversation so there is no drift in task interpretation.

Only the <summary> content will be fed back to the next assistant. Keep it thorough; err on the side of including technical detail.`
