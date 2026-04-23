package main

// LLM judge — a minimal OpenAI-compatible chat client used to grade
// evobench's retrieval choices. Runs non-streaming against whichever
// endpoint the user configures (MiniMax by default).
//
// Design choices:
//   - Stand-alone HTTP client (no streaming, no retry ladder) because
//     a judge call is one shot, bounded in size, and benchmark-time.
//   - Reads creds from configs/config.yaml via internal/config, so the
//     operator never has to paste keys into env vars.
//   - Graceful degradation: if the API errors or times out, the judge
//     records "skip" for that round and evobench continues with
//     auto-grading stats only. Never fatals the whole run.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/a3c/platform/internal/config"
)

// JudgeConfig is the minimum a judge call needs.
type JudgeConfig struct {
	Enabled  bool
	BaseURL  string
	APIKey   string
	Model    string
	Provider string
	Timeout  time.Duration
}

// LoadJudgeConfig reads credentials from configs/config.yaml for the
// named provider entry. Returns a disabled config if the key is empty
// — callers check Enabled before invoking JudgeRank.
func LoadJudgeConfig(provider string) JudgeConfig {
	cfg := config.Load("")
	var creds *config.ProviderCreds
	switch strings.ToLower(provider) {
	case "minimax", "":
		creds = &cfg.LLM.MiniMax
		provider = "minimax"
	case "openai":
		creds = &cfg.LLM.OpenAI
	case "deepseek":
		creds = &cfg.LLM.DeepSeek
	case "anthropic":
		creds = &cfg.LLM.Anthropic
	default:
		return JudgeConfig{}
	}
	if creds == nil || creds.APIKey == "" {
		return JudgeConfig{}
	}
	return JudgeConfig{
		Enabled:  true,
		BaseURL:  creds.BaseURL,
		APIKey:   creds.APIKey,
		Model:    creds.Model,
		Provider: provider,
		Timeout:  120 * time.Second,
	}
}

// JudgeCandidate is the minimal per-artifact info the judge needs to
// reason about relevance. We deliberately pass short strings, not the
// full payload, to keep prompts small and focused.
type JudgeCandidate struct {
	ID      string
	Kind    string
	Summary string
}

// JudgeResult is the parsed verdict for one judge call.
type JudgeResult struct {
	BestID   string   // the id the judge picked as most relevant
	Ranking  []string // full ranking if the judge returned one; empty otherwise
	Reason   string   // judge's short rationale (truncated)
	LatencyMs int64
	Skipped  bool    // true when the call errored or was disabled
	Err      string  // non-empty on skip
}

// JudgeRank asks the LLM to pick the most relevant candidate for the
// given query and returns its choice. Uses a strict JSON-only prompt
// so parsing is deterministic.
//
// The judge returns a ranked list of candidate IDs, but we only need
// the top-1 for agreement calculation; downstream callers can use the
// full ranking to compute NDCG / MRR if they care.
func JudgeRank(ctx context.Context, cfg JudgeConfig, queryDesc string, candidates []JudgeCandidate) JudgeResult {
	if !cfg.Enabled || len(candidates) == 0 {
		return JudgeResult{Skipped: true, Err: "judge disabled or no candidates"}
	}
	prompt := buildJudgePrompt(queryDesc, candidates)

	body, err := json.Marshal(map[string]any{
		"model":       cfg.Model,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
		// Reasoning models (MiniMax-M2.7, DeepSeek-R1) burn tokens in
		// <think> before emitting JSON. Budget generously; the judge
		// call is called a bounded number of rounds per bench run so
		// runaway cost is capped by flagJudgeRounds, not by a per-call
		// max_tokens starvation.
		"max_tokens":  4096,
		"temperature": 0.0,
		"stream":      false,
	})
	if err != nil {
		return JudgeResult{Skipped: true, Err: err.Error()}
	}

	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	reqCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return JudgeResult{Skipped: true, Err: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	start := time.Now()
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return JudgeResult{Skipped: true, Err: err.Error(),
			LatencyMs: time.Since(start).Milliseconds()}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		return JudgeResult{Skipped: true, Err: err.Error(), LatencyMs: latencyMs}
	}
	if resp.StatusCode != 200 {
		return JudgeResult{Skipped: true, LatencyMs: latencyMs,
			Err: fmt.Sprintf("http %d: %s", resp.StatusCode, truncate(string(raw), 200))}
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return JudgeResult{Skipped: true, LatencyMs: latencyMs, Err: "decode: " + err.Error()}
	}
	if len(parsed.Choices) == 0 {
		return JudgeResult{Skipped: true, LatencyMs: latencyMs, Err: "empty choices"}
	}
	content := parsed.Choices[0].Message.Content
	res := parseJudgeOutput(content)
	res.LatencyMs = latencyMs
	return res
}

