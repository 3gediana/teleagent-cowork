// Command planningsmoke — end-to-end integration smoke for the A3C
// platform using "AI-driven planning tool" as the sample project.
//
// Uses real MySQL + Redis (configured via configs/config.yaml with an
// override to the a3c_smoke database so we never stomp a live
// install) plus a local mock LLM that returns scripted responses per
// role. Exercises the full pipeline:
//
//     Chief chat → Maintain dialogue → Client agent registration
//     → Task claim → Change submit → Audit_1 (L1 case)
//     → Fix → Audit_2 → PR submit → Evaluate → BizReview → Merge
//     → Chief chat (multi-round continuation)
//     → Analyze / refinery
//
// Along the way we assert every state transition, collect every
// tool call, every DialogueMessage row, every emitted event — then
// dump a structured report so the operator can see exactly what the
// platform produced. Logic errors surface as ✗ markers in the report.
//
// Setup:
//   mysql -uroot -e "DROP DATABASE IF EXISTS a3c_smoke; CREATE DATABASE a3c_smoke CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
//
// Run:  cd platform/backend && go run ./cmd/planningsmoke
//
// Exit code: 0 on pass, 1 if any assertion fails.
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
	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/llm"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/runner"
	"github.com/a3c/platform/internal/service"
)

// ──────────────────────────────────────────────────────────────────
//  Global report state. Every observation flows in here so the
//  final "Final report" section can render a comprehensive summary
//  without plumbing a handful of slices through every helper.
// ──────────────────────────────────────────────────────────────────

type report struct {
	mu          sync.Mutex
	failures    []string
	steps       []string
	events      []emittedEvent
	llmCalls    []llmCall
	toolResults []toolResultLine
}

type emittedEvent struct {
	ProjectID string
	Type      string
	Payload   map[string]interface{}
}

type llmCall struct {
	Role         string
	ToolNames    []string
	ResponseType string // "tool_use:<name>" | "text"
}

type toolResultLine struct {
	SessionID string
	Role      string
	Tool      string
	Status    string
}

var r = &report{}

func note(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.steps = append(r.steps, msg)
	r.mu.Unlock()
	fmt.Println("  • " + msg)
}

func check(cond bool, msg string) {
	r.mu.Lock()
	if cond {
		fmt.Printf("  ✔ %s\n", msg)
	} else {
		fmt.Printf("  ✗ %s\n", msg)
		r.failures = append(r.failures, msg)
	}
	r.mu.Unlock()
}

func section(title string) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 72))
	fmt.Printf("  %s\n", title)
	fmt.Println(strings.Repeat("─", 72))
}

// ──────────────────────────────────────────────────────────────────
//  Mock LLM. Each request's tool list tells us the role; we return
//  a scripted response that matches what the runner expects for that
//  role's terminal tool.
// ──────────────────────────────────────────────────────────────────

// roleResponse is looked up by role-key (derived from the tool names
// present in the request). Returning an empty script signals "the
// role isn't scripted in this smoke" and the LLM will reply with a
// generic text stop.
type roleResponse struct {
	toolName string
	toolArgs map[string]interface{}
	text     string // when toolName == "" we just emit text
}

// mockState lives on the LLM server and tracks the role-turn counter
// so we can vary responses across turns (e.g. L1 audit → fix → L0
// audit_2).
type mockState struct {
	mu    sync.Mutex
	turns map[string]int // role -> turn count
}

func (s *mockState) take(role string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.turns[role]
	s.turns[role] = n + 1
	return n
}

func classifyRole(toolsInRequest []string) string {
	// Look for terminal / distinctive tools to tag the role. Order
	// matters: Maintain's tool set contains BOTH create_task (unique
	// to maintain) AND biz_review_output (also unique to maintain),
	// so we have to check the maintain-indicative tools BEFORE the
	// single-output terminals, or every maintain session would be
	// misclassified as biz_review.
	has := func(name string) bool {
		for _, t := range toolsInRequest {
			if t == name {
				return true
			}
		}
		return false
	}
	switch {
	// Maintain is the most distinctive: if any of its "work" tools
	// are present, we know the role even if its biz_review tool also
	// happens to be there.
	case has("create_task"), has("write_milestone"), has("update_milestone"),
		has("propose_direction"), has("delete_task"):
		return "maintain"
	// Single-output terminal tools map to their respective roles.
	case has("chief_output"):
		return "chief"
	case has("analyze_output"):
		return "analyze"
	case has("evaluate_output"):
		return "evaluate"
	case has("merge_output"):
		return "merge"
	case has("fix_output"):
		return "fix"
	case has("audit2_output"):
		return "audit_2"
	case has("audit_output"):
		return "audit_1"
	case has("biz_review_output"):
		return "biz_review"
	case has("assess_output"):
		return "assess"
	}
	return "unknown"
}

