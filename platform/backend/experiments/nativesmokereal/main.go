// Command nativesmokereal — end-to-end smoke test against a REAL LLM
// provider. Same harness as experiments/nativesmoke but instead of an
// httptest mock, it points the runner at the provider whose
// credentials live in configs/config.yaml under `llm.minimax`
// (default) or whichever provider is selected via --provider.
//
// This is the binary to run after filling in a real API key in
// configs/config.yaml, to prove the native runtime actually talks to
// the provider, reassembles the streaming tool-use call, and
// completes the audit pipeline end-to-end.
//
// Credentials come from config ONLY — never from flags, env, or
// source. The user spec:  "写config里头，别硬编码了".
//
// Usage:
//   go run ./experiments/nativesmokereal                  # default: minimax
//   go run ./experiments/nativesmokereal --provider deepseek
//   go run ./experiments/nativesmokereal --provider anthropic

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/llm"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/runner"
	"github.com/a3c/platform/internal/service"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	log.SetFlags(0)

	provider := flag.String("provider", "minimax",
		"which credential entry to use from config.yaml llm.* (minimax|openai|anthropic|deepseek)")
	model_ := flag.String("model", "",
		"override the config's default model id (optional)")
	verbose := flag.Bool("v", false, "verbose: print full provider response")
	flag.Parse()

	// ---- Load config --------------------------------------------------
	cfg := config.Load("")
	creds, format, defaultModel := pickCreds(&cfg.LLM, *provider)
	if creds.APIKey == "" {
		fmt.Fprintf(os.Stderr, "✗ llm.%s.api_key is empty in configs/config.yaml\n", *provider)
		fmt.Fprintf(os.Stderr, "  Paste your key into the file and re-run.\n")
		os.Exit(2)
	}
	if *model_ != "" {
		defaultModel = *model_
	}
	if defaultModel == "" {
		fmt.Fprintf(os.Stderr, "✗ llm.%s.model is empty and --model not provided\n", *provider)
		os.Exit(2)
	}

	// Banner WITHOUT the key — we explicitly redact because logs from
	// this binary might end up in CI output. Showing only the tail 4
	// chars is enough to confirm "right key" without disclosure.
	fmt.Println(strings.Repeat("─", 72))
	fmt.Printf("  Native runtime REAL end-to-end smoke — provider=%s format=%s\n", *provider, format)
	fmt.Printf("  model=%s  base_url=%s  key=%s\n",
		defaultModel, creds.BaseURL, redactKey(creds.APIKey))
	fmt.Println(strings.Repeat("─", 72))

	// ---- Bootstrap in-memory DB --------------------------------------
	db, err := gorm.Open(sqlite.Open("file:nativesmokereal?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Project{}, &model.Agent{}, &model.Task{},
		&model.AgentSession{}, &model.ToolCallTrace{},
		&model.Change{}, &model.RoleOverride{},
		&model.LLMEndpoint{},
	); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	model.DB = db

	projectID := "proj_real_smoke"
	humanID := "agent_human_real"
	db.Create(&model.Project{ID: projectID, Name: "Real", Status: "ready"})
	accessKey := "hk_real"
	db.Create(&model.Agent{
		ID: humanID, Name: "human", Status: "online",
		CurrentProjectID: &projectID, AccessKey: accessKey, IsHuman: true,
	})

	// ---- Register endpoint with real creds ---------------------------
	endpointID := model.GenerateID("llm")
	if err := db.Create(&model.LLMEndpoint{
		ID:           endpointID,
		Name:         "real-" + *provider,
		Format:       format,
		BaseURL:      creds.BaseURL,
		APIKey:       creds.APIKey,
		Models:       fmt.Sprintf(`[{"id":%q}]`, defaultModel),
		DefaultModel: defaultModel,
		Status:       "active",
		CreatedBy:    humanID,
	}).Error; err != nil {
		log.Fatalf("insert llm_endpoint: %v", err)
	}
	if err := llm.LoadEndpoint(endpointID); err != nil {
		log.Fatalf("load endpoint: %v", err)
	}
	if err := agent.SetRoleOverride(agent.RoleAudit1, endpointID, defaultModel); err != nil {
		log.Fatalf("role override: %v", err)
	}
	fmt.Printf("  endpoint_id=%s registered & loaded\n", endpointID)

	// ---- Wire the runner ---------------------------------------------
	runner.NativeRegistryBuilder = runner.PlatformRegistryBuilder
	runner.PlatformToolSink = service.HandleToolCallResult
	runner.NativePromptBuilder = func(sess *agent.Session) (string, string, error) {
		return realAuditSystemPrompt, realAuditUserInput, nil
	}
	rec := &realRecorder{}
	runner.StreamEmitter = rec.emit

	// ---- Pending Change + Session ------------------------------------
	changeID := model.GenerateID("change")
	db.Create(&model.Change{
		ID: changeID, ProjectID: projectID, AgentID: humanID,
		Status: "pending", CreatedAt: time.Now(),
	})
	sessionID := model.GenerateID("session")
	db.Create(&model.AgentSession{
		ID: sessionID, Role: string(agent.RoleAudit1),
		ProjectID: projectID, ChangeID: changeID,
		Status: "pending", CreatedAt: time.Now(),
	})
	sess := &agent.Session{
		ID: sessionID, Role: agent.RoleAudit1,
		ProjectID: projectID, ChangeID: changeID, Status: "pending",
		Context: &agent.SessionContext{
			ProjectPath:  os.TempDir(),
			InputContent: realAuditUserInput,
			ChangeInfo:   &agent.ChangeContext{ChangeID: changeID, TaskName: "smoke", TaskDesc: "canary"},
		},
	}

	// ---- Dispatch through the native runner --------------------------
	fmt.Println("  dispatching …  (real LLM call — this takes 2-20s)")
	started := time.Now()
	if err := runner.Dispatch(sess); err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ dispatch failed: %v\n", err)
		os.Exit(1)
	}
	elapsed := time.Since(started)

	// ---- Assertions --------------------------------------------------
	fmt.Println(strings.Repeat("─", 40))
	fmt.Println("  results")
	fmt.Println(strings.Repeat("─", 40))

	var fail []string
	ok := func(cond bool, msg string) {
		if cond {
			fmt.Printf("  ✔ %s\n", msg)
			return
		}
		fmt.Printf("  ✗ %s\n", msg)
		fail = append(fail, msg)
	}

	ok(sess.Status == "completed", fmt.Sprintf("session.Status == completed (got %q)", sess.Status))

	var postChange model.Change
	db.Where("id = ?", changeID).First(&postChange)
	// Accept any final status; what matters is that the tool_use
	// landed and the runtime completed without error. Real models
	// may legitimately return L1 or L2 even on our trivial prompt.
	ok(postChange.ReviewedAt != nil, "change.ReviewedAt populated (tool call reached the service sink)")

	auditLvl := "(none)"
	if postChange.AuditLevel != nil {
		auditLvl = *postChange.AuditLevel
	}
	ok(postChange.AuditLevel != nil, fmt.Sprintf("change.AuditLevel was set (got %s)", auditLvl))

	var traces []model.ToolCallTrace
	db.Where("session_id = ?", sessionID).Find(&traces)
	ok(len(traces) >= 1, fmt.Sprintf("tool_call_trace rows persisted (got %d)", len(traces)))
	for _, tr := range traces {
		ok(tr.ToolName == "audit_output",
			fmt.Sprintf("tool call was audit_output (got %q) — args: %s",
				tr.ToolName, shortenForLog(tr.Args, 200)))
	}

	// Stream events
	for _, evtype := range []string{runner.EventToolCall, runner.EventAgentTurn,
		runner.EventAgentDone, runner.EventChatUpdate} {
		ok(rec.count(evtype) >= 1, fmt.Sprintf("stream emitted %s", evtype))
	}
	ok(rec.count(runner.EventAgentError) == 0,
		fmt.Sprintf("no AGENT_ERROR on happy path (got %d)", rec.count(runner.EventAgentError)))

	// ---- Summary -----------------------------------------------------
	fmt.Println(strings.Repeat("─", 40))
	fmt.Printf("  model answered with: level=%s\n", auditLvl)
	fmt.Printf("  real-call duration: %s\n", elapsed)
	fmt.Printf("  session.Output:\n%s\n", indent(sess.Output, 4))
	if *verbose {
		fmt.Println("  full journal:")
		for _, tr := range traces {
			fmt.Printf("    tool=%s success=%v\n      args=%s\n      result=%s\n",
				tr.ToolName, tr.Success,
				shortenForLog(tr.Args, 500),
				shortenForLog(tr.ResultSummary, 500))
		}
	}

	fmt.Println(strings.Repeat("─", 72))
	if len(fail) == 0 {
		fmt.Printf("  ✔ REAL provider smoke PASSED (%s / %s)\n", *provider, defaultModel)
		os.Exit(0)
	}
	fmt.Printf("  ✗ %d check(s) failed on real smoke:\n", len(fail))
	for _, f := range fail {
		fmt.Printf("    · %s\n", f)
	}
	os.Exit(1)
}

