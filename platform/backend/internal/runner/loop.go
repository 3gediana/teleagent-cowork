package runner

// Runner loop — the heart of the native agent runtime.
//
// One Run() call == one full agent turn-loop == one opencode session
// equivalent. We feed the LLM a system prompt + user task, consume the
// streaming response, dispatch tool_use events to tools in our
// Registry, build tool_result messages, and feed the whole thing back
// for the next turn. Exits when the model produces a stop with no
// outstanding tool calls.
//
// What this replaces: opencode.Scheduler.Dispatch + opencode serve's
// internal loop. We now control every step, which means we can:
//   - Inject token-budget cutoffs without patching opencode
//   - Persist a full journal per tool call (input + output + timing)
//   - Hot-swap models per role without restarting
//   - Build domain-specific sub-tools without shelling out
//
// Non-goals today:
//   - Sub-agents / agent chaining (a tool invoking another full Run).
//   - Checkpointing / resume across server restarts.
//
// Parallel tool execution (Anthropic's tool_use array can carry
// multiple pending calls) landed in Phase 4: see dispatch.go for the
// partition + bounded-goroutine-pool logic.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/llm"
)

// RunOptions is the knob bag for a single Run. Most fields have safe
// defaults — only EndpointID, Model, and SystemPrompt are required.
type RunOptions struct {
	// EndpointID identifies the target llm.Registry entry. Must be
	// non-empty; runner does NOT fall back to a "first active
	// endpoint" here because that's a surprising action at runtime.
	// The dispatcher handles defaulting before calling Run.
	EndpointID string

	// Model picks a specific model id from the endpoint's catalogue.
	// Empty falls back to the endpoint's DefaultModel.
	Model string

	// SystemPrompt is the role-specific template output (from
	// agent.BuildPrompt). Becomes the `system` field on the Messages
	// API request.
	SystemPrompt string

	// UserInput is the initial user-turn content — typically the
	// rendered task brief for this session.
	UserInput string

	// MaxTokens is the per-response output budget. Defaults to 8192.
	// Tools that produce long results (read+paste of a big file) can
	// still blow past this; the runner splits via summarisation if
	// that happens (future work).
	MaxTokens int

	// Reasoning enables Anthropic extended thinking when the model
	// supports it. Ignored by the OpenAI adapter.
	Reasoning llm.ReasoningEffort

	// MaxIterations caps how many turn-loops we'll do before forcing
	// a stop. Prevents runaway loops from a buggy model that refuses
	// to emit stop_reason=end_turn. Default 20 — generous enough for
	// complex audit/fix flows, tight enough to trip cheaply.
	MaxIterations int

	// ToolChoice lets a role insist the first turn MUST call a tool
	// (e.g. audit roles that MUST emit audit_output). The string
	// matches llm.ChatRequest.ToolChoice semantics: "" (auto),
	// "any" (must call some tool), "none" (no tools), or a specific
	// tool name. Useful for locking outputs in a schema the rest of
	// the platform can consume.
	ToolChoice string

	// Compaction controls the long-conversation survival mechanism.
	// See compaction.go for the two-tier design. Zero value (empty
	// struct) = disabled; callers passing an explicit policy get the
	// auto-compact behaviour. DefaultCompactionPolicy is a reasonable
	// starting point for a 200k-window model.
	Compaction CompactionPolicy

	// Clear controls tier-0 hard clears at semantic boundaries —
	// terminal-tool emission, topic shifts, idle gaps. Zero value
	// disables (back-compat). DefaultClearPolicy is the recommended
	// config for Chief / chat-like sessions; audit/fix roles can
	// leave this disabled since their sessions are single-shot and
	// short-lived.
	Clear ClearPolicy
}

// RunResult captures everything a caller might want to inspect after
// the loop exits.
type RunResult struct {
	// FinalText is the assistant's final textual reply, excluding any
	// tool_use blocks. Often empty for audit-style roles where the
	// final turn is entirely a tool call.
	FinalText string

	// StopReason is the last stop_reason the model emitted.
	StopReason llm.StopReason

	// Usage totals summed across every turn in the loop.
	Usage llm.Usage

	// Iterations is how many turn-loops were needed.
	Iterations int

	// Journal is the tool-call log (one entry per Execute call).
	// Tools populate this via the runner's Session; exposed here for
	// the dispatcher to persist onto AgentSession.
	Journal []JournalEntry
}