// scriptKey combines role with a session-id-independent counter so we
// can drive multi-session flows (e.g. Audit_1 fires twice: once for
// the original L1, once for the resubmit that should pass L0).
// Multiple chief chats likewise need distinct responses.
//
// The mock tracks these counters keyed by role; `scriptFor` reads the
// current turn and decides what to emit. A terminal tool response is
// followed by a plain text stop on the next turn so the runner's
// loop exits (otherwise we'd never see message_stop and would burn
// through MaxIterations).
func scriptFor(role string, turn int) roleResponse {
	// Second turn onwards for any role with a terminal tool: quietly
	// exit. The runner has already received the tool_result from the
	// first turn's tool_use, so stopping here matches what a real
	// LLM would do.
	if turn >= 1 && roleHasTerminalTool(role) {
		return roleResponse{text: "Done. (mock)"}
	}

	switch role {
	case "audit_1":
		// L1 verdict: flags the missing validation. Schema: level
		// (not "result"), issues[*]{file,line,type,detail,status=open}.
		return roleResponse{toolName: "audit_output", toolArgs: map[string]interface{}{
			"level": "L1",
			"issues": []map[string]interface{}{
				{
					"file":   "planner/schedule.go",
					"line":   42,
					"type":   "bug",
					"detail": "Schedule.AddTask doesn't validate that Deadline is after StartTime.",
					"status": "open",
				},
			},
			"pattern_observed": "Always validate relative time constraints at the API boundary before persisting.",
		}}
	case "fix":
		return roleResponse{toolName: "fix_output", toolArgs: map[string]interface{}{
			"action":       "fix",
			"fixed":        true,
			"fix_strategy": "Added explicit Deadline>StartTime check at the top of AddTask.",
		}}
	case "audit_2":
		return roleResponse{toolName: "audit2_output", toolArgs: map[string]interface{}{
			"result": "merge",
		}}
	case "evaluate":
		return roleResponse{toolName: "evaluate_output", toolArgs: map[string]interface{}{
			"result":             "approved",
			"merge_cost_rating":  "low",
			"recommended_action": "auto_advance",
			"quality_patterns":   "Table-driven tests exercise both the happy path and the invalid-deadline branch.",
		}}
	case "biz_review":
		return roleResponse{toolName: "biz_review_output", toolArgs: map[string]interface{}{
			"result":              "approved",
			"biz_review":          "Closes milestone item M1: deadline validation. Aligns with the 'minimal scheduler' direction. No scope concerns.",
			"version_suggestion":  "0.2.0",
			"alignment_rationale": "Small validation additions that close a milestone line item are always worth shipping, even without new tests.",
		}}
	case "merge":
		return roleResponse{toolName: "merge_output", toolArgs: map[string]interface{}{
			"result":           "success",
			"merge_commit_sha": "abc123def456abc123def456abc123def456abc1",
		}}
	case "chief":
		return roleResponse{toolName: "chief_output", toolArgs: map[string]interface{}{
			"result":  "reported",
			"summary": "规划表 MVP 的方向我理解了——先做任务列表和截止时间校验。我会让 Maintain 建第一个里程碑。",
		}}
	case "maintain":
		// The maintain role doesn't have a single "terminal tool" —
		// it can fire create_task / write_milestone / biz_review_output
		// depending on the prompt. We decide based on context
		// heuristics: if any biz_review language likely appears this
		// is a PR biz_review session; otherwise it's either a
		// milestone-write or task-create. Turn parity is our proxy.
		switch turn {
		case 0:
			return roleResponse{toolName: "write_milestone", toolArgs: map[string]interface{}{
				"name":        "M1: 核心调度器",
				"description": "实现最小可用的任务增删改查 + 截止时间校验。",
			}}
		case 1:
			return roleResponse{toolName: "create_task", toolArgs: map[string]interface{}{
				"name":        "实现 Schedule.AddTask 的截止时间校验",
				"description": "确保 deadline 晚于 start_time，否则返回 ErrInvalidSchedule。",
				"priority":    "high",
			}}
		default:
			// PR biz review session (turn>=2).
			return roleResponse{toolName: "biz_review_output", toolArgs: map[string]interface{}{
				"result":             "approved",
				"biz_review":         "Aligned with M1 deadline validation work. No scope concerns.",
				"version_suggestion": "0.2.0",
			}}
		}
	case "analyze":
		return roleResponse{toolName: "analyze_output", toolArgs: map[string]interface{}{
			"distilled_experience_ids": []string{"exp_1", "exp_2"},
			"skill_candidates": []map[string]interface{}{
				{
					"name":             "validate_inputs_before_storing",
					"type":             "pattern",
					"applicable_tags":  []string{"validation", "bugfix"},
					"precondition":     "About to persist caller-supplied struct fields.",
					"action":           "Validate every field with a natural range constraint before the write.",
					"prohibition":      "Don't defer validation to the DB layer for errors the caller can cheaply prevent.",
					"evidence":         []string{"exp_1", "exp_2"},
				},
			},
			"policy_suggestions": []interface{}{},
		}}
	case "assess":
		return roleResponse{toolName: "assess_output", toolArgs: map[string]interface{}{
			"summary": "AI planning tool — Go module, 3 packages, no DB yet.",
		}}
	}
	// Fallback: plain text stop. Unknown role or unexpected extra turn.
	return roleResponse{text: "(smoke: unscripted response for role=" + role + ")"}
}

// roleHasTerminalTool returns true for roles whose conversation model
// is "emit one terminal tool then stop". The mock uses it to decide
// when to switch a role's response to plain text (so the runner loop
// terminates). Maintain is intentionally excluded because it fires
// different tools on different turns; we cap its behaviour via the
// switch inside scriptFor instead.
func roleHasTerminalTool(role string) bool {
	switch role {
	case "audit_1", "audit_2", "fix", "evaluate", "merge",
		"biz_review", "chief", "analyze", "assess":
		return true
	}
	return false
}

