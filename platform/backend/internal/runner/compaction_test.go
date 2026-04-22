package runner

// Tests for context compaction. Covers:
//   - threshold math (autoCompactThreshold)
//   - estimateMessageTokens reasonable approximation
//   - findKeepCutoff boundary cases
//   - microCompact stripping policy (what gets stripped, what doesn't)
//   - circuit breaker (3 consecutive summarize failures disables)
//   - maybeCompact orchestration (below/above threshold, tier-1 saves
//     enough to skip tier-2, etc.)

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/a3c/platform/internal/llm"
)

// ---- threshold math --------------------------------------------------

func TestAutoCompactThreshold_DefaultsMatchClaudeCode(t *testing.T) {
	pol := DefaultCompactionPolicy
	// 200k window - 20k reserve - 13k buffer = 167k
	got := autoCompactThreshold(pol)
	want := 167_000
	if got != want {
		t.Errorf("default threshold = %d; want %d", got, want)
	}
}

func TestAutoCompactThreshold_MisconfiguredReturnsZero(t *testing.T) {
	// If someone passes reserve > window, effective is negative —
	// threshold function must return 0 (which callers treat as
	// "compaction disabled") rather than an obscure negative number.
	pol := CompactionPolicy{ContextWindow: 1000, ReserveForSummary: 2000}
	got := autoCompactThreshold(pol)
	if got != 0 {
		t.Errorf("misconfigured threshold = %d; want 0", got)
	}
}

// ---- token estimation ------------------------------------------------

func TestEstimateMessageTokens_Empty(t *testing.T) {
	if got := estimateMessageTokens(nil); got != 0 {
		t.Errorf("empty = %d; want 0", got)
	}
}

func TestEstimateMessageTokens_ReasonableScale(t *testing.T) {
	// Rough sanity check: 4000 chars of text ≈ 1000 tokens raw,
	// padded 4/3 → ~1333. Must not return an order-of-magnitude
	// wrong answer.
	msg := llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{
			llm.NewTextBlock(strings.Repeat("a", 4000)),
		},
	}
	got := estimateMessageTokens([]llm.Message{msg})
	if got < 800 || got > 2000 {
		t.Errorf("4000 chars → %d tokens; want ~1333 (800..2000)", got)
	}
}

func TestEstimateMessageTokens_IncludesToolBlocks(t *testing.T) {
	// ToolUse + ToolResult both contribute — a conversation with 10
	// file reads should register a token count, not zero.
	msgs := []llm.Message{{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			llm.NewToolUseBlock("id1", "read", json.RawMessage(`{"path":"a.go"}`)),
		},
	}, {
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{
			llm.NewToolResultBlock("id1", strings.Repeat("b", 2000), false),
		},
	}}
	if got := estimateMessageTokens(msgs); got < 400 {
		t.Errorf("tool blocks contributed only %d tokens — should be ~670", got)
	}
}

// ---- findKeepCutoff --------------------------------------------------

func TestFindKeepCutoff_KeepsLastNAssistants(t *testing.T) {
	// Conversation: user, asst, user, asst, user, asst, user, asst
	// keep=2 should return the index of the 2nd-most-recent assistant
	// message so everything from there onward is preserved.
	msgs := []llm.Message{
		{Role: llm.RoleUser}, {Role: llm.RoleAssistant}, // 0, 1
		{Role: llm.RoleUser}, {Role: llm.RoleAssistant}, // 2, 3
		{Role: llm.RoleUser}, {Role: llm.RoleAssistant}, // 4, 5
		{Role: llm.RoleUser}, {Role: llm.RoleAssistant}, // 6, 7
	}
	// Want: cutoff=5 so that indices 5,6,7 are preserved (the last 2
	// assistant messages are at indices 5 and 7).
	cutoff := findKeepCutoff(msgs, 2)
	if cutoff != 5 {
		t.Errorf("keep=2 → cutoff=%d, want 5", cutoff)
	}
}

func TestFindKeepCutoff_KeepZero_StripsEverything(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser}, {Role: llm.RoleAssistant}}
	if got := findKeepCutoff(msgs, 0); got != 2 {
		t.Errorf("keep=0 cutoff=%d, want %d (len)", got, len(msgs))
	}
}

