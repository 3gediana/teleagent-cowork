package agentpool

// End-to-end smoke that actually starts `opencode serve` in a prepared
// workDir and asks MiniMax-M2.7 for a trivial reply via prompt_async.
// This is the final gate that catches the zod v4 regression — unit
// tests can pass while the real binary still crashes.
//
// Gated behind A3C_RUN_E2E_TESTS=1 because it needs:
//
//   - `opencode` on PATH (or ${A3C_OPENCODE_CMD} pointing at it)
//   - Network access to api.minimaxi.com
//   - A valid MiniMax API key exposed via either the global
//     ~/.config/opencode/opencode.json OR the MINIMAX_API_KEY env.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2E_PoolAgentPromptAsyncReturnsText(t *testing.T) {
	if os.Getenv("A3C_RUN_E2E_TESTS") != "1" {
		t.Skip("Set A3C_RUN_E2E_TESTS=1 to run the opencode serve e2e.")
	}
	prepareMu.Lock()
	templateReady = false
	prepareMu.Unlock()

	poolRoot := t.TempDir()
	workDir := filepath.Join(poolRoot, "pool_e2e")

	if err := prepareOpencodeDir(workDir, poolRoot); err != nil {
		t.Fatalf("prepareOpencodeDir: %v", err)
	}

	port, err := pickFreePort()
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}

	opencodeBin := os.Getenv("A3C_OPENCODE_CMD")
	if opencodeBin == "" {
		opencodeBin = "opencode"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, opencodeBin, "serve", "--port", fmt.Sprintf("%d", port))
	cmd.Dir = workDir
	cmd.Stdout = &testLogWriter{t: t, prefix: "[opencode stdout]"}
	cmd.Stderr = &testLogWriter{t: t, prefix: "[opencode stderr]"}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start opencode: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	// Wait for /global/health.
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/global/health", port)
	if !waitHealthy(ctx, healthURL, 20*time.Second) {
		t.Fatalf("opencode serve never went healthy on port %d", port)
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	sessID, err := createSession(base, "e2e test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Logf("session: %s", sessID)

	if err := promptAsync(base, sessID, "Say hello in one sentence.", "minimax-coding-plan", "MiniMax-M2.7"); err != nil {
		t.Fatalf("prompt_async: %v", err)
	}

	// Poll the messages endpoint until the assistant replies or we
	// time out. 60s is plenty for MiniMax's reasoning warmup.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		msgs, err := listMessages(base, sessID)
		if err != nil {
			t.Logf("list messages: %v", err)
			continue
		}
		for _, m := range msgs {
			if m.Info.Role == "assistant" && len(m.Parts) > 0 {
				for _, p := range m.Parts {
					if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
						t.Logf("assistant replied: %s", truncForLog(p.Text, 140))
						return // SUCCESS
					}
				}
			}
		}
	}
	t.Fatal("assistant never produced a non-empty text part (parts=0 regression)")
}

// ---- helpers (local, no extra deps) ---------------------------------

func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitHealthy(ctx context.Context, url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		if resp, err := client.Get(url); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func createSession(base, title string) (string, error) {
	body, _ := json.Marshal(map[string]string{"title": title})
	resp, err := http.Post(base+"/session", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func promptAsync(base, sessID, prompt, providerID, modelID string) error {
	body, _ := json.Marshal(map[string]any{
		"parts": []map[string]any{
			{"type": "text", "text": prompt},
		},
		"model": map[string]string{
			"providerID": providerID,
			"modelID":    modelID,
		},
	})
	resp, err := http.Post(
		fmt.Sprintf("%s/session/%s/prompt_async", base, sessID),
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, raw)
	}
	return nil
}

type e2eMessage struct {
	Info struct {
		Role    string `json:"role"`
		ModelID string `json:"modelID"`
	} `json:"info"`
	Parts []e2ePart `json:"parts"`
}

type e2ePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func listMessages(base, sessID string) ([]e2eMessage, error) {
	resp, err := http.Get(fmt.Sprintf("%s/session/%s/message?limit=5", base, sessID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, raw)
	}
	var out []e2eMessage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func truncForLog(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

type testLogWriter struct {
	t      *testing.T
	prefix string
}

func (w *testLogWriter) Write(b []byte) (int, error) {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	for _, l := range lines {
		if l == "" {
			continue
		}
		w.t.Logf("%s %s", w.prefix, l)
	}
	return len(b), nil
}
