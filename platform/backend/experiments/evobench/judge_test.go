package main

// Tests for the judge response parser and the retry classifier.
// These are the two pieces that absorb model-side flakiness, so we
// exercise them on the actual response shapes we've seen in the
// wild (think blocks, fenced code, embedded strings, malformed
// truncations, etc).

import (
	"strings"
	"testing"
)

func TestParseJudgeOutput_StripsThinkBlocks(t *testing.T) {
	in := `<think>chain of thought blah blah {fake-json:"trap"}</think>
{"best":"art_1","ranking":["art_1","art_2"],"why":"art_1 directly addresses the auth bug"}`
	got := parseJudgeOutput(in)
	if got.Skipped {
		t.Fatalf("unexpected skip: %s", got.Err)
	}
	if got.BestID != "art_1" {
		t.Errorf("BestID: got %q want art_1", got.BestID)
	}
	if len(got.Ranking) != 2 || got.Ranking[0] != "art_1" {
		t.Errorf("Ranking: got %v want [art_1, art_2]", got.Ranking)
	}
}

func TestParseJudgeOutput_HandlesUnterminatedThink(t *testing.T) {
	// MiniMax-M2.7 occasionally truncates its own <think> block when
	// it hits max_tokens mid-thought. We must not crash; we should
	// just return Skipped with a helpful Err.
	in := `<think>halfway done thinking and then nothing closes`
	got := parseJudgeOutput(in)
	if !got.Skipped {
		t.Errorf("expected skip on unterminated think, got %+v", got)
	}
	if !strings.Contains(got.Err, "no json object") {
		t.Errorf("Err should mention 'no json object', got %q", got.Err)
	}
}

func TestParseJudgeOutput_ExtractsFencedJSON(t *testing.T) {
	// Some providers wrap output in ```json ... ``` even though the
	// prompt says no prose. The fenced extractor must take precedence
	// over the balanced-brace fallback so the fence markers don't
	// leak into the parsed JSON string.
	in := "Here is my answer:\n```json\n{\"best\":\"art_42\",\"ranking\":[\"art_42\"],\"why\":\"top match\"}\n```\nThanks."
	got := parseJudgeOutput(in)
	if got.Skipped {
		t.Fatalf("unexpected skip: %s", got.Err)
	}
	if got.BestID != "art_42" {
		t.Errorf("BestID: got %q want art_42", got.BestID)
	}
}

func TestParseJudgeOutput_BalancedBraceScanIgnoresStringContent(t *testing.T) {
	// The "why" field can legitimately contain { and } characters.
	// A naive first-{-to-last-} scan would cut at the wrong place; the
	// balanced scan must follow string state machine.
	in := `{"best":"art_X","ranking":["art_X"],"why":"matched {key:value} pattern in summary"}`
	got := parseJudgeOutput(in)
	if got.Skipped {
		t.Fatalf("unexpected skip: %s", got.Err)
	}
	if got.BestID != "art_X" {
		t.Errorf("BestID: got %q want art_X", got.BestID)
	}
	if !strings.Contains(got.Reason, "matched") {
		t.Errorf("Reason: got %q want substring 'matched'", got.Reason)
	}
}

func TestParseJudgeOutput_RejectsTruncatedJSON(t *testing.T) {
	// What we see in production when the body cap clips: opening
	// brace plus partial content but no matching close. The
	// balanced scan returns "" and we surface a Skipped result so
	// the retry layer can take over.
	in := `{"best":"art_1","ranking":["art_1","art_2",`
	got := parseJudgeOutput(in)
	if !got.Skipped {
		t.Errorf("truncated JSON must be Skipped, got %+v", got)
	}
}

func TestParseJudgeOutput_RejectsPlainProse(t *testing.T) {
	// Sanity: the model just talks instead of obeying the prompt.
	// We should fail closed (skip) rather than fabricate a result.
	in := `Sorry, I cannot rank these candidates without more information.`
	got := parseJudgeOutput(in)
	if !got.Skipped {
		t.Errorf("plain prose must be Skipped, got %+v", got)
	}
}

func TestIsTransientJudgeErr_TransientCases(t *testing.T) {
	cases := []string{
		"decode: unexpected end of JSON input",
		"bad json: invalid character",
		"no json object in reply: ...",
		"empty choices",
		"context deadline exceeded",
		"Post : net/http: timeout awaiting response headers",
		"read tcp: connection reset by peer",
		"http 429: rate limit hit",
		"http 502: bad gateway",
		"http 503: upstream unavailable",
		"http 504: gateway timeout",
		"unexpected EOF",
	}
	for _, e := range cases {
		if !isTransientJudgeErr(e) {
			t.Errorf("expected transient: %q", e)
		}
	}
}

func TestIsTransientJudgeErr_PermanentCases(t *testing.T) {
	// These must NOT trigger a retry. Auth failures, missing config,
	// and "no candidates" don't recover by waiting two seconds.
	cases := []string{
		"",
		"judge disabled or no candidates",
		"http 401: invalid api key",
		"http 403: forbidden",
		"http 404: model not found",
		"http 400: bad request: max_tokens out of range",
	}
	for _, e := range cases {
		if isTransientJudgeErr(e) {
			t.Errorf("expected non-transient: %q", e)
		}
	}
}