func TestFindKeepCutoff_FewerThanKeep_DoesntStrip(t *testing.T) {
	// Only 1 assistant message, keep=3 requested → nothing to strip.
	msgs := []llm.Message{{Role: llm.RoleUser}, {Role: llm.RoleAssistant}}
	if got := findKeepCutoff(msgs, 3); got != 0 {
		t.Errorf("fewer-than-keep cutoff=%d, want 0 (strip nothing)", got)
	}
}

// ---- microCompact ----------------------------------------------------

// helper: build a turn = assistant(tool_use) + user(tool_result) pair
func mkToolTurn(toolID, toolName, input, result string) []llm.Message {
	return []llm.Message{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				llm.NewToolUseBlock(toolID, toolName, json.RawMessage(input)),
			},
		},
		{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{
				llm.NewToolResultBlock(toolID, result, false),
			},
		},
	}
}

func TestMicroCompact_StripsOldEphemeralToolResults(t *testing.T) {
	// 5 turns of read — all ephemeral. With keep=2 the OLDEST 3 get
	// their results stripped; the last 2 should stay intact.
	var msgs []llm.Message
	for i := 0; i < 5; i++ {
		toolID := "id-" + string(rune('a'+i))
		result := strings.Repeat("content-of-"+toolID, 100) // bulky
		msgs = append(msgs, mkToolTurn(toolID, "read",
			`{"path":"`+toolID+`.go"}`, result)...)
	}

	cs := newCompactionState()
	out, stripped := microCompact(cs, msgs, 2)

	if stripped != 3 {
		t.Errorf("stripped count = %d, want 3", stripped)
	}
	// Find all tool_result blocks in the output and verify which are
	// stripped.
	var resultBodies []string
	for _, m := range out {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				resultBodies = append(resultBodies, b.ToolResult)
			}
		}
	}
	if len(resultBodies) != 5 {
		t.Fatalf("expected 5 tool_result blocks, got %d", len(resultBodies))
	}
	// First 3 should be placeholder, last 2 should be the original.
	for i := 0; i < 3; i++ {
		if resultBodies[i] != strippedPlaceholder {
			t.Errorf("result[%d] should be stripped, got %q (first 20)",
				i, resultBodies[i][:min(20, len(resultBodies[i]))])
		}
	}
	for i := 3; i < 5; i++ {
		if resultBodies[i] == strippedPlaceholder {
			t.Errorf("result[%d] should be preserved but was stripped", i)
		}
	}
}

func TestMicroCompact_DoesNotStripNonEphemeralTools(t *testing.T) {
	// A turn with audit_output (platform tool, NOT ephemeral) must
	// survive microcompact even if it's old. Platform tool outputs
	// are semantic results the downstream logic cares about.
	msgs := mkToolTurn("x", "audit_output",
		`{"result":"merge"}`, `audit verdict recorded`)
	// add 3 more ephemeral turns after so the audit_output is "old"
	for i := 0; i < 3; i++ {
		toolID := "r" + string(rune('0'+i))
		msgs = append(msgs, mkToolTurn(toolID, "read",
			`{"path":"x"}`, strings.Repeat("y", 500))...)
	}

	cs := newCompactionState()
	out, _ := microCompact(cs, msgs, 2)

	// Find the audit_output result — must still be the original text.
	found := false
	for _, m := range out {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && b.ToolUseID == "x" {
				if b.ToolResult != "audit verdict recorded" {
					t.Errorf("audit_output result got stripped: %q", b.ToolResult)
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("audit_output tool_result disappeared from output")
	}
}

func TestMicroCompact_EmptyConversation(t *testing.T) {
	cs := newCompactionState()
	out, stripped := microCompact(cs, nil, 3)
	if len(out) != 0 || stripped != 0 {
		t.Errorf("empty in → out=%d stripped=%d, want 0,0", len(out), stripped)
	}
}

// ---- maybeCompact orchestration --------------------------------------

func TestMaybeCompact_BelowThreshold_NoOp(t *testing.T) {
	// Small transcript, policy with huge window → nothing happens.
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock("hi")}},
	}
	cs := newCompactionState()
	out, outcome := maybeCompact(nil, cs, DefaultCompactionPolicy, "", "", msgs, 100)
	if outcome.Action != "below-threshold" {
		t.Errorf("action = %q, want below-threshold", outcome.Action)
	}
	if len(out) != len(msgs) {
		t.Errorf("messages changed; should be a no-op")
	}
}