// ---- helpers ---------------------------------------------------------

// pickCreds returns (creds, format, default_model) for the selected
// provider name. format is the llm.ProviderID — ProviderAnthropic for
// "anthropic", ProviderOpenAI for everything else. Panics on unknown
// provider so typos surface immediately.
func pickCreds(cfg *config.LLMConfig, name string) (config.ProviderCreds, string, string) {
	switch strings.ToLower(name) {
	case "minimax":
		return cfg.MiniMax, "openai", cfg.MiniMax.Model
	case "deepseek":
		return cfg.DeepSeek, "openai", cfg.DeepSeek.Model
	case "openai":
		return cfg.OpenAI, "openai", cfg.OpenAI.Model
	case "anthropic":
		return cfg.Anthropic, "anthropic", cfg.Anthropic.Model
	default:
		log.Fatalf("unknown provider %q — expected minimax|deepseek|openai|anthropic", name)
		panic("unreachable")
	}
}

func redactKey(k string) string {
	if len(k) < 8 {
		return strings.Repeat("*", len(k))
	}
	return "…" + k[len(k)-4:] + fmt.Sprintf(" (%d chars)", len(k))
}

func shortenForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

// ---- prompts --------------------------------------------------------

// realAuditSystemPrompt is a short audit-1 role brief. We don't load
// the production template here — we want a small, fast prompt so the
// real-call duration stays predictable in smoke runs.
const realAuditSystemPrompt = `You are the audit_1 agent for a code review platform.

Your job: read the change description, decide if it looks safe, and
emit EXACTLY ONE call to the audit_output tool with the verdict.
Do not reply in plain text — the platform only reads tool calls.

For this canary run, assume the change is trivially safe and respond
with level="L0". This test is checking that the native runtime wires
you up correctly — your verdict content is not being judged.`

const realAuditUserInput = `Change under review:

  Task: Canary audit — verify the native runtime can talk to the LLM
        provider and reassemble a tool_use call end-to-end.

  Description: A trivial one-line change (logging statement). Nothing
               risky. Please emit audit_output with level="L0".`

// ---- event recorder --------------------------------------------------

type realRecorder struct {
	mu     sync.Mutex
	events []realEvent
}

type realEvent struct {
	typ     string
	payload map[string]interface{}
}

func (r *realRecorder) emit(_, typ string, payload map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, realEvent{typ, payload})
}

func (r *realRecorder) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.events {
		if ev.typ == typ {
			n++
		}
	}
	return n
}
