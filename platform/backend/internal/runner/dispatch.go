package runner

// Tool dispatch within a single assistant turn.
//
// Learned from Claude Code's services/tools/toolOrchestration.ts:
//   - Partition consecutive concurrency-safe tool calls into one
//     batch; everything else is a singleton batch that runs serially.
//   - Within a safe batch, fan out to a goroutine-pool bounded by
//     MaxToolUseConcurrency (default 10).
//   - Preserve tool_use / tool_result order: the LLM API requires
//     tool_result blocks to appear in the SAME order as the tool_use
//     blocks the model emitted. Concurrent execution within a batch
//     is fine as long as we emit results in input order.
//   - Tool outputs feed into `rsess.Journal` — protect with a mutex
//     when concurrent writers exist.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/a3c/platform/internal/llm"
)

// MaxToolUseConcurrency caps in-flight concurrent tool calls. Matches
// Claude Code's CLAUDE_CODE_MAX_TOOL_USE_CONCURRENCY default of 10.
// Safe roles like analyze routinely fan out 5-10 reads per turn;
// beyond that we're more likely to exhaust file descriptors than to
// gain meaningful wall-clock savings.
var MaxToolUseConcurrency = 10

// toolBatch is either a single unsafe tool or a run of consecutive
// safe tools that will be executed in parallel.
type toolBatch struct {
	safe  bool
	calls []toolCall
}

// partitionToolCalls walks the model's tool_use sequence and groups
// consecutive concurrency-safe calls together. Order is preserved;
// one unsafe call breaks the run and forces the next safe calls to
// start a fresh batch.
func partitionToolCalls(reg *Registry, calls []toolCall) []toolBatch {
	var out []toolBatch
	for _, tc := range calls {
		safe := isCallConcurrencySafe(reg, tc)
		if len(out) > 0 && out[len(out)-1].safe && safe {
			// Extend the current safe batch.
			out[len(out)-1].calls = append(out[len(out)-1].calls, tc)
			continue
		}
		// Either the previous batch was unsafe, or this call is
		// unsafe, or there's no batch yet. Start a new one.
		out = append(out, toolBatch{safe: safe, calls: []toolCall{tc}})
	}
	return out
}

// isCallConcurrencySafe answers "can this specific call run in
// parallel with siblings?" Missing tool = hallucination, treat as
// unsafe (we'll feed a synthetic error back to the model).
func isCallConcurrencySafe(reg *Registry, tc toolCall) bool {
	tool := reg.Get(tc.Name)
	if tool == nil {
		return false
	}
	// Defensive: tool authors might panic on exotic input. A panic
	// here must NOT crash the dispatcher — treat as unsafe.
	defer func() { _ = recover() }()
	return tool.IsConcurrencySafe(tc.Input)
}

// toolOutcome is the result of one tool_use execution, carrying the
// ContentBlock to thread back into the next turn's user message.
type toolOutcome struct {
	ToolCall toolCall
	Block    llm.ContentBlock
	Journal  JournalEntry
	Fatal    error
}