func TestMaybeCompact_DisabledWhenWindowZero(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.NewTextBlock(strings.Repeat("x", 1_000_000))}},
	}
	cs := newCompactionState()
	pol := CompactionPolicy{ContextWindow: 0}
	_, outcome := maybeCompact(nil, cs, pol, "", "", msgs, 0)
	if outcome.Action != "disabled" {
		t.Errorf("action = %q, want disabled", outcome.Action)
	}
}

func TestMaybeCompact_CircuitBrokenStopsRetrying(t *testing.T) {
	// Pre-load the state as if 3 failures already happened.
	cs := newCompactionState()
	cs.consecutiveFailures = 3
	pol := DefaultCompactionPolicy
	_, outcome := maybeCompact(nil, cs, pol, "", "", nil, 999_999)
	if outcome.Action != "circuit-broken" {
		t.Errorf("action = %q, want circuit-broken", outcome.Action)
	}
}

func TestMaybeCompact_MicroCompactAlone_SavesEnough(t *testing.T) {
	// Build a transcript that's over threshold purely because of 5
	// bulky read tool_results. MicroCompact with keep=2 strips 3 of
	// them, bringing us well below threshold — tier 2 must NOT fire.
	pol := CompactionPolicy{
		ContextWindow:               1_000,
		ReserveForSummary:           100,
		AutoCompactBuffer:           100,
		MaxConsecutiveFailures:      3,
		MicroCompactKeepRecentTurns: 2,
	}
	// threshold = 1000 - 100 - 100 = 800 tokens ≈ 2400 chars raw
	// 5 turns × 800 chars of tool_result ≈ 4000 chars = ~1333 tokens → over
	// After stripping 3 oldest: 2 × 800 chars = 1600 chars = ~533 tokens → under
	var msgs []llm.Message
	for i := 0; i < 5; i++ {
		tid := "t" + string(rune('0'+i))
		msgs = append(msgs, mkToolTurn(tid, "read",
			`{"path":"x"}`, strings.Repeat("z", 800))...)
	}

	cs := newCompactionState()
	// Pass a measured token count that's above threshold to force the
	// compaction decision deterministically.
	out, outcome := maybeCompact(nil, cs, pol, "", "", msgs, 900)
	if outcome.Action != "microcompact" {
		t.Errorf("action = %q, want microcompact (tier 2 should not fire)", outcome.Action)
	}
	if outcome.StrippedResults != 3 {
		t.Errorf("stripped = %d, want 3", outcome.StrippedResults)
	}
	if len(out) != len(msgs) {
		t.Errorf("microcompact changed message count %d → %d", len(msgs), len(out))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestEphemeralList_MatchesBuiltinNames guards against a silent drift
// where a builtin read tool gets renamed but the ephemeral list still
// references the old name — microCompact would then strip nothing.
// Keep this check simple: every name IN the list must correspond to
// a registered builtin tool that returns true for IsConcurrencySafe
// (our operational proxy for "read-only, bulky output, stripable").
func TestEphemeralList_MatchesBuiltinNames(t *testing.T) {
	builtins := map[string]Tool{
		(ReadTool{}).Name(): ReadTool{},
		(GlobTool{}).Name(): GlobTool{},
		(GrepTool{}).Name(): GrepTool{},
	}
	for name := range ephemeralToolNames {
		tool, ok := builtins[name]
		if !ok {
			t.Errorf("ephemeral list references %q but no builtin is registered under that name", name)
			continue
		}
		if !tool.IsConcurrencySafe(nil) {
			t.Errorf("ephemeral tool %q should be concurrency-safe (read-only)", name)
		}
	}
}