// Run executes the full loop. Blocking — call from the dispatcher's
// goroutine. Honours ctx cancellation at every iteration boundary.
func Run(ctx context.Context, sess *agent.Session, reg *Registry, opts RunOptions) (*RunResult, error) {
	if opts.EndpointID == "" {
		return nil, fmt.Errorf("runner: EndpointID is required")
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 8192
	}
	if opts.MaxIterations == 0 {
		opts.MaxIterations = 20
	}

	rsess := &RunnerSession{
		AgentSession: sess,
		EndpointID:   opts.EndpointID,
		Model:        opts.Model,
	}

	// Build the tool descriptor list once — it doesn't change mid-loop.
	tools := make([]llm.ToolDef, 0)
	for _, t := range reg.List() {
		tools = append(tools, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.InputSchema(),
		})
	}

	// Start the running transcript. Seed with the user-turn from opts.
	messages := []llm.Message{
		llm.NewUserText(opts.UserInput),
	}

	var (
		finalText  strings.Builder
		totalUsage llm.Usage
		lastStop   llm.StopReason
	)

	// Derive the fanout ids for emit() once — reading them off the
	// session lets tools emit without passing values around.
	projectID := ""
	sessionID := ""
	if sess != nil {
		sessionID = sess.ID
		projectID = sess.ProjectID
	}

	// Emit a single AGENT_ERROR when Run exits with an error from any
	// path below. Centralised via a closure so the multiple error
	// return sites don't duplicate the broadcast bookkeeping.
	emitErr := func(err error) error {
		emit(projectID, EventAgentError, map[string]interface{}{
			"session_id": sessionID,
			"error":      err.Error(),
		})
		return err
	}

	// Compaction state — persists across iterations so the circuit
	// breaker and already-compacted-ids tracking survive the loop.
	compactState := newCompactionState()
	var lastTurnTokens int

	// Seed the idle-clear detector with the initial user turn.
	// Tier-0 needs *some* anchor timestamp to avoid firing on every
	// cold-started session.
	compactState.MarkUserTurn(time.Now())

	for iter := 1; iter <= opts.MaxIterations; iter++ {
		if err := ctx.Err(); err != nil {
			return nil, emitErr(fmt.Errorf("runner: cancelled at iteration %d: %w", iter, err))
		}

		// Tier-0: hard clear at semantic boundaries (terminal-output
		// emitted, topic shift, idle gap). Runs BEFORE compact —
		// clearing is cheaper than a summary and makes it moot.
		if opts.Clear.MinMessagesBeforeClear > 0 || len(opts.Clear.TerminalToolNames) > 0 || opts.Clear.IdleClearAfter > 0 {
			cleared, reason := maybeClear(compactState, opts.Clear, messages, time.Now())
			if reason != "" {
				log.Printf("[Compaction] iter=%d clear (%s): %d→%d messages",
					iter, reason, len(messages), len(cleared))
				emit(projectID, EventAgentTurn, map[string]interface{}{
					"session_id":   sessionID,
					"iteration":    iter,
					"compaction":   "cleared",
					"clear_reason": reason,
					"before_msgs":  len(messages),
					"after_msgs":   len(cleared),
				})
				messages = cleared
				// A clear invalidates micro/summarize tracking.
				compactState = newCompactionState()
				compactState.MarkUserTurn(time.Now())
				lastTurnTokens = 0
			}
		}

		// Tier-1/2 compact if needed BEFORE building the next
		// request. This way a summarize LLM call can fail without
		// aborting the parent run — we just proceed with whatever
		// transcript we have and let the next iteration try again.
		if opts.Compaction.ContextWindow > 0 {
			compacted, outcome := maybeCompact(ctx, compactState, opts.Compaction,
				opts.EndpointID, opts.Model, messages, lastTurnTokens)
			if outcome.Action == "microcompact" || outcome.Action == "summarized" {
				log.Printf("[Compaction] iter=%d %s: %d→%d tokens, stripped=%d, summary_cost=$%.4f",
					iter, outcome.Action, outcome.Before, outcome.Tokens,
					outcome.StrippedResults, outcome.SummaryUSD)
				emit(projectID, EventAgentTurn, map[string]interface{}{
					"session_id":  sessionID,
					"iteration":   iter,
					"compaction":  outcome.Action,
					"before":      outcome.Before,
					"after":       outcome.Tokens,
				})
			}
			messages = compacted
		}

		req := llm.ChatRequest{
			Model:     opts.Model,
			System:    opts.SystemPrompt,
			Messages:  messages,
			Tools:     tools,
			MaxTokens: opts.MaxTokens,
			Reasoning: opts.Reasoning,
		}
		// ToolChoice only applies on the first turn — after that the
		// model should be free to decide (or the loop will livelock
		// insisting on the same tool every turn).
		if iter == 1 && opts.ToolChoice != "" {
			req.ToolChoice = opts.ToolChoice
		}

		stream, err := llm.DefaultRegistry.ChatStream(ctx, opts.EndpointID, req)
		if err != nil {
			return nil, emitErr(fmt.Errorf("runner: iter %d ChatStream: %w", iter, err))
		}

		// Forward each EvTextDelta to the frontend as it arrives so
		// the dashboard can render live typewriter output. Closure
		// captures the per-iteration projectID/sessionID.
		onDelta := func(delta string) {
			emit(projectID, EventAgentTextDelta, map[string]interface{}{
				"session_id": sessionID,
				"iteration":  iter,
				"delta":      delta,
			})
		}

		turn, err := consumeStream(stream, onDelta)
		if err != nil {
			return nil, emitErr(fmt.Errorf("runner: iter %d stream: %w", iter, err))
		}

		// Per-turn summary: useful for dashboards plotting iteration
		// counts and token usage over time.
		emit(projectID, EventAgentTurn, map[string]interface{}{
			"session_id":    sessionID,
			"iteration":     iter,
			"input_tokens":  turn.Usage.InputTokens,
			"output_tokens": turn.Usage.OutputTokens,
			"tool_count":    len(turn.ToolCalls),
		})
		totalUsage.InputTokens += turn.Usage.InputTokens
		totalUsage.OutputTokens += turn.Usage.OutputTokens
		totalUsage.CacheReadTokens += turn.Usage.CacheReadTokens
		totalUsage.CacheCreationTokens += turn.Usage.CacheCreationTokens

		// Track the most recent input_tokens for the compaction
		// decision on the NEXT iteration. InputTokens is authoritative
		// ("what the provider just charged us for") — better than the
		// estimator because it includes system prompt + tools schema
		// that the estimator can't see.
		lastTurnTokens = turn.Usage.InputTokens
		totalUsage.USD += turn.Usage.USD
		lastStop = turn.StopReason

		// Assemble an assistant message mirroring what the model
		// streamed. The LLM API requires us to echo the tool_use
		// blocks back on the next turn alongside the tool_result.
		assistantBlocks := make([]llm.ContentBlock, 0, 1+len(turn.ToolCalls))
		if turn.Text != "" {
			assistantBlocks = append(assistantBlocks, llm.NewTextBlock(turn.Text))
		}
		for _, tc := range turn.ToolCalls {
			assistantBlocks = append(assistantBlocks,
				llm.NewToolUseBlock(tc.ID, tc.Name, tc.Input))
		}

		// Terminal condition: no tool calls → we're done.
		if len(turn.ToolCalls) == 0 {
			if turn.Text != "" {
				finalText.WriteString(turn.Text)
			}
			// Append the final assistant message (useful for audit
			// logs even though we don't send it back to the model).
			if len(assistantBlocks) > 0 {
				messages = append(messages, llm.Message{
					Role:    llm.RoleAssistant,
					Content: assistantBlocks,
				})
			}
			// Broadcast the final reply so the chat panel updates and
			// a clean AGENT_DONE so "running" badges flip to "complete".
			// Shape of CHAT_UPDATE matches opencode's exactly — no
			// frontend change needed for native-runtime sessions to
			// light up the existing chat surface.
			if finalText.Len() > 0 {
				// session_id is a native-runtime extension on top of
				// opencode's CHAT_UPDATE payload. Clients that know
				// about AGENT_TEXT_DELTA use it to swap the
				// typewriter-streaming message in place with the
				// finalised content; opencode clients ignore it.
				emit(projectID, EventChatUpdate, map[string]interface{}{
					"role":       "agent",
					"content":    finalText.String(),
					"session_id": sessionID,
				})
			}
			emit(projectID, EventAgentDone, map[string]interface{}{
				"session_id":    sessionID,
				"iterations":    iter,
				"input_tokens":  totalUsage.InputTokens,
				"output_tokens": totalUsage.OutputTokens,
				"cost_usd":      totalUsage.USD,
			})
			return &RunResult{
				FinalText:  finalText.String(),
				StopReason: lastStop,
				Usage:      totalUsage,
				Iterations: iter,
				Journal:    rsess.Journal,
			}, nil
		}

		// Intermediate: dispatch each tool call and collect results.
		messages = append(messages, llm.Message{
			Role:    llm.RoleAssistant,
			Content: assistantBlocks,
		})
		if turn.Text != "" {
			// Preserve partial narration (audit roles sometimes emit
			// brief reasoning before a tool call).
			finalText.WriteString(turn.Text)
			finalText.WriteString("\n")
		}

		// Dispatch all tool_use calls from this turn. Safe (read-only)
		// tools run in parallel bounded by MaxToolUseConcurrency;
		// unsafe tools (writes, platform sinks) run sequentially.
		// Order of result blocks is preserved to match the order of
		// tool_use blocks the model emitted — required by both
		// Anthropic and OpenAI APIs.
		resultBlocks, fatal := dispatchToolCalls(ctx, reg, rsess, projectID, sessionID, turn.ToolCalls)
		if fatal != nil {
			return nil, emitErr(fmt.Errorf("runner: %w", fatal))
		}

		// Tool-result block list is delivered as a single user turn;
		// both Anthropic and OpenAI APIs require this grouping.
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: resultBlocks,
		})
		compactState.MarkUserTurn(time.Now())
	}

	return nil, emitErr(fmt.Errorf("runner: exceeded MaxIterations=%d without a terminal turn (likely model loop)", opts.MaxIterations))
}