// buildSSEResponse renders a role response as Anthropic-style SSE the
// native runner understands. We emit:
//   message_start → content_block_start(tool_use) →
//   content_block_delta*N → content_block_stop → message_delta →
//   message_stop
// ...or, when toolName is empty, just a text block sequence.
func buildSSEResponse(rr roleResponse) string {
	var sb strings.Builder
	writeEvent := func(eventName, data string) {
		sb.WriteString("event: " + eventName + "\n")
		sb.WriteString("data: " + data + "\n\n")
	}

	writeEvent("message_start", `{"type":"message_start","message":{"id":"msg_test","role":"assistant","type":"message","model":"smoke-model","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":10}}}`)

	if rr.toolName != "" {
		argsJSON, _ := json.Marshal(rr.toolArgs)
		writeEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_test","name":%q,"input":{}}}`, rr.toolName))
		writeEvent("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, string(argsJSON)))
		writeEvent("content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":20}}`)
	} else {
		text := rr.text
		if text == "" {
			text = "(smoke: empty text)"
		}
		textJSON, _ := json.Marshal(text)
		writeEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeEvent("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, string(textJSON)))
		writeEvent("content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}`)
	}

	writeEvent("message_stop", `{"type":"message_stop"}`)
	return sb.String()
}

// startMockLLM wires a real httptest server that speaks enough
// Anthropic wire protocol to drive the native runner.
//
// Turn detection is per-session: we inspect the last message of the
// request — if it carries tool_result blocks, we're past the first
// turn and the mock should emit a text-stop so the session
// terminates. If the last message is a plain text user message, this
// is the session's first LLM call and the role-appropriate tool_use
// should fire. This avoids the "role counter leaks across sessions"
// bug that earlier versions had (where session 2's turn 0 inherited
// the role counter from session 1).
func startMockLLM(state *mockState) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var reqPayload struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &reqPayload)
		toolNames := make([]string, 0, len(reqPayload.Tools))
		for _, t := range reqPayload.Tools {
			toolNames = append(toolNames, t.Name)
		}
		role := classifyRole(toolNames)

		// Detect turn from the LAST message in the request. A message
		// whose content contains at least one tool_result block means
		// we're on turn 1+ of this session; anything else means this
		// is the opening call.
		turn := 0
		if n := len(reqPayload.Messages); n > 0 {
			last := reqPayload.Messages[n-1]
			for _, blk := range last.Content {
				if blk.Type == "tool_result" {
					turn = 1
					break
				}
			}
		}

		// Advance the global counter ONLY when we're answering a
		// fresh session-start call (turn==0). The follow-up turn
		// (the "Done." text-stop) should not burn a counter slot
		// because it's the tail of the same session, not a new one.
		// Without this guard, maintain's globalTurn overflowed past
		// create_task on session #2 and every Phase-3 maintain
		// session ended up emitting biz_review_output.
		globalTurn := 0
		if turn == 0 {
			globalTurn = state.take(role)
		}
		rr := scriptForWithGlobal(role, turn, globalTurn)
		responseType := "text"
		if rr.toolName != "" {
			responseType = "tool_use:" + rr.toolName
		}
		r.mu.Lock()
		r.llmCalls = append(r.llmCalls, llmCall{Role: role, ToolNames: toolNames, ResponseType: responseType})
		r.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte(buildSSEResponse(rr)))
	}))
}

// scriptForWithGlobal wraps scriptFor with a "force text stop on
// turn>=1" rule applied to every role, and passes the cross-session
// globalTurn counter into the maintain branch so we can vary which
// tool the maintain agent picks across successive sessions.
func scriptForWithGlobal(role string, turn, globalTurn int) roleResponse {
	if turn >= 1 {
		return roleResponse{text: "Done. (mock)"}
	}
	// Turn 0 — same as scriptFor except maintain uses globalTurn to
	// pick between write_milestone / create_task / biz_review_output
	// across separate sessions.
	if role == "maintain" {
		switch globalTurn {
		case 0:
			return roleResponse{toolName: "write_milestone", toolArgs: map[string]interface{}{
				"content": "## Milestone: M1 核心调度器\n\n**Goal**: 实现最小可用的任务增删改查 + 截止时间校验。\n\n**Tasks**:\n- [ ] 支持 AddTask / RemoveTask\n- [ ] Deadline 校验",
			}}
		case 1:
			return roleResponse{toolName: "create_task", toolArgs: map[string]interface{}{
				"name":        "实现 Schedule.AddTask 的截止时间校验",
				"description": "确保 deadline 晚于 start_time，否则返回 ErrInvalidSchedule。",
				"priority":    "high",
			}}
		default:
			return roleResponse{toolName: "biz_review_output", toolArgs: map[string]interface{}{
				"result":             "approved",
				"biz_review":         "Aligned with M1 deadline validation work. No scope concerns.",
				"version_suggestion": "0.2.0",
			}}
		}
	}
	return scriptFor(role, 0)
}

// ──────────────────────────────────────────────────────────────────
//  DB + platform wiring.
// ──────────────────────────────────────────────────────────────────

