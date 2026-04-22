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
	"time"

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

	// lastTerminalToolTurnIx: index of the assistant turn that emitted
	// a terminal output tool (audit_output, chief_output, ...). Set
	// when the loop detects such an emission; used by the clear
	// policy to decide if the *work unit* is complete and the
	// transcript can be reset when the next user turn arrives.
	lastTerminalToolTurnIx int

	// lastUserTurnAt: monotonic clock at which the last user-role
	// message was appended. Powers the idle-clear heuristic. Zero
	// value means "never observed a user turn in this session".
	lastUserTurnAt time.Time

	// clearsFired: how many hard clears have occurred in this
	// session. Operator telemetry + guard against pathological
	// clear-every-turn loops.
	clearsFired int
}

func newCompactionState() *CompactionState {
	return &CompactionState{
		microCompactedIDs:      map[string]bool{},
		lastTerminalToolTurnIx: -1,
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
	Action          string // disabled | below-threshold | microcompact | summarize-failed | summarized | circuit-broken | cleared
	Tokens          int    // token count AFTER compaction (estimated)
	Before          int    // token count before compaction
	StrippedResults int    // how many tool_result blocks were blanked by microcompact
	Error           string
	SummaryUSD      float64
	// ClearReason populated when Action == "cleared". One of
	// "terminal-output", "topic-shift", "idle-gap", "explicit".
	ClearReason string
}

// ---- tier 0: hard boundary clear --------------------------------------
//
// Summary/microcompact trim the transcript; clear REPLACES it with a
// fresh one. Use for cases where the model genuinely doesn't need the
// prior transcript — Claude Code's compaction was designed for the
// "accumulating long task" case, but a lot of multi-agent platform
// sessions are actually "one-off, unrelated task" sessions where a
// summary is dead weight. Detecting those and clearing entirely is
// cheaper than summarising.
//
// Three signals trigger a clear; any one is enough:
//
// 1. **Terminal output emitted**. Every role has an output tool that
//    represents "I am done with this work unit" (chief_output,
//    audit_output, analyze_output, ...). After such a call, if the
//    loop happens to continue (e.g. model calls the tool then keeps
//    yapping), we can safely reset — the committed result is on disk.
//    Also applies across user turns: if an earlier turn produced a
//    terminal output and a fresh user turn arrives, the prior
//    transcript is stale history, not context.
//
// 2. **Topic shift**. A new user message whose content has low
//    lexical overlap (via cheap token-set Jaccard — no embeddings
//    needed) with the recent transcript. Threshold is deliberately
//    conservative so we don't clear mid-task.
//
// 3. **Idle gap**. User came back after IdleClearAfter elapsed. Even
//    if the topic is the same, the operator has likely re-gathered
//    context mentally; what was useful to the model 45 minutes ago
//    is mostly dead weight.

// ClearPolicy configures when `maybeClear` fires. Zero-value means
// "never clear" — clearing is opt-in so existing sessions that don't
// pass a policy stay on the old behaviour.
type ClearPolicy struct {
	// TerminalToolNames list tools whose emission marks work-unit
	// completion. After emitting one, the next user turn in the
	// same session triggers a clear. Empty disables this signal.
	TerminalToolNames []string

	// IdleClearAfter: clear when the gap between consecutive user
	// turns exceeds this duration. 0 disables.
	IdleClearAfter time.Duration

	// TopicShiftMinTurns: minimum total user turns before the topic-
	// shift detector is even allowed to fire. Stops it from clearing
	// a fresh conversation on the second user turn just because the
	// first user turn was short. Default 3.
	TopicShiftMinTurns int

	// TopicShiftJaccardMax: Jaccard similarity below which the new
	// user turn is considered "a different topic". 0.10 is empirically
	// the sweet spot — below ~0.10 is clearly different subjects.
	// 0 disables the detector.
	TopicShiftJaccardMax float64

	// MinMessagesBeforeClear: don't bother clearing if the transcript
	// has fewer messages than this — clearing a 3-message chat saves
	// nothing and just loses useful context. Default 6.
	MinMessagesBeforeClear int

	// ClearKeepSystemPrompt: if true, clearing preserves the initial
	// system-prompt-ish user turn (some roles seed state in the
	// first message). Most callers want this.
	ClearKeepSystemPrompt bool
}

// DefaultClearPolicy is a reasonable starting config. Platform roles
// that don't want clearing pass `ClearPolicy{}` (zero value disables).
var DefaultClearPolicy = ClearPolicy{
	TerminalToolNames: []string{
		"audit_output", "audit2_output", "fix_output",
		"evaluate_output", "merge_output",
		"biz_review_output", "assess_output",
		"analyze_output", "chief_output",
	},
	IdleClearAfter:         30 * time.Minute,
	TopicShiftMinTurns:     3,
	TopicShiftJaccardMax:   0.10,
	MinMessagesBeforeClear: 6,
	ClearKeepSystemPrompt:  true,
}

// maybeClear decides whether the transcript should be dropped in
// favour of a clean-slate restart. Runs before the compact tiers; a
// successful clear makes micro/summarize no-ops on the next iteration.
//
// Returns the (possibly cleared) messages + a reason string. Empty
// reason means "didn't clear".
//
// Important: clear triggers key on *genuine* new user turns, not on
// tool_result-carrying user messages (the runner wraps every
// tool_result batch as a Role=user message, which is a protocol
// requirement, not a semantic "human just typed"). Earlier drafts
// conflated the two and fired a clear after every tool turn, forcing
// the model into an infinite terminal-tool loop. The
// lastRealUserIx helper enforces the distinction.
func maybeClear(
	cs *CompactionState,
	pol ClearPolicy,
	messages []llm.Message,
	now time.Time,
) ([]llm.Message, string) {
	if len(messages) < pol.MinMessagesBeforeClear {
		return messages, ""
	}

	lastUserIx := lastRealUserIx(messages)

	// Signal 1: terminal-tool emission already in transcript and a
	// fresh user turn arrived after it. Walk backwards finding the
	// *last* real user turn, then see if an earlier assistant turn
	// emitted a terminal tool.
	if len(pol.TerminalToolNames) > 0 && lastUserIx > 0 {
		terminalSet := make(map[string]bool, len(pol.TerminalToolNames))
		for _, n := range pol.TerminalToolNames {
			terminalSet[n] = true
		}
		// Look at everything BEFORE the last real user turn — if a
		// terminal-output tool was emitted in there, that work-unit
		// is done; the new user turn starts fresh.
		for i := 0; i < lastUserIx; i++ {
			if messages[i].Role != llm.RoleAssistant {
				continue
			}
			for _, blk := range messages[i].Content {
				if blk.Type == llm.BlockToolUse && terminalSet[blk.ToolName] {
					cs.clearsFired++
					return applyClear(messages, lastUserIx, pol), "terminal-output"
				}
			}
		}
	}

	// Signal 2: idle gap — dormant for a long stretch, clear on the
	// assumption the operator has moved on between tasks.
	if pol.IdleClearAfter > 0 && !cs.lastUserTurnAt.IsZero() {
		if now.Sub(cs.lastUserTurnAt) > pol.IdleClearAfter && lastUserIx > 0 {
			cs.clearsFired++
			return applyClear(messages, lastUserIx, pol), "idle-gap"
		}
	}

	// Signal 3: topic-shift Jaccard on user turns.
	if pol.TopicShiftJaccardMax > 0 {
		userTurns := collectUserTexts(messages)
		if len(userTurns) >= pol.TopicShiftMinTurns {
			latest := userTurns[len(userTurns)-1]
			// Compare latest to the union of the previous N user
			// turns. Union compensates for short latest messages.
			prior := strings.Join(userTurns[:len(userTurns)-1], " ")
			j := jaccard(tokenSet(latest), tokenSet(prior))
			if j < pol.TopicShiftJaccardMax && lastUserIx > 0 {
				cs.clearsFired++
				return applyClear(messages, lastUserIx, pol), "topic-shift"
			}
		}
	}

	return messages, ""
}

// lastRealUserIx finds the most recent user-role message that carries
// at least one BlockText block, i.e. actually represents something a
// human typed (or a system-generated seed that looks like a user
// turn). Messages whose content is entirely tool_result blocks are
// skipped — they exist for protocol reasons (both Anthropic and
// OpenAI wrap tool_result blocks inside user turns) but semantically
// they're the tail end of an assistant turn, not a new user turn.
// Returns -1 when no such message exists.
func lastRealUserIx(messages []llm.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != llm.RoleUser {
			continue
		}
		hasText := false
		for _, blk := range messages[i].Content {
			if blk.Type == llm.BlockText {
				hasText = true
				break
			}
		}
		if hasText {
			return i
		}
	}
	return -1
}