// buildJudgePrompt renders the instruction + candidate list as a
// single user message. Using JSON-only output makes parsing robust
// against model verbosity.
func buildJudgePrompt(queryDesc string, cands []JudgeCandidate) string {
	var sb strings.Builder
	sb.WriteString("You are a retrieval quality judge. Given a task description and a list of candidate knowledge artifacts, return the single JSON object `{\"best\":\"<id>\",\"ranking\":[\"<id1>\",\"<id2>\",...],\"why\":\"<short reason>\"}`. Rank by how helpful each artifact would be for solving the task. Output ONLY the JSON object, no prose.\n\n")
	sb.WriteString("TASK: ")
	sb.WriteString(queryDesc)
	sb.WriteString("\n\nCANDIDATES:\n")
	for _, c := range cands {
		fmt.Fprintf(&sb, "  - id=%s kind=%s summary=%s\n", c.ID, c.Kind, c.Summary)
	}
	sb.WriteString("\nRemember: ONLY the JSON object.")
	return sb.String()
}

// parseJudgeOutput extracts best-id / ranking / reason from model
// text. Reasoning models (MiniMax-M2.7, DeepSeek-R1) wrap their
// chain-of-thought in <think>...</think> and emit the real answer
// after. We strip those before JSON extraction; otherwise the `{`
// character inside the thinking block would trip up our balancer.
func parseJudgeOutput(content string) JudgeResult {
	content = stripThinkBlocks(content)

	// Prefer fenced code blocks when present.
	jsonStr := extractJSONObject(content)
	if jsonStr == "" {
		return JudgeResult{Skipped: true, Err: "no json object in reply: " + truncate(content, 200)}
	}

	var obj struct {
		Best    string   `json:"best"`
		Ranking []string `json:"ranking"`
		Why     string   `json:"why"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
		return JudgeResult{Skipped: true, Err: "bad json: " + err.Error() + " raw=" + truncate(jsonStr, 120)}
	}
	return JudgeResult{
		BestID:  obj.Best,
		Ranking: obj.Ranking,
		Reason:  truncate(obj.Why, 120),
	}
}

// stripThinkBlocks removes <think>...</think> (and XML-escaped
// variants) from a response. Reasoning providers emit these before
// the final answer; we only care about what comes after.
func stripThinkBlocks(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "</think>")
		if end < 0 {
			// unterminated — drop everything from the tag onward
			return s[:start]
		}
		s = s[:start] + s[start+end+len("</think>"):]
	}
	return s
}

// extractJSONObject finds the first balanced {..} block in s. Uses a
// brace depth counter (not just first-/last-index) so embedded
// strings or nested objects in the `why` field don't confuse it.
func extractJSONObject(s string) string {
	// Prefer fenced code block if present
	if i := strings.Index(s, "```json"); i >= 0 {
		rest := s[i+len("```json"):]
		if j := strings.Index(rest, "```"); j >= 0 {
			candidate := strings.TrimSpace(rest[:j])
			if strings.HasPrefix(candidate, "{") {
				return candidate
			}
		}
	}
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+len("```"):]
		if j := strings.Index(rest, "```"); j >= 0 {
			candidate := strings.TrimSpace(rest[:j])
			if strings.HasPrefix(candidate, "{") {
				return candidate
			}
		}
	}
	// Fallback: balanced scan
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