// bootstrapDB loads configs/config.yaml but overrides the database
// name to a3c_smoke so this smoke never touches a real deployment.
// model.InitDB + InitRedis handle AutoMigrate + connection pooling.
func bootstrapDB() error {
	cfg := config.Load("")
	cfg.Database.DBName = "a3c_smoke" // force-isolate

	if err := model.InitDB(&cfg.Database); err != nil {
		return fmt.Errorf("mysql: %w", err)
	}
	if err := model.InitRedis(&cfg.Redis); err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	// Wipe a previous smoke run (if any) so assertions start clean.
	truncateAll()
	return nil
}

// truncateAll wipes every table we touch. FK-safety isn't an issue
// because GORM AutoMigrate in this codebase doesn't declare FK
// constraints; we can truncate in any order.
func truncateAll() {
	tables := []string{
		"project", "agent", "task", "content_block", "milestone",
		"milestone_archive", "agent_session", "tool_call_trace",
		"change_", "change", "role_override", "file_lock", "branch",
		"pull_request", "llm_endpoint", "dialogue_message",
		"policy", "experience", "skill_candidate", "task_tag",
		"knowledge_artifact", "episode", "refinery_run",
	}
	for _, t := range tables {
		// IF EXISTS so we don't error on models that renamed tables.
		model.DB.Exec("DELETE FROM `" + t + "`")
	}
}

func wirePlatform(llmURL string) {
	// Install a single llm_endpoint row pointing at the mock server
	// so runner.Dispatch resolves to native and actually hits it.
	endpoint := &model.LLMEndpoint{
		ID:           "llm_smoke",
		Name:         "Smoke Anthropic Mock",
		Format:       "anthropic",
		BaseURL:      llmURL,
		APIKey:       "smoke-key",
		Status:       "active",
		Models:       `[{"id":"smoke-model","name":"smoke","supports_tools":true}]`,
		DefaultModel: "smoke-model",
	}
	if err := model.DB.Create(endpoint).Error; err != nil {
		log.Fatalf("create LLMEndpoint: %v", err)
	}
	if err := llm.LoadEndpoint(endpoint.ID); err != nil {
		log.Fatalf("load endpoint: %v", err)
	}

	// Wire runner hooks just like cmd/server/main.go does.
	runner.NativeRegistryBuilder = runner.PlatformRegistryBuilder
	runner.PlatformToolSink = service.HandleToolCallResult
	runner.SessionCompletionHandler = service.HandleSessionCompletion
	runner.StreamEmitter = func(projectID, eventType string, payload map[string]interface{}) {
		r.mu.Lock()
		r.events = append(r.events, emittedEvent{ProjectID: projectID, Type: eventType, Payload: payload})
		r.mu.Unlock()
	}
	agent.RegisterDispatcher(runner.Dispatch)
	service.InitDataPath(os.TempDir())
}

// ──────────────────────────────────────────────────────────────────
//  Helpers for setting up the scenario.
// ──────────────────────────────────────────────────────────────────

func seedProjectAndHuman() (projectID, humanID string) {
	projectID = "proj_planner"
	humanID = "agent_human_smoke"
	model.DB.Create(&model.Project{ID: projectID, Name: "AI-Driven Planning Tool", Status: "ready"})
	model.DB.Create(&model.Agent{
		ID: humanID, Name: "operator", Status: "online",
		CurrentProjectID: &projectID, AccessKey: "hk_smoke", IsHuman: true,
	})
	// Seed an initial Direction so Chief's context-builder has something
	// to read (buildChiefContext fans out several DB queries).
	model.DB.Create(&model.ContentBlock{
		ID: "cb_dir", ProjectID: projectID, BlockType: "direction",
		Content: "做一个 AI 驱动的任务规划工具，帮助用户自动安排时间并校验冲突。",
		Version: 1,
	})
	return
}

func seedClientAgent(projectID string) string {
	agentID := "agent_client_smoke"
	model.DB.Create(&model.Agent{
		ID: agentID, Name: "planner-worker", Status: "online",
		CurrentProjectID: &projectID, AccessKey: "hk_client",
	})
	return agentID
}

