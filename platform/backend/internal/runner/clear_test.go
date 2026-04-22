package runner

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/a3c/platform/internal/llm"
)

// mkText is a tiny builder so individual tests don't repeat the
// llm.Message{Role: ..., Content: [{Type: Text, Text: ...}]} ritual.
func mkText(role llm.Role, text string) llm.Message {
	return llm.Message{Role: role, Content: []llm.ContentBlock{llm.NewTextBlock(text)}}
}

// mkToolUse builds an assistant message whose content is a single
// tool_use of the given name. The input is empty — we're testing
// whether maybeClear picks up the tool name, not what it was called
// with.
func mkToolUse(name string) llm.Message {
	return llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			llm.NewToolUseBlock("tu_"+name, name, json.RawMessage(`{}`)),
		},
	}
}

// TestMaybeClear_TerminalOutputTriggersOnNextUserTurn: the canonical
// work-unit-finished case. Chief emits chief_output, then a new user
// message arrives; the transcript should reset.
func TestMaybeClear_TerminalOutputTriggersOnNextUserTurn(t *testing.T) {
	cs := newCompactionState()
	pol := DefaultClearPolicy
	// 8 messages: user + assistant back-and-forth, with chief_output
	// in the middle, then a fresh user question at the end.
	messages := []llm.Message{
		mkText(llm.RoleUser, "please summarize the platform"),
		mkText(llm.RoleAssistant, "let me check"),
		mkText(llm.RoleUser, "tool result 1"),
		mkToolUse("chief_output"),
		mkText(llm.RoleUser, "tool result for output"),
		mkText(llm.RoleAssistant, "there you go"),
		mkText(llm.RoleUser, "unrelated: add an OAuth task"),
	}
	out, reason := maybeClear(cs, pol, messages, time.Now())
	if reason != "terminal-output" {
		t.Fatalf("expected terminal-output, got %q", reason)
	}
	// Result keeps system seed (first user message) + latest user message.
	if len(out) != 2 {
		t.Fatalf("expected 2 messages after clear, got %d: %v", len(out), out)
	}
	latest := out[len(out)-1]
	if latest.Role != llm.RoleUser {
		t.Errorf("expected last to be user, got %v", latest.Role)
	}
	if got := latest.Content[0].Text; !strings.Contains(got, "OAuth") {
		t.Errorf("last user turn should be the fresh one; got %q", got)
	}
	if cs.ClearStats() != 1 {
		t.Errorf("clearsFired not incremented")
	}
}

// TestMaybeClear_TopicShift: when the latest user message has near-
// zero token overlap with the earlier conversation, clear.
func TestMaybeClear_TopicShift(t *testing.T) {
	cs := newCompactionState()
	pol := DefaultClearPolicy
	messages := []llm.Message{
		mkText(llm.RoleUser, "what agents are running on the auth milestone"),
		mkText(llm.RoleAssistant, "three agents alice bob carol"),
		mkText(llm.RoleUser, "ok and who owns the session refresh piece"),
		mkText(llm.RoleAssistant, "bob owns session refresh today"),
		mkText(llm.RoleUser, "how are the auth tests looking today"),
		mkText(llm.RoleAssistant, "passing all green on auth branch"),
		mkText(llm.RoleUser, "describe quantum entanglement for me please"),
	}
	out, reason := maybeClear(cs, pol, messages, time.Now())
	if reason != "topic-shift" {
		t.Fatalf("expected topic-shift, got %q (len=%d)", reason, len(out))
	}
	if len(out) < 1 {
		t.Fatal("expected non-empty transcript after clear")
	}
	latest := out[len(out)-1]
	if got := latest.Content[0].Text; !strings.Contains(got, "quantum") {
		t.Errorf("latest user turn not preserved correctly: %q", got)
	}
}

// TestMaybeClear_NoShiftOnRelatedFollowUp: guard against false
// positives — a related follow-up should NOT trigger a clear.
func TestMaybeClear_NoShiftOnRelatedFollowUp(t *testing.T) {
	cs := newCompactionState()
	pol := DefaultClearPolicy
	messages := []llm.Message{
		mkText(llm.RoleUser, "what agents are running on the auth milestone right now"),
		mkText(llm.RoleAssistant, "three agents auth auth milestone — alice, bob, carol"),
		mkText(llm.RoleUser, "ok and who owns the session refresh auth piece right now"),
		mkText(llm.RoleAssistant, "bob owns session refresh auth milestone"),
		mkText(llm.RoleUser, "and what about the alice agent on auth session right now"),
	}
	_, reason := maybeClear(cs, pol, messages, time.Now())
	if reason != "" {
		t.Fatalf("follow-up should NOT trigger a clear; got %q", reason)
	}
}

