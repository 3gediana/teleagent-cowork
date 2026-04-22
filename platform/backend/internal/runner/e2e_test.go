package runner

// End-to-end smoke test — same shape as cmd/nativesmoke but as a
// `go test` so CI and `go test ./...` catch regressions.
//
// The standalone binary at cmd/nativesmoke exists for operators to
// run against a real staging DB + Redis + endpoint; this test covers
// the same wire path using SQLite-in-memory + a mock HTTP server.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/llm"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestEndToEnd_AuditOneCompletesNativeRuntime drives the full
// dispatcher → LLM → tool_use → service sink → DB chain against a
// scripted mock provider. Regressions in any link (loader, registry,
// dispatcher routing, platform-tool adapter, stream emission,
// tool-trace persistence) fail this test.
func TestEndToEnd_AuditOneCompletesNativeRuntime(t *testing.T) {
	// --- SQLite + schema ------------------------------------------
	db, err := gorm.Open(sqlite.Open("file:e2erun_audit?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Project{}, &model.Agent{}, &model.Task{},
		&model.AgentSession{}, &model.ToolCallTrace{},
		&model.Change{}, &model.RoleOverride{},
		&model.LLMEndpoint{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prevDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = prevDB })

	projectID := "proj_e2e_audit"
	humanID := "agent_human_e2e"
	db.Create(&model.Project{ID: projectID, Name: "E2E", Status: "ready"})
	db.Create(&model.Agent{
		ID: humanID, Name: "h", Status: "online",
		CurrentProjectID: &projectID, AccessKey: "hk_e2e", IsHuman: true,
	})

	// --- Mock LLM -------------------------------------------------
	var bodies [][]byte
	var bodiesMu sync.Mutex
	callIdx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodiesMu.Lock()
		bodies = append(bodies, b)
		bodiesMu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		script := e2eTurn1
		if callIdx > 0 {
			script = e2eTurn2
		}
		callIdx++
		for _, frame := range script {
			_, _ = w.Write([]byte(frame))
		}
	}))
	defer srv.Close()

	// --- Endpoint + override --------------------------------------
	endpointID := model.GenerateID("llm")
	modelID := "e2e-model"
	if err := db.Create(&model.LLMEndpoint{
		ID:           endpointID,
		Name:         "e2e",
		Format:       "openai",
		BaseURL:      srv.URL,
		APIKey:       "k",
		Models:       fmt.Sprintf(`[{"id":%q}]`, modelID),
		DefaultModel: modelID,
		Status:       "active",
		CreatedBy:    humanID,
	}).Error; err != nil {
		t.Fatalf("endpoint insert: %v", err)
	}
	if err := llm.LoadEndpoint(endpointID); err != nil {
		t.Fatalf("load endpoint: %v", err)
	}
	t.Cleanup(func() { llm.RemoveEndpoint(endpointID) })
	if err := agent.SetRoleOverride(agent.RoleAudit1, endpointID, modelID); err != nil {
		t.Fatalf("role override: %v", err)
	}

	// --- Production wiring ----------------------------------------
	prevBuilder := NativeRegistryBuilder
	prevSink := PlatformToolSink
	prevPrompt := NativePromptBuilder
	prevEmitter := StreamEmitter
	NativeRegistryBuilder = PlatformRegistryBuilder
	PlatformToolSink = service.HandleToolCallResult
	NativePromptBuilder = func(sess *agent.Session) (string, string, error) {
		return "audit prompt", "audit input", nil
	}
	rec := &e2eRecorder{}
	StreamEmitter = rec.emit
	t.Cleanup(func() {
		NativeRegistryBuilder = prevBuilder
		PlatformToolSink = prevSink
		NativePromptBuilder = prevPrompt
		StreamEmitter = prevEmitter
	})

	// --- Pending Change + Session --------------------------------
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
			InputContent: "audit",
			ChangeInfo:   &agent.ChangeContext{ChangeID: changeID},
		},
	}

	// --- Dispatch -------------------------------------------------
	if err := Dispatch(sess); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// --- Assertions ----------------------------------------------
	if callIdx != 2 {
		t.Errorf("want 2 LLM calls (tool_use + follow-up text); got %d", callIdx)
	}
	if sess.Status != "completed" {
		t.Errorf("session.Status: got %q, want completed", sess.Status)
	}
	var postChange model.Change
	db.Where("id = ?", changeID).First(&postChange)
	if postChange.Status != "approved" {
		t.Errorf("change.Status: got %q, want approved", postChange.Status)
	}
	if postChange.ReviewedAt == nil {
		t.Error("change.ReviewedAt should be set")
	}
	// AuditLevel must persist end-to-end now that broadcast.go
	// nil-guards Redis (pre-fix, the Redis nil-deref panic inside
	// approveChange made the later Save skip this field).
	if postChange.AuditLevel == nil || *postChange.AuditLevel != "L0" {
		lvl := "(nil)"
		if postChange.AuditLevel != nil {
			lvl = *postChange.AuditLevel
		}
		t.Errorf("change.AuditLevel: got %q, want L0", lvl)
	}

	var traces []model.ToolCallTrace
	db.Where("session_id = ?", sessionID).Find(&traces)
	if len(traces) < 1 {
		t.Errorf("tool_call_traces: got 0, want >=1")
	}
	for _, tr := range traces {
		if tr.ToolName != "audit_output" {
			t.Errorf("unexpected tool trace: %+v", tr)
		}
	}

	// Event sequencing
	for _, evtype := range []string{EventToolCall, EventAgentTurn, EventAgentDone, EventChatUpdate} {
		if rec.count(evtype) == 0 {
			t.Errorf("stream missing %s", evtype)
		}
	}
	if rec.count(EventAgentError) != 0 {
		t.Errorf("unexpected AGENT_ERROR on happy path")
	}

	// Wire-shape: first request should advertise the audit_output tool.
	bodiesMu.Lock()
	body0 := string(bodies[0])
	bodiesMu.Unlock()
	if !strings.Contains(body0, `"audit_output"`) {
		t.Errorf("first request should carry audit_output tool schema; body was %q", body0)
	}
}

// ---- test fixtures + recorder ----------------------------------------

var e2eTurn1 = []string{
	`data: {"choices":[{"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_audit","type":"function","function":{"name":"audit_output","arguments":""}}]},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"level\":\"L0\""}}]},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}` + "\n\n",
	`data: {"usage":{"prompt_tokens":120,"completion_tokens":15,"total_tokens":135},"choices":[]}` + "\n\n",
	`data: [DONE]` + "\n\n",
}

var e2eTurn2 = []string{
	`data: {"choices":[{"delta":{"role":"assistant","content":"Audit complete."},"index":0}]}` + "\n\n",
	`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}` + "\n\n",
	`data: [DONE]` + "\n\n",
}

type e2eRecorder struct {
	mu     sync.Mutex
	events []e2eEvent
}

type e2eEvent struct {
	Type    string
	Payload map[string]interface{}
}

func (r *e2eRecorder) emit(_, typ string, payload map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e2eEvent{typ, payload})
}

func (r *e2eRecorder) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}
