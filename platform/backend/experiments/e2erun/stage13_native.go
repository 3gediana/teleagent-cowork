package main

// Stage 13 — End-to-end smoke test of the native agent runtime.
//
// This stage proves that the whole chain works in one process:
//
//   llm_endpoint row
//        ↓
//   llm.Registry (via loader.LoadEndpoint)
//        ↓
//   RoleOverride(audit_1, provider=<endpoint_id>, model=<model_id>)
//        ↓
//   agent.Session with Role=audit_1 + pending Change
//        ↓
//   runner.Dispatch (because provider starts with "llm_")
//        ↓
//   runner.Run turn-loop → scripted LLM → audit_output tool call
//        ↓
//   service.HandleToolCallResult → ProcessAuditOutput → Change.Status=merged
//
// We also record every StreamEmitter event so we can print the wire
// sequence the frontend would see (CHAT_UPDATE / TOOL_CALL / AGENT_DONE).
//
// What this proves (that the unit tests cannot):
//   * Main-process wiring (StreamEmitter + PlatformToolSink + NativeRegistryBuilder) is correct.
//   * model.LLMEndpoint → llm.Entry loader handles a real INSERT.
//   * RoleOverride persistence + GetRoleConfigWithOverride actually
//     routes to native when the ModelProvider starts with "llm_".
//   * service.HandleToolCallResult invokes ProcessAuditOutput on a
//     real Change row and flips its status.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/llm"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/runner"
	"github.com/a3c/platform/internal/service"
	"gorm.io/gorm"
)

// stage13Native runs the full native-runtime smoke test. Returns no
// error — panics on setup failures so e2erun's "done" line is
// meaningful.
func stage13Native(db *gorm.DB, projectID, humanID string) {
	// 0. Make sure the LLM-endpoint table exists. Main's AutoMigrate
	//    list doesn't include it (this table is a Phase 1 addition)
	//    so we add it here — additive migration is safe on SQLite.
	if err := db.AutoMigrate(&model.LLMEndpoint{}); err != nil {
		log.Fatalf("stage13: migrate llm_endpoint: %v", err)
	}

	// 1. Spin up a scripted OpenAI-compatible endpoint. The server
	//    returns two different scripts on successive calls: first an
	//    audit_output tool call, then a plain text reply to close the
	//    loop. Captures each request body so we can verify the runner
	//    sent the right thing.
	var capture struct {
		sync.Mutex
		requests []string
	}
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.Lock()
		capture.requests = append(capture.requests, string(body))
		capture.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		script := script1AuditToolCall
		if call > 0 {
			script = script2TextReply
		}
		call++
		for _, frame := range script {
			_, _ = w.Write([]byte(frame))
		}
	}))
	defer srv.Close()
	fmt.Printf("  mock LLM server listening at %s\n", srv.URL)

	// 2. Persist an LLMEndpoint row pointing at the mock. Use the
	//    real ID helper so the prefix is "llm_*" — that's what
	//    Dispatch.routesToNative keys off.
	endpointID := model.GenerateID("llm")
	modelID := "gpt-4o-mini-e2e"
	endpoint := model.LLMEndpoint{
		ID:           endpointID,
		Name:         "e2e mock",
		Format:       "openai",
		BaseURL:      srv.URL,
		APIKey:       "test-key",
		Models:       fmt.Sprintf(`[{"id":%q}]`, modelID),
		DefaultModel: modelID,
		Status:       "active",
		CreatedBy:    humanID,
	}
	if err := db.Create(&endpoint).Error; err != nil {
		log.Fatalf("stage13: insert llm_endpoint: %v", err)
	}
	if err := llm.LoadEndpoint(endpointID); err != nil {
		log.Fatalf("stage13: load endpoint into registry: %v", err)
	}
	fmt.Printf("  endpoint registered: id=%s format=openai model=%s\n", endpointID, modelID)

	// 3. Route the audit_1 role to our new endpoint. On a running
	//    server this is what the "Change model" button on the
	//    SettingsPage writes; we do it directly here.
	if err := agent.SetRoleOverride(agent.RoleAudit1, endpointID, modelID); err != nil {
		log.Fatalf("stage13: set role override: %v", err)
	}
	cfg := agent.GetRoleConfigWithOverride(agent.RoleAudit1)
	fmt.Printf("  role override: audit_1 → provider=%s model=%s\n", cfg.ModelProvider, cfg.ModelID)

	// 4. Install the production runner wiring, same as cmd/server/main.go.
	//    Overriding the prompt builder avoids the template-path
	//    dependency (this e2e doesn't care what the prompt says —
	//    the mock server ignores it anyway).
	runner.NativeRegistryBuilder = runner.PlatformRegistryBuilder
	runner.PlatformToolSink = service.HandleToolCallResult
	runner.NativePromptBuilder = func(sess *agent.Session) (string, string, error) {
		return "You are a code auditor.", "Audit the submitted change.", nil
	}
	rec := &stageEventRecorder{}
	runner.StreamEmitter = rec.record
	defer func() {
		// Reset to defaults so subsequent stages (if any) see a clean slate.
		runner.StreamEmitter = nil
	}()

	// 5. Create a pending Change row the audit will mutate. We don't
	//    exercise the full submit pipeline here — we just need a row
	//    for ProcessAuditOutput to find.
	changeID := model.GenerateID("change")
	if err := db.Create(&model.Change{
		ID:        changeID,
		ProjectID: projectID,
		AgentID:   humanID,
		Status:    "pending",
		CreatedAt: time.Now(),
	}).Error; err != nil {
		log.Fatalf("stage13: insert change: %v", err)
	}

	// 6. Create the AgentSession row + in-memory Session object.
	//    Mirror what agent.DispatchSession does in production.
	sessionID := model.GenerateID("session")
	if err := db.Create(&model.AgentSession{
		ID:            sessionID,
		Role:          string(agent.RoleAudit1),
		ProjectID:     projectID,
		ChangeID:      changeID,
		Status:        "pending",
		TriggerReason: "e2e-smoke",
		CreatedAt:     time.Now(),
	}).Error; err != nil {
		log.Fatalf("stage13: insert session: %v", err)
	}
	sess := &agent.Session{
		ID:        sessionID,
		Role:      agent.RoleAudit1,
		ProjectID: projectID,
		ChangeID:  changeID,
		Status:    "pending",
		Context: &agent.SessionContext{
			ProjectPath:   "/tmp/e2e-project",
			InputContent:  "Audit the change",
			TriggerReason: "e2e-smoke",
			ChangeInfo: &agent.ChangeContext{
				ChangeID: changeID,
				TaskName: "Fix login bug",
				TaskDesc: "Return 200 on valid credentials",
			},
		},
	}

	// 7. Drive the dispatcher. This is the exact call
	//    agent.DispatchSession makes in production (minus the
	//    goroutine wrapper).
	fmt.Println("  dispatching → runner.Dispatch …")
	if err := runner.Dispatch(sess); err != nil {
		log.Fatalf("stage13: dispatch: %v", err)
	}

	// 8. Verify outcomes.
	fmt.Println("  ---- results ----")
	fmt.Printf("  session.Status      = %s\n", sess.Status)

	var postChange model.Change
	db.Where("id = ?", changeID).First(&postChange)
	auditLvl := "(nil)"
	if postChange.AuditLevel != nil {
		auditLvl = *postChange.AuditLevel
	}
	fmt.Printf("  change.Status       = %s\n", postChange.Status)
	fmt.Printf("  change.AuditLevel   = %s\n", auditLvl)

	var traces []model.ToolCallTrace
	db.Where("session_id = ?", sessionID).Find(&traces)
	fmt.Printf("  tool_call_traces    = %d row(s)\n", len(traces))
	for _, tr := range traces {
		fmt.Printf("    - %s success=%v args=%s\n", tr.ToolName, tr.Success, truncate(tr.Args, 80))
	}

	rec.dump()

	// Final green light for the stage.
	ok := sess.Status == "completed" &&
		postChange.Status == "merged" &&
		auditLvl == "L0" &&
		len(traces) >= 1
	if ok {
		fmt.Println("  ✔ native runtime end-to-end smoke PASSED")
	} else {
		fmt.Println("  ✗ native runtime end-to-end smoke FAILED — inspect state above")
	}
}

