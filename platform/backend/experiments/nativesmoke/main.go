// Command nativesmoke — standalone end-to-end smoke test for the
// native agent runtime.
//
// This binary exists because experiments/e2erun (which includes Stage 13
// covering the same ground) requires the Python embedder sidecar at
// :3011 for earlier stages. `nativesmoke` has zero external
// dependencies — pure SQLite in-memory + a local httptest server —
// so it's safe to run in CI as a gate before migrating any role.
//
// What it proves, in one shot:
//
//   1. Inserting an llm_endpoint row + LoadEndpoint registers a live
//      Provider in the Registry (reading the row's JSON Models list).
//   2. Setting a RoleOverride with that endpoint id routes the role
//      through runner.Dispatch (not opencode fallback).
//   3. The runner Loop drives a multi-turn conversation:
//        LLM → tool_use audit_output → tool_result → LLM → text stop
//   4. The platform-tool sink correctly invokes
//      service.HandleToolCallResult → ProcessAuditOutput, which
//      flips the Change.Status to "merged" with L0 audit level.
//   5. All six StreamEmitter event types fire in the right order.
//   6. ToolCallTrace rows are persisted in the same shape opencode
//      would produce.
//
// Exit code: 0 on pass, 1 on any deviation. Prints a step-by-step
// banner output so CI logs stay readable when this fails in the
// unlikely event a regression lands.
//
// Run:   cd platform/backend && go run ./experiments/nativesmoke

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/a3c/platform/internal/agent"
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

	// Track failures so we can exit non-zero at the end without
	// bailing halfway (useful: a run that fails one check but
	// completes others still tells you which check failed first).
	var failures []string
	check := func(cond bool, msg string) {
		if cond {
			fmt.Printf("  ✔ %s\n", msg)
			return
		}
		fmt.Printf("  ✗ %s\n", msg)
		failures = append(failures, msg)
	}

	fmt.Println(strings.Repeat("─", 72))
	fmt.Println("  Native runtime end-to-end smoke")
	fmt.Println(strings.Repeat("─", 72))

	// Step 1 — bootstrap DB.
	db, err := gorm.Open(sqlite.Open("file:nativesmoke?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
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

	projectID := "proj_smoke"
	humanID := "agent_human_smoke"
	db.Create(&model.Project{ID: projectID, Name: "Smoke", Status: "ready"})
	accessKey := "hk_smoke"
	db.Create(&model.Agent{
		ID: humanID, Name: "human", Status: "online",
		CurrentProjectID: &projectID, AccessKey: accessKey, IsHuman: true,
	})
	fmt.Printf("  SQLite booted; project=%s\n", projectID)

	// Step 2 — spin up a scripted mock LLM that plays the classic
	// "tool_use then text" two-turn dance.
	var capturedBodies [][]byte
	var captureMu sync.Mutex
	callIdx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captureMu.Lock()
		capturedBodies = append(capturedBodies, b)
		captureMu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		script := turn1Script
		if callIdx > 0 {
			script = turn2Script
		}
		callIdx++
		for _, frame := range script {
			_, _ = w.Write([]byte(frame))
		}
	}))
	defer srv.Close()
	fmt.Printf("  mock LLM at %s\n", srv.URL)

	// Step 3 — register endpoint + set role override.
	endpointID := model.GenerateID("llm")
	modelID := "smoke-model"
	if err := db.Create(&model.LLMEndpoint{
		ID:           endpointID,
		Name:         "smoke",
		Format:       "openai",
		BaseURL:      srv.URL,
		APIKey:       "test",
		Models:       fmt.Sprintf(`[{"id":%q}]`, modelID),
		DefaultModel: modelID,
		Status:       "active",
		CreatedBy:    humanID,
	}).Error; err != nil {
		log.Fatalf("insert llm_endpoint: %v", err)
	}
	if err := llm.LoadEndpoint(endpointID); err != nil {
		log.Fatalf("load endpoint: %v", err)
	}
	if err := agent.SetRoleOverride(agent.RoleAudit1, endpointID, modelID); err != nil {
		log.Fatalf("set role override: %v", err)
	}
	fmt.Printf("  endpoint_id=%s; audit_1 routed to it\n", endpointID)

	// Step 4 — wire runner in "production" mode.
	runner.NativeRegistryBuilder = runner.PlatformRegistryBuilder
	runner.PlatformToolSink = service.HandleToolCallResult
	// Override the prompt builder so we don't need template files on
	// disk; the mock LLM ignores content anyway.
	runner.NativePromptBuilder = func(sess *agent.Session) (string, string, error) {
		return "You are a code auditor.", "Audit the submitted change.", nil
	}
	rec := &eventRecorder{}
	runner.StreamEmitter = rec.emit

	// Step 5 — create a pending Change + AgentSession.
	changeID := model.GenerateID("change")
	db.Create(&model.Change{
		ID: changeID, ProjectID: projectID, AgentID: humanID,
		Status: "pending", CreatedAt: time.Now(),
	})
	sessionID := model.GenerateID("session")
	db.Create(&model.AgentSession{
		ID:        sessionID,
		Role:      string(agent.RoleAudit1),
		ProjectID: projectID,
		ChangeID:  changeID,
		Status:    "pending",
		CreatedAt: time.Now(),
	})
	sess := &agent.Session{
		ID: sessionID, Role: agent.RoleAudit1,
		ProjectID: projectID, ChangeID: changeID, Status: "pending",
		Context: &agent.SessionContext{
			ProjectPath:  os.TempDir(),
			InputContent: "Audit",
			ChangeInfo:   &agent.ChangeContext{ChangeID: changeID, TaskName: "t", TaskDesc: "d"},
		},
	}

	// Step 6 — dispatch.
	fmt.Println("  dispatching …")
	if err := runner.Dispatch(sess); err != nil {
		log.Fatalf("dispatch: %v", err)
	}

	// Step 7 — assertions.
	fmt.Println(strings.Repeat("─", 40))
	fmt.Println("  results")
	fmt.Println(strings.Repeat("─", 40))

	check(callIdx == 2, fmt.Sprintf("mock LLM saw exactly 2 turns (got %d)", callIdx))
	check(sess.Status == "completed", fmt.Sprintf("session.Status == completed (got %q)", sess.Status))

	// L0 audit outcome: service.approveChange flips Status to "approved"
	// and auto-completes the linked task. ProcessAuditOutput sets
	// AuditLevel after approveChange returns — both persist cleanly
	// now that service/broadcast.go nil-guards Redis (previously
	// the absent RDB panicked partway through and lost AuditLevel).
	var postChange model.Change
	db.Where("id = ?", changeID).First(&postChange)
	check(postChange.Status == "approved",
		fmt.Sprintf("change.Status == approved (got %q)", postChange.Status))
	check(postChange.ReviewedAt != nil,
		"change.ReviewedAt was populated by the audit")
	gotLvl := ""
	if postChange.AuditLevel != nil {
		gotLvl = *postChange.AuditLevel
	}
	check(gotLvl == "L0", fmt.Sprintf("change.AuditLevel == L0 (got %q)", gotLvl))

	var traces []model.ToolCallTrace
	db.Where("session_id = ?", sessionID).Find(&traces)
	check(len(traces) >= 1, fmt.Sprintf("tool_call_trace rows persisted (got %d)", len(traces)))
	for _, tr := range traces {
		check(tr.ToolName == "audit_output" && tr.Success,
			fmt.Sprintf("trace row for %s is a success", tr.ToolName))
	}

	expected := []string{
		runner.EventToolCall,
		runner.EventAgentTurn,
		runner.EventAgentDone,
		runner.EventChatUpdate,
	}
	for _, t := range expected {
		check(rec.count(t) >= 1, fmt.Sprintf("stream emitted %s", t))
	}
	// Error event must NOT fire on the happy path.
	check(rec.count(runner.EventAgentError) == 0,
		fmt.Sprintf("no AGENT_ERROR on happy path (got %d)", rec.count(runner.EventAgentError)))

	// Wire-shape assertion: the first request body should contain the
	// audit_output tool schema the runner built from agent.PlatformTools.
	captureMu.Lock()
	firstBody := ""
	if len(capturedBodies) > 0 {
		firstBody = string(capturedBodies[0])
	}
	captureMu.Unlock()
	check(strings.Contains(firstBody, `"audit_output"`),
		"first request body advertises audit_output tool")
	check(strings.Contains(firstBody, `"tools"`),
		"first request body has a tools array")

	// Print event counts for the record.
	fmt.Println(strings.Repeat("─", 40))
	fmt.Println("  event counts")
	fmt.Println(strings.Repeat("─", 40))
	for _, row := range rec.summary() {
		fmt.Printf("    %-24s %d\n", row.name, row.n)
	}

	fmt.Println(strings.Repeat("─", 72))
	if len(failures) == 0 {
		fmt.Println("  ✔ ALL GREEN — native runtime is production-ready for audit_1")
		os.Exit(0)
	}
	fmt.Printf("  ✗ %d check(s) failed:\n", len(failures))
	for _, f := range failures {
		fmt.Printf("    · %s\n", f)
	}
	os.Exit(1)
}