// waitForSessionStatus polls the agent manager + DB until the named
// session reaches one of the terminal statuses — or the deadline
// expires. Returns the final session so callers can assert on output.
func waitForSessionStatus(sessionID string, want []string, timeout time.Duration) *agent.Session {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := agent.DefaultManager.GetSession(sessionID)
		if s != nil {
			for _, w := range want {
				if s.Status == w {
					return s
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return agent.DefaultManager.GetSession(sessionID)
}

// waitForCount polls until the named GORM query returns at least `want` rows.
func waitForCount(query func() int64, want int64, timeout time.Duration) int64 {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if n := query(); n >= want {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
	return query()
}

// ──────────────────────────────────────────────────────────────────
//  Main: orchestrate the scenario step by step.
// ──────────────────────────────────────────────────────────────────

func main() {
	log.SetFlags(0)
	fmt.Println(strings.Repeat("═", 72))
	fmt.Println("  A3C Platform — End-to-End Smoke: AI-Driven Planning Tool")
	fmt.Println(strings.Repeat("═", 72))

	// ── Setup ────────────────────────────────────────────────────
	section("Bootstrap")
	if err := bootstrapDB(); err != nil {
		log.Fatalf("db: %v", err)
	}
	db := model.DB
	note("MySQL (a3c_smoke) + Redis up; all tables migrated and truncated")

	state := &mockState{turns: map[string]int{}}
	srv := startMockLLM(state)
	defer srv.Close()
	note("Mock LLM listening at %s", srv.URL)

	wirePlatform(srv.URL)
	note("Runner + platform tools wired; native runner is the only dispatcher")

	projectID, humanID := seedProjectAndHuman()
	note("Project=%s human=%s", projectID, humanID)

	// ── Phase 1: Chief chat (human sets direction) ──────────────
	section("Phase 1: Chief chat — human explains what the project is")
	service.TriggerChiefChat(projectID, "我想做一个 AI 驱动的任务规划表。第一个里程碑先做最小调度器。")
	time.Sleep(300 * time.Millisecond) // TriggerChiefChat is async

	// The session id isn't returned, so discover it by role.
	var chiefSession model.AgentSession
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("project_id = ? AND role = ?", projectID, "chief").Count(&n)
		return n
	}, 1, 5*time.Second)
	db.Where("project_id = ? AND role = ?", projectID, "chief").Order("created_at DESC").First(&chiefSession)

	finalChief := waitForSessionStatus(chiefSession.ID, []string{"completed", "failed"}, 5*time.Second)
	check(finalChief != nil && finalChief.Status == "completed", "Chief chat session reaches completed")
	// finalChief is the in-memory session; its Output should be the
	// chief_output tool's `summary` argument (the chat response).
	check(strings.Contains(finalChief.Output, "M1") || strings.Contains(finalChief.Output, "规划表"),
		fmt.Sprintf("Chief session.Output carries tool summary (got %q)", truncate(finalChief.Output, 60)))

	// Dialogue history should have both user + assistant rows.
	var dialogueRows []model.DialogueMessage
	db.Where("project_id = ? AND channel = ?", projectID, "chief").Order("created_at ASC").Find(&dialogueRows)
	check(len(dialogueRows) >= 2, fmt.Sprintf("DialogueMessage[chief] captured %d turn(s)", len(dialogueRows)))
	var sawUser, sawAssistant bool
	for _, m := range dialogueRows {
		if m.Role == "user" {
			sawUser = true
		}
		if m.Role == "assistant" {
			sawAssistant = true
		}
	}
	check(sawUser && sawAssistant, "Chief dialogue has both user + assistant turns")

	// ── Phase 2: Maintain creates milestone ─────────────────────
	section("Phase 2: Maintain agent creates milestone")
	if err := service.TriggerMaintainAgent(projectID, "timer", "设置第一个里程碑"); err != nil {
		log.Fatalf("maintain: %v", err)
	}
	var maintainSession model.AgentSession
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("project_id = ? AND role = ? AND trigger_reason = ?", projectID, "maintain", "timer").Count(&n)
		return n
	}, 1, 5*time.Second)
	db.Where("project_id = ? AND role = ? AND trigger_reason = ?", projectID, "maintain", "timer").Order("created_at DESC").First(&maintainSession)
	waitForSessionStatus(maintainSession.ID, []string{"completed", "failed"}, 5*time.Second)

	// The write_milestone tool should have created a milestone.
	var milestone model.Milestone
	msErr := db.Where("project_id = ?", projectID).First(&milestone).Error
	check(msErr == nil, "Milestone row created by Maintain's write_milestone tool")
	if msErr == nil {
		check(strings.Contains(milestone.Name, "M1"), fmt.Sprintf("Milestone name contains M1: got %q", milestone.Name))
	}

	// ── Phase 3: Maintain creates task (dashboard-style input) ──
	section("Phase 3: Dashboard input — human asks Maintain to add a task")
	if err := service.TriggerMaintainAgent(projectID, "dashboard_task_input", "加一个任务：给 Schedule.AddTask 加截止时间校验"); err != nil {
		log.Fatalf("maintain dashboard: %v", err)
	}
	var maintainDash model.AgentSession
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("project_id = ? AND trigger_reason = ?", projectID, "dashboard_task_input").Count(&n)
		return n
	}, 1, 5*time.Second)
	db.Where("project_id = ? AND trigger_reason = ?", projectID, "dashboard_task_input").Order("created_at DESC").First(&maintainDash)
	waitForSessionStatus(maintainDash.ID, []string{"completed", "failed"}, 5*time.Second)

	var tasks []model.Task
	db.Where("project_id = ?", projectID).Find(&tasks)
	check(len(tasks) >= 1, fmt.Sprintf("Maintain's create_task tool produced %d task row(s)", len(tasks)))

	var maintainDialogueRows []model.DialogueMessage
	db.Where("project_id = ? AND channel = ?", projectID, "maintain").Find(&maintainDialogueRows)
	check(len(maintainDialogueRows) >= 2, fmt.Sprintf("DialogueMessage[maintain] captured %d turn(s)", len(maintainDialogueRows)))

	// ── Phase 4: Client agent claims + submits change ───────────
	section("Phase 4: Client agent submits a change")
	clientID := seedClientAgent(projectID)

	if len(tasks) == 0 {
		// Fall back: make a task manually so the rest of the pipeline
		// has a target. Happens when Phase 3 didn't produce one.
		db.Create(&model.Task{
			ID: model.GenerateID("task"), ProjectID: projectID,
			Name: "Seed task (fallback)", Priority: "high", Status: "pending",
		})
		db.Where("project_id = ?", projectID).Find(&tasks)
	}
	task := tasks[0]
	task.AssigneeID = &clientID
	task.Status = "claimed"
	db.Save(&task)
	note("Agent %s claimed task %s", clientID, task.ID)

	// Submit a change. The audit workflow fires automatically from
	// service.StartAuditWorkflow — we drive it here directly to keep
	// the smoke synchronous.
	// All json-typed columns must carry parseable JSON — MySQL in
	// strict mode rejects empty strings with "Invalid JSON text:
	// The document is empty" on UPDATE (platform bug: many service
	// layer DB.Save calls ignore the returned error, so such failures
	// are silent — see change_feedback + audit.go db.Save sites).
	change := &model.Change{
		ID: model.GenerateID("chg"), ProjectID: projectID,
		TaskID:            &task.ID,
		AgentID:           clientID,
		Version:           "v0.1.0",
		Status:            "pending",
		ModifiedFiles:     `["planner/schedule.go"]`,
		NewFiles:          `[]`,
		DeletedFiles:      `[]`,
		Diff:              `{"planner/schedule.go":"+ func (s *Schedule) AddTask(...) error { ... }"}`,
		InjectedArtifacts: `[]`,
		Description:       "Add deadline validation to Schedule.AddTask",
		CreatedAt:         time.Now(),
	}
	db.Create(change)
	note("Change %s submitted", change.ID)

	if err := service.StartAuditWorkflow(change.ID); err != nil {
		log.Fatalf("audit workflow: %v", err)
	}

	// ── Phase 5: Observe Audit_1 (L1 path) ──────────────────────
	section("Phase 5: Audit_1 inspects the change")
	var audit1Session model.AgentSession
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("change_id = ? AND role = ?", change.ID, "audit_1").Count(&n)
		return n
	}, 1, 5*time.Second)
	db.Where("change_id = ? AND role = ?", change.ID, "audit_1").Order("created_at DESC").First(&audit1Session)
	waitForSessionStatus(audit1Session.ID, []string{"completed", "failed", "rejected", "pending_fix"}, 10*time.Second)

	// Check that the Change row absorbed the L1 verdict. Status is
	// intentionally permissive: ProcessAuditOutput flips the row to
	// pending_fix and immediately dispatches the Fix agent, so by
	// the time this check runs the Fix session may have already
	// approved (or rejected) the change. Any post-audit terminal
	// state is acceptable — the audit_level is the real signal that
	// audit_1 ran and produced a verdict.
	db.Where("id = ?", change.ID).First(change)
	check(change.AuditLevel != nil && *change.AuditLevel == "L1",
		fmt.Sprintf("Change.AuditLevel=L1 (got %v)", derefStr(change.AuditLevel)))
	check(change.Status == "pending_fix" || change.Status == "approved" || change.Status == "rejected",
		fmt.Sprintf("Change.Status reached a post-audit state (got %q)", change.Status))

	// ── Phase 6: Fix runs, then Audit_2 ─────────────────────────
	section("Phase 6: Fix + Audit_2 close the loop")
	// Fix doesn't auto-trigger in this smoke (usually triggered by
	// the audit_output handler in service.ProcessAuditOutput). Many
	// deployments wire that in; here we dispatch it explicitly so
	// we can observe the hand-off.
	fixCtx := service.BuildChangeContext(change)
	fixSess := agent.DefaultManager.CreateSession(agent.RoleFix, projectID, fixCtx, "fix_needed")
	fixSess.ChangeID = change.ID
	agent.DispatchSession(fixSess)
	waitForSessionStatus(fixSess.ID, []string{"completed", "failed"}, 10*time.Second)
	check(fixSess.Status == "completed" || agent.DefaultManager.GetSession(fixSess.ID).Status == "completed", "Fix session completes")

	audit2Ctx := service.BuildChangeContext(change)
	audit2Sess := agent.DefaultManager.CreateSession(agent.RoleAudit2, projectID, audit2Ctx, "re_audit")
	audit2Sess.ChangeID = change.ID
	agent.DispatchSession(audit2Sess)
	waitForSessionStatus(audit2Sess.ID, []string{"completed", "failed"}, 10*time.Second)
	check(audit2Sess.Status == "completed" || agent.DefaultManager.GetSession(audit2Sess.ID).Status == "completed", "Audit_2 session completes")

	// ── Phase 7: PR submit → Evaluate → BizReview → Merge ───────
	section("Phase 7: PR pipeline")
	branch := &model.Branch{
		ID: model.GenerateID("brn"), ProjectID: projectID,
		Name: "feat/schedule-validation", BaseVersion: "v0.1.0",
		Status: "open", CreatorID: clientID, CreatedAt: time.Now(),
	}
	db.Create(branch)

	pr := &model.PullRequest{
		ID: model.GenerateID("pr"), ProjectID: projectID,
		BranchID: branch.ID, Title: "feat: add deadline validation",
		Description: "Ensures AddTask rejects Deadline < StartTime.",
		SelfReview:  `{"ok": true, "risk": "low"}`,
		Status:      "pending_human_review", SubmitterID: clientID,
		DiffStat: `[{"file":"planner/schedule.go","insertions":12,"deletions":0}]`,
		DiffFull: "diff --git a/planner/schedule.go b/planner/schedule.go\n@@\n+ validate deadline\n",
		CreatedAt: time.Now(),
	}
	db.Create(pr)
	note("PR %s created", pr.ID)

	if err := service.TriggerEvaluateAgent(pr); err != nil {
		log.Fatalf("evaluate: %v", err)
	}
	var evalSess model.AgentSession
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("pr_id = ? AND role = ?", pr.ID, "evaluate").Count(&n)
		return n
	}, 1, 5*time.Second)
	db.Where("pr_id = ? AND role = ?", pr.ID, "evaluate").Order("created_at DESC").First(&evalSess)
	waitForSessionStatus(evalSess.ID, []string{"completed", "failed"}, 10*time.Second)
	db.Where("id = ?", evalSess.ID).First(&evalSess) // refresh
	check(evalSess.Status == "completed", fmt.Sprintf("Evaluate session completes (got %q)", evalSess.Status))

	db.Where("id = ?", pr.ID).First(pr)
	check(pr.TechReview != "" && strings.Contains(pr.TechReview, "merge_cost_rating"), "PR.TechReview populated by evaluate_output")

	if err := service.TriggerMaintainBizReview(pr); err != nil {
		log.Fatalf("biz review: %v", err)
	}
	var bizSess model.AgentSession
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("pr_id = ? AND role = ? AND trigger_reason = ?", pr.ID, "maintain", "pr_biz_review").Count(&n)
		return n
	}, 1, 5*time.Second)
	// Maintain's trigger_reason for biz review — inspect pr_agent for exact string
	// (the trigger_reason isn't set explicitly so it stays empty; just grab the
	// most recent maintain session on the PR).
	db.Where("pr_id = ? AND role = ?", pr.ID, "maintain").Order("created_at DESC").First(&bizSess)
	waitForSessionStatus(bizSess.ID, []string{"completed", "failed"}, 10*time.Second)
	db.Where("id = ?", bizSess.ID).First(&bizSess)
	check(bizSess.Status == "completed", fmt.Sprintf("BizReview (Maintain) session completes (got %q)", bizSess.Status))

	if err := service.TriggerMergeAgent(pr); err != nil {
		log.Fatalf("merge: %v", err)
	}
	var mergeSess model.AgentSession
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("pr_id = ? AND role = ?", pr.ID, "merge").Count(&n)
		return n
	}, 1, 5*time.Second)
	db.Where("pr_id = ? AND role = ?", pr.ID, "merge").Order("created_at DESC").First(&mergeSess)
	waitForSessionStatus(mergeSess.ID, []string{"completed", "failed"}, 10*time.Second)
	db.Where("id = ?", mergeSess.ID).First(&mergeSess)
	check(mergeSess.Status == "completed", fmt.Sprintf("Merge session completes (got %q)", mergeSess.Status))

	// ── Phase 8: Chief follow-up (multi-round dialogue) ─────────
	section("Phase 8: Chief follow-up — multi-round dialogue")
	service.TriggerChiefChat(projectID, "M1 进展怎么样？")
	time.Sleep(300 * time.Millisecond)
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("project_id = ? AND role = ?", projectID, "chief").Count(&n)
		return n
	}, 2, 5*time.Second)

	var allChiefRows []model.DialogueMessage
	db.Where("project_id = ? AND channel = ?", projectID, "chief").Order("created_at ASC").Find(&allChiefRows)
	// Give the 2nd session a beat to complete + write its assistant row.
	deadline := time.Now().Add(5 * time.Second)
	for len(allChiefRows) < 4 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		db.Where("project_id = ? AND channel = ?", projectID, "chief").Order("created_at ASC").Find(&allChiefRows)
	}
	check(len(allChiefRows) >= 4, fmt.Sprintf("Chief dialogue accumulated >=4 turns after follow-up (got %d)", len(allChiefRows)))

	// ── Phase 9: Analyze (refinery stand-in) ────────────────────
	section("Phase 9: Analyze — distill experiences into skills")
	// Seed a couple of fake experience rows so the trigger isn't a no-op.
	db.Create(&model.Experience{
		ID: "exp_1", ProjectID: projectID, TaskID: task.ID,
		Outcome: "L1_fixed", Status: "raw", CreatedAt: time.Now(),
	})
	db.Create(&model.Experience{
		ID: "exp_2", ProjectID: projectID, TaskID: task.ID,
		Outcome: "passed_audit2", Status: "raw", CreatedAt: time.Now(),
	})
	service.TriggerAnalyzeAgent(projectID)
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("project_id = ? AND role = ?", projectID, "analyze").Count(&n)
		return n
	}, 1, 5*time.Second)
	var analyzeSess model.AgentSession
	db.Where("project_id = ? AND role = ?", projectID, "analyze").Order("created_at DESC").First(&analyzeSess)
	waitForSessionStatus(analyzeSess.ID, []string{"completed", "failed"}, 10*time.Second)
	db.Where("id = ?", analyzeSess.ID).First(&analyzeSess)
	check(analyzeSess.Status == "completed", fmt.Sprintf("Analyze session completes (got %q)", analyzeSess.Status))

	// ── Final report ───────────────────────────────────────────
	section("Final report")
	printSummary(projectID, task.ID, change.ID, pr.ID)

	fmt.Println()
	if len(r.failures) == 0 {
		fmt.Println(strings.Repeat("═", 72))
		fmt.Println("  ✅  ALL CHECKS PASSED")
		fmt.Println(strings.Repeat("═", 72))
		return
	}
	fmt.Println(strings.Repeat("═", 72))
	fmt.Printf("  ❌  %d check(s) FAILED:\n", len(r.failures))
	for _, f := range r.failures {
		fmt.Printf("       - %s\n", f)
	}
	fmt.Println(strings.Repeat("═", 72))
	os.Exit(1)
}

func derefStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// printSummary renders the accumulated evidence as a structured
// block: LLM calls by role, events by type, DB row counts per table.
// This is the "what did the platform actually do?" picture.
func printSummary(projectID, taskID, changeID, prID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fmt.Println()
	fmt.Println("  📞  LLM calls by role:")
	roleCounts := map[string]int{}
	roleResp := map[string]map[string]int{}
	for _, c := range r.llmCalls {
		roleCounts[c.Role]++
		if roleResp[c.Role] == nil {
			roleResp[c.Role] = map[string]int{}
		}
		roleResp[c.Role][c.ResponseType]++
	}
	roles := make([]string, 0, len(roleCounts))
	for k := range roleCounts {
		roles = append(roles, k)
	}
	sort.Strings(roles)
	for _, role := range roles {
		fmt.Printf("     %-12s %d call(s)", role, roleCounts[role])
		var kinds []string
		for k, n := range roleResp[role] {
			kinds = append(kinds, fmt.Sprintf("%s×%d", k, n))
		}
		sort.Strings(kinds)
		fmt.Printf("   [%s]\n", strings.Join(kinds, ", "))
	}

	fmt.Println()
	fmt.Println("  📡  Events emitted by type:")
	eventCounts := map[string]int{}
	for _, e := range r.events {
		eventCounts[e.Type]++
	}
	eventKeys := make([]string, 0, len(eventCounts))
	for k := range eventCounts {
		eventKeys = append(eventKeys, k)
	}
	sort.Strings(eventKeys)
	for _, k := range eventKeys {
		fmt.Printf("     %-22s  %d\n", k, eventCounts[k])
	}

	fmt.Println()
	fmt.Println("  🗄️  DB state:")
	var n int64
	model.DB.Model(&model.AgentSession{}).Where("project_id = ?", projectID).Count(&n)
	fmt.Printf("     agent_session:       %d rows\n", n)
	model.DB.Model(&model.ToolCallTrace{}).Where("project_id = ?", projectID).Count(&n)
	fmt.Printf("     tool_call_trace:     %d rows\n", n)
	model.DB.Model(&model.DialogueMessage{}).Where("project_id = ?", projectID).Count(&n)
	fmt.Printf("     dialogue_message:    %d rows\n", n)
	model.DB.Model(&model.Task{}).Where("project_id = ?", projectID).Count(&n)
	fmt.Printf("     task:                %d rows\n", n)
	model.DB.Model(&model.Milestone{}).Where("project_id = ?", projectID).Count(&n)
	fmt.Printf("     milestone:           %d rows\n", n)
	model.DB.Model(&model.Change{}).Where("project_id = ?", projectID).Count(&n)
	fmt.Printf("     change:              %d rows\n", n)
	model.DB.Model(&model.PullRequest{}).Where("project_id = ?", projectID).Count(&n)
	fmt.Printf("     pull_request:        %d rows\n", n)

	fmt.Println()
	fmt.Println("  🗣️  Dialogue transcript (chief channel, chronological):")
	var rows []model.DialogueMessage
	model.DB.Where("project_id = ? AND channel = ?", projectID, "chief").Order("created_at ASC").Find(&rows)
	for i, m := range rows {
		label := "👤 user"
		if m.Role == "assistant" {
			label = "🤖 chief"
		}
		content := m.Content
		if len(content) > 120 {
			content = content[:117] + "..."
		}
		// Drop history prefix so the transcript stays readable — the
		// agent's stored "output" starts with the system's rehydrated
		// history markdown.
		if idx := strings.Index(content, "---"); idx >= 0 && idx < 20 {
			content = strings.TrimSpace(content[idx+3:])
		}
		fmt.Printf("     [%d] %-10s  %s\n", i+1, label, content)
	}

	fmt.Println()
	fmt.Println("  📊  Change + PR trajectory:")
	var ch model.Change
	model.DB.Where("id = ?", changeID).First(&ch)
	fmt.Printf("     change.status=%s  audit_level=%v\n", ch.Status, derefStr(ch.AuditLevel))
	var pr model.PullRequest
	model.DB.Where("id = ?", prID).First(&pr)
	fmt.Printf("     pull_request.status=%s  tech_review_len=%d  biz_review_len=%d\n",
		pr.Status, len(pr.TechReview), len(pr.BizReview))

	// Anomaly hints.
	if roleCounts["unknown"] > 0 {
		fmt.Println()
		fmt.Printf("  ⚠️  unknown-role LLM calls: %d — a tool set in the request didn't match any scripted role. Check scriptFor + classifyRole.\n", roleCounts["unknown"])
	}
}