// applyClear builds a fresh transcript: optionally the very first
// message (as "system seed") + the latest user turn.
//
// Resetting microCompactedIDs is crucial — ids from the pre-clear
// transcript are gone, and carrying them over would prevent future
// microcompacts on a replayed id (unlikely but cheap to avoid).
func applyClear(messages []llm.Message, lastUserIx int, pol ClearPolicy) []llm.Message {
	out := make([]llm.Message, 0, 2)
	if pol.ClearKeepSystemPrompt && len(messages) > 0 {
		// Keep the original user seed only if it was a user-role
		// text-only turn (the convention for seeded "here's your
		// task" intros). Skip if the first message is an assistant
		// turn from a previously-clear session.
		first := messages[0]
		if first.Role == llm.RoleUser && len(first.Content) == 1 && first.Content[0].Type == llm.BlockText {
			out = append(out, first)
		}
	}
	out = append(out, messages[lastUserIx])
	return out
}

// ClearStats exposes consumed telemetry so the loop can log + emit.
func (cs *CompactionState) ClearStats() int { return cs.clearsFired }

// MarkUserTurn is called by the loop whenever a new user-role message
// is appended so the idle detector has a timestamp to compare against.
func (cs *CompactionState) MarkUserTurn(t time.Time) {
	cs.lastUserTurnAt = t
}