// toolCall is a temporary struct for assembling tool_use output from
// the stream before we turn it into llm.ContentBlock.
type toolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// turnState captures everything we pull out of one LLM round-trip.
type turnState struct {
	Text       string
	ToolCalls  []toolCall
	StopReason llm.StopReason
	Usage      llm.Usage
}

// consumeStream drains the channel into a turnState. Returns on
// EvMessageStop (success) or EvError (propagated as err).
//
// onDelta, when non-nil, fires for every EvTextDelta as it arrives.
// The runner uses this to forward token chunks to the frontend as
// AGENT_TEXT_DELTA events; tests pass nil for quiet execution.
func consumeStream(stream <-chan llm.StreamEvent, onDelta func(string)) (turnState, error) {
	var st turnState
	// toolIdx → in-progress toolCall. Anthropic's tool_use arrives as
	// start → input deltas → end, so we buffer per index.
	pending := map[string]*toolCall{}
	var currentToolID string

	var textSB strings.Builder
	for ev := range stream {
		switch ev.Type {
		case llm.EvTextDelta:
			textSB.WriteString(ev.TextDelta)
			if onDelta != nil && ev.TextDelta != "" {
				onDelta(ev.TextDelta)
			}

		case llm.EvThinkingDelta:
			// Ignore for now — thinking doesn't go into the transcript
			// the model sees on the next turn, and we're not surfacing
			// it to the frontend yet. Future work: stream to dashboard.

		case llm.EvToolUseStart:
			currentToolID = ev.ToolUseID
			pending[currentToolID] = &toolCall{
				ID:   ev.ToolUseID,
				Name: ev.ToolName,
			}

		case llm.EvToolUseEnd:
			if tc := pending[ev.ToolUseID]; tc != nil {
				tc.Input = ev.ToolInput
				st.ToolCalls = append(st.ToolCalls, *tc)
				delete(pending, ev.ToolUseID)
			}
			currentToolID = ""

		case llm.EvMessageStop:
			st.StopReason = ev.StopReason
			st.Usage = ev.Usage
			st.Text = textSB.String()
			return st, nil

		case llm.EvError:
			return st, ev.Err
		}
	}
	// Channel closed without a terminal event — treat as error.
	st.Text = textSB.String()
	return st, fmt.Errorf("runner: stream closed without terminal event")
}

func toolNames(reg *Registry) []string {
	tools := reg.List()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return names
}