// ---- scripted SSE frames ------------------------------------------------

// script1AuditToolCall is the first LLM response: the model emits an
// audit_output tool call with L0 verdict, then finishes.
var script1AuditToolCall = []string{
	`data: {"choices":[{"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_audit_1","type":"function","function":{"name":"audit_output","arguments":""}}]},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"level\":\"L0\""}}]},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}` + "\n\n",
	`data: {"usage":{"prompt_tokens":120,"completion_tokens":15,"total_tokens":135},"choices":[]}` + "\n\n",
	`data: [DONE]` + "\n\n",
}

// script2TextReply is the second LLM response: after seeing the
// tool_result, the model confirms completion in plain text.
var script2TextReply = []string{
	`data: {"choices":[{"delta":{"role":"assistant","content":"Audit complete."},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}` + "\n\n",
	`data: {"usage":{"prompt_tokens":160,"completion_tokens":3,"total_tokens":163},"choices":[]}` + "\n\n",
	`data: [DONE]` + "\n\n",
}

// ---- event recorder ----------------------------------------------------

type stageEventRecorder struct {
	mu     sync.Mutex
	events []recordedStageEvent
}

type recordedStageEvent struct {
	Type    string
	Payload map[string]interface{}
}

func (r *stageEventRecorder) record(_, eventType string, payload map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedStageEvent{eventType, payload})
}

func (r *stageEventRecorder) dump() {
	r.mu.Lock()
	defer r.mu.Unlock()

	counts := map[string]int{}
	for _, ev := range r.events {
		counts[ev.Type]++
	}
	// Stable print order.
	types := make([]string, 0, len(counts))
	for k := range counts {
		types = append(types, k)
	}
	sort.Strings(types)
	fmt.Println("  SSE event counts:")
	for _, k := range types {
		fmt.Printf("    %-24s %d\n", k, counts[k])
	}
	// Print the first TOOL_CALL payload fully so the reader can see
	// the wire shape matches what opencode emits.
	for _, ev := range r.events {
		if ev.Type == runner.EventToolCall {
			b, _ := json.MarshalIndent(ev.Payload, "      ", "  ")
			fmt.Printf("  first TOOL_CALL payload:\n      %s\n", string(b))
			break
		}
	}
}