// collectUserTexts pulls all user-role text blocks in order so the
// topic-shift detector can diff them.
func collectUserTexts(messages []llm.Message) []string {
	var out []string
	for _, m := range messages {
		if m.Role != llm.RoleUser {
			continue
		}
		var sb strings.Builder
		for _, blk := range m.Content {
			if blk.Type == llm.BlockText {
				sb.WriteString(blk.Text)
				sb.WriteByte(' ')
			}
		}
		s := strings.TrimSpace(sb.String())
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// tokenSet is a cheap lowercase word-set for Jaccard. Strips
// punctuation, drops tokens shorter than 2 chars (the / a / is / of /
// …) — otherwise every sentence has huge overlap through stopwords.
func tokenSet(s string) map[string]struct{} {
	set := make(map[string]struct{})
	var cur strings.Builder
	flush := func() {
		t := strings.ToLower(strings.TrimSpace(cur.String()))
		cur.Reset()
		if len(t) < 3 {
			return
		}
		// Dead-simple stopword list — covers the big offenders in
		// English + Chinese romanisation. Empirically enough for the
		// Jaccard threshold to behave.
		switch t {
		case "the", "and", "for", "with", "that", "this", "you", "are", "not",
			"will", "from", "have", "was", "were", "but", "has", "its",
			"can", "i'm", "i've", "don't", "one", "two", "all", "any",
			"我要", "我是", "你是", "那个", "就是", "然后", "因为",
			"什么", "怎么", "可以", "这个", "我们", "他们", "就要":
			return
		}
		set[t] = struct{}{}
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r >= 0x4e00 {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return set
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
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