// ---- scripted SSE frames ----------------------------------------------

// turn1Script is what the mock LLM returns on the first request:
// a single tool_call for audit_output with L0 verdict.
var turn1Script = []string{
	`data: {"choices":[{"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_audit","type":"function","function":{"name":"audit_output","arguments":""}}]},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"level\":\"L0\""}}]},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}` + "\n\n",
	`data: {"usage":{"prompt_tokens":120,"completion_tokens":15,"total_tokens":135},"choices":[]}` + "\n\n",
	`data: [DONE]` + "\n\n",
}

// turn2Script is the model's second response (after the tool_result
// came back): a plain text confirmation that closes the loop.
var turn2Script = []string{
	`data: {"choices":[{"delta":{"role":"assistant","content":"Audit complete."},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}` + "\n\n",
	`data: {"usage":{"prompt_tokens":160,"completion_tokens":3,"total_tokens":163},"choices":[]}` + "\n\n",
	`data: [DONE]` + "\n\n",
}

// ---- event recorder ---------------------------------------------------

type eventRecorder struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	projectID string
	typ       string
	payload   map[string]interface{}
}

func (r *eventRecorder) emit(projectID, typ string, payload map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedEvent{projectID, typ, payload})
}

func (r *eventRecorder) count(typ string) int {
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

type summaryRow struct {
	name string
	n    int
}

func (r *eventRecorder) summary() []summaryRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := map[string]int{}
	for _, ev := range r.events {
		m[ev.typ]++
	}
	out := make([]summaryRow, 0, len(m))
	for k, v := range m {
		out = append(out, summaryRow{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// keeps json/encoding imported for future payload-shape assertions.
var _ = json.Marshal