// TestMaybeClear_IdleGap: a 45-minute gap since the last user turn
// should trigger an idle clear regardless of lexical overlap.
func TestMaybeClear_IdleGap(t *testing.T) {
	cs := newCompactionState()
	pol := DefaultClearPolicy
	// Mark last user turn as being 45 minutes ago.
	cs.MarkUserTurn(time.Now().Add(-45 * time.Minute))
	messages := []llm.Message{
		mkText(llm.RoleUser, "what's happening on the auth milestone"),
		mkText(llm.RoleAssistant, "three agents on auth"),
		mkText(llm.RoleUser, "who's working on auth"),
		mkText(llm.RoleAssistant, "bob is on auth"),
		mkText(llm.RoleUser, "and what is the auth progress today"),
		mkText(llm.RoleAssistant, "80 percent of the auth milestone shipped"),
		mkText(llm.RoleUser, "and what is the auth progress now"),
	}
	_, reason := maybeClear(cs, pol, messages, time.Now())
	if reason != "idle-gap" {
		t.Fatalf("expected idle-gap clear after 45min dormancy; got %q", reason)
	}
}

// TestMaybeClear_BelowMinMessages: clearing a 3-message chat saves
// nothing and loses useful context.
func TestMaybeClear_BelowMinMessages(t *testing.T) {
	cs := newCompactionState()
	pol := DefaultClearPolicy
	messages := []llm.Message{
		mkText(llm.RoleUser, "hi"),
		mkText(llm.RoleAssistant, "hey"),
		mkText(llm.RoleUser, "something completely different"),
	}
	_, reason := maybeClear(cs, pol, messages, time.Now())
	if reason != "" {
		t.Errorf("expected no clear for short transcript, got %q", reason)
	}
}

// TestMaybeClear_ZeroPolicyNoOp: empty ClearPolicy must be a no-op.
// This is the backward-compat escape hatch for roles that don't want
// any clearing.
func TestMaybeClear_ZeroPolicyNoOp(t *testing.T) {
	cs := newCompactionState()
	messages := []llm.Message{
		mkText(llm.RoleUser, "msg 1"),
		mkText(llm.RoleAssistant, "reply 1"),
		mkToolUse("chief_output"),
		mkText(llm.RoleUser, "something else"),
		mkText(llm.RoleAssistant, "ok"),
		mkText(llm.RoleUser, "final"),
	}
	_, reason := maybeClear(cs, ClearPolicy{}, messages, time.Now())
	if reason != "" {
		t.Errorf("zero policy should be a no-op, got %q", reason)
	}
}

// TestJaccard_KnownPairs: pin the similarity math with clear cases so
// regressions in tokenSet/jaccard are obvious.
func TestJaccard_KnownPairs(t *testing.T) {
	cases := []struct {
		name     string
		a, b     string
		minScore float64 // lower bound
		maxScore float64 // upper bound
	}{
		{"identical", "merge the branch into main", "merge the branch into main", 0.9, 1.01},
		{"paraphrase", "merge the auth branch into main", "merge the auth branch to master", 0.3, 0.9},
		{"unrelated", "merge auth branch", "describe quantum entanglement cat", 0.0, 0.05},
		{"empty-one-side", "merge the branch", "", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := jaccard(tokenSet(c.a), tokenSet(c.b))
			if got < c.minScore || got > c.maxScore {
				t.Errorf("jaccard(%q, %q) = %.3f, want in [%.2f,%.2f]",
					c.a, c.b, got, c.minScore, c.maxScore)
			}
		})
	}
}

// TestApplyClear_KeepsSeedAndLatest: the output shape must always be
// [seed?, latest user]. Middle turns are gone.
func TestApplyClear_KeepsSeedAndLatest(t *testing.T) {
	pol := DefaultClearPolicy
	messages := []llm.Message{
		mkText(llm.RoleUser, "SEED TASK: audit the auth module"),
		mkText(llm.RoleAssistant, "let me look"),
		mkText(llm.RoleUser, "tool result"),
		mkText(llm.RoleAssistant, "reply"),
		mkText(llm.RoleUser, "FRESH QUESTION"),
	}
	out := applyClear(messages, 4, pol)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages (seed + latest), got %d", len(out))
	}
	if !strings.Contains(out[0].Content[0].Text, "SEED") {
		t.Errorf("seed not preserved: %q", out[0].Content[0].Text)
	}
	if !strings.Contains(out[1].Content[0].Text, "FRESH") {
		t.Errorf("latest not preserved: %q", out[1].Content[0].Text)
	}
}