// dispatchToolCalls processes all tool_use calls from a single turn,
// honouring concurrency safety. Returns the ordered tool_result
// blocks to include in the next user message, plus any fatal error
// from a tool that should abort the whole run.
//
// Parameters beyond the call list are scoped context from the Loop —
// they're not great as a struct-arg because the Loop-local closures
// (emitEventForCall, recordTrace) depend on loop-scoped values like
// iteration number.
func dispatchToolCalls(
	ctx context.Context,
	reg *Registry,
	rsess *RunnerSession,
	projectID, sessionID string,
	calls []toolCall,
) ([]llm.ContentBlock, error) {
	results := make([]llm.ContentBlock, len(calls))
	journalEntries := make([]JournalEntry, 0, len(calls))
	var fatal error

	// Journal is shared — concurrent executions must append
	// deterministically (input-order). We collect per-call entries
	// into a slice keyed by the input index, then append under a
	// lock after each batch.
	batches := partitionToolCalls(reg, calls)
	indexByID := make(map[string]int, len(calls))
	for i, tc := range calls {
		indexByID[tc.ID] = i
	}

	for _, batch := range batches {
		if !batch.safe {
			// Serial path: one call, inline. This is also how
			// unknown-tool errors are emitted (the batch ends up
			// unsafe because isCallConcurrencySafe returns false for
			// missing tools).
			for _, tc := range batch.calls {
				oc := runOneTool(ctx, reg, rsess, projectID, sessionID, tc)
				results[indexByID[tc.ID]] = oc.Block
				journalEntries = append(journalEntries, oc.Journal)
				if oc.Fatal != nil {
					fatal = oc.Fatal
					break
				}
			}
			if fatal != nil {
				break
			}
			continue
		}

		// Concurrent path: bounded goroutine pool.
		outcomes := make([]toolOutcome, len(batch.calls))
		var wg sync.WaitGroup
		sem := make(chan struct{}, MaxToolUseConcurrency)
		for i, tc := range batch.calls {
			wg.Add(1)
			sem <- struct{}{} // blocks when pool is full
			go func(i int, tc toolCall) {
				defer wg.Done()
				defer func() { <-sem }()
				outcomes[i] = runOneTool(ctx, reg, rsess, projectID, sessionID, tc)
			}(i, tc)
		}
		wg.Wait()

		// Merge back in input order. Collect any fatal for return
		// after the whole batch has drained (so in-flight goroutines
		// finish their traces rather than being abandoned half-way).
		for i, oc := range outcomes {
			tc := batch.calls[i]
			results[indexByID[tc.ID]] = oc.Block
			journalEntries = append(journalEntries, oc.Journal)
			if oc.Fatal != nil && fatal == nil {
				fatal = oc.Fatal
			}
		}
		if fatal != nil {
			break
		}
	}

	// Append all journal entries to the session's journal. Single
	// writer here, no mutex needed.
	rsess.Journal = append(rsess.Journal, journalEntries...)

	return results, fatal
}

// runOneTool handles lookup + execution + trace + SSE broadcast for
// one tool call. Pure function of the inputs — safe to call from
// multiple goroutines (its writes go to the outcome struct; shared
// broadcasts + DB writes go through external fire-and-forget paths
// that are themselves concurrency-safe).
func runOneTool(
	ctx context.Context,
	reg *Registry,
	rsess *RunnerSession,
	projectID, sessionID string,
	tc toolCall,
) toolOutcome {
	// Broadcast the intent — frontend shows "running tool X" while
	// the actual work happens. Matches opencode's TOOL_CALL shape.
	argsMap := map[string]interface{}{}
	_ = json.Unmarshal(tc.Input, &argsMap)
	emit(projectID, EventToolCall, map[string]interface{}{
		"session_id": sessionID,
		"tool":       tc.Name,
		"args":       argsMap,
	})

	tool := reg.Get(tc.Name)
	if tool == nil {
		msg := fmt.Sprintf("Error: unknown tool %q. Available tools: %s",
			tc.Name, strings.Join(toolNames(reg), ", "))
		recordToolCallTrace(sessionID, projectID, tc.Name, tc.Input, msg, false)
		return toolOutcome{
			ToolCall: tc,
			Block:    llm.NewToolResultBlock(tc.ID, msg, true),
			Journal:  JournalEntry{ToolName: tc.Name, Input: tc.Input, Output: msg, IsError: true},
		}
	}

	start := time.Now()
	result, isErr, fatal := tool.Execute(ctx, rsess, tc.Input)
	elapsedMs := time.Since(start).Milliseconds()

	recordToolCallTrace(sessionID, projectID, tc.Name, tc.Input, result, !isErr && fatal == nil)

	entry := JournalEntry{
		ToolName:  tc.Name,
		Input:     tc.Input,
		Output:    result,
		IsError:   isErr,
		ElapsedMs: elapsedMs,
	}

	if fatal != nil {
		return toolOutcome{
			ToolCall: tc,
			Journal:  entry,
			Fatal:    fmt.Errorf("tool %s fatal: %w", tc.Name, fatal),
			// Block is nil — upstream checks Fatal and aborts; the
			// result block is never emitted to the model.
		}
	}

	return toolOutcome{
		ToolCall: tc,
		Block:    llm.NewToolResultBlock(tc.ID, result, isErr),
		Journal:  entry,
	}
}
