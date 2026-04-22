// Command planninglive — the full-platform integration test but with
// a REAL LLM in the loop. Same scenario as cmd/planningsmoke (Chief
// chat → Maintain → Audit_1 → Fix → Audit_2 → PR → Evaluate →
// BizReview → Merge → Analyze) except every agent's brain is driven
// by the provider configured under `llm.<provider>` in
// configs/config.yaml (default: minimax).
//
// Because the LLM is non-deterministic, assertions here are about
// *shape*, not content:
//   - The session ran and reached terminal status.
//   - The terminal tool call produced a parseable verdict.
//   - DB rows were created / transitioned in the expected direction.
//   - No silent error swallowing on the platform side.
//
// The binary prints every LLM turn, every tool call and every DB
// state change so the operator can eyeball how a real model actually
// wields the platform's tool schemas.
//
// Setup (once per run):
//   mysql -uroot -e "DROP DATABASE IF EXISTS a3c_live; CREATE DATABASE a3c_live CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
//
// Run:
//   cd platform/backend && go run ./cmd/planninglive [--provider minimax]
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
//  Report state (same shape as planningsmoke so the operator sees
//  a consistent summary layout across mock + live runs).
// ──────────────────────────────────────────────────────────────────

type liveReport struct {
	mu          sync.Mutex
	failures    []string
	events      []emittedEvent
	toolCalls   []toolCallRecord
	startedAt   time.Time
	llmCallCnt  int
	totalTokIn  int
	totalTokOut int
	totalUSD    float64
}

type emittedEvent struct {
	ProjectID string
	Type      string
	Payload   map[string]interface{}
}
type toolCallRecord struct {
	SessionID string
	Role      string
	Tool      string
	Args      string
}

var r = &liveReport{}

func note(format string, args ...interface{}) {
	fmt.Println("  • " + fmt.Sprintf(format, args...))
}
func check(cond bool, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cond {
		fmt.Printf("  ✔ %s\n", msg)
		return
	}
	fmt.Printf("  ✗ %s\n", msg)
	r.failures = append(r.failures, msg)
}
func section(title string) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 72))
	fmt.Printf("  %s\n", title)
	fmt.Println(strings.Repeat("─", 72))
}

// ──────────────────────────────────────────────────────────────────
//  Config / provider credentials (same pattern as nativesmokereal).
// ──────────────────────────────────────────────────────────────────

func pickCreds(cfg *config.LLMConfig, name string) (config.ProviderCreds, string, string) {
	switch strings.ToLower(name) {
	case "minimax":
		return cfg.MiniMax, "openai", cfg.MiniMax.Model
	case "openai":
		return cfg.OpenAI, "openai", cfg.OpenAI.Model
	case "anthropic":
		return cfg.Anthropic, "anthropic", cfg.Anthropic.Model
	case "deepseek":
		return cfg.DeepSeek, "openai", cfg.DeepSeek.Model
	}
	return config.ProviderCreds{}, "", ""
}

func redactKey(key string) string {
	if len(key) < 8 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}

// ──────────────────────────────────────────────────────────────────
//  Project seed — a real on-disk repo so audit/fix agents actually
//  have files to read. Minimal: one .go file with the deliberate bug
//  we'd like audit_1 to notice (Schedule.AddTask does not validate
//  that Deadline > StartTime).
// ──────────────────────────────────────────────────────────────────

const seedFileContent = `package planner

import (
	"errors"
	"time"
)

// Schedule holds tasks with start + deadline times. The core model
// layer of the AI-driven planning tool MVP.
type Schedule struct {
	Tasks []Task
}

type Task struct {
	Title     string
	StartTime time.Time
	Deadline  time.Time
}

// ErrInvalidSchedule signals that a Task violates ordering invariants.
var ErrInvalidSchedule = errors.New("invalid schedule: deadline before start time")

// AddTask appends a new task to the schedule.
//
// BUG (intentional, for audit_1 to find): Deadline can be earlier
// than StartTime and this method still succeeds. The check should be
// Deadline.After(StartTime) — if not, return ErrInvalidSchedule.
func (s *Schedule) AddTask(t Task) error {
	s.Tasks = append(s.Tasks, t)
	return nil
}

// RemoveTask drops the task at the given index.
func (s *Schedule) RemoveTask(i int) error {
	if i < 0 || i >= len(s.Tasks) {
		return errors.New("index out of range")
	}
	s.Tasks = append(s.Tasks[:i], s.Tasks[i+1:]...)
	return nil
}
`

func seedProjectRepo(projectID, dataDir string) string {
	root := filepath.Join(dataDir, "projects", projectID, "repo")
	pkgDir := filepath.Join(root, "planner")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		log.Fatalf("seed repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "schedule.go"), []byte(seedFileContent), 0o644); err != nil {
		log.Fatalf("seed file: %v", err)
	}
	return root
}

// ──────────────────────────────────────────────────────────────────
//  DB + platform wiring.
// ──────────────────────────────────────────────────────────────────

func bootstrapDB() (*config.Config, error) {
	cfg := config.Load("")
	cfg.Database.DBName = "a3c_live"

	if err := model.InitDB(&cfg.Database); err != nil {
		return nil, fmt.Errorf("mysql: %w", err)
	}
	if err := model.InitRedis(&cfg.Redis); err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	truncateAll()
	return cfg, nil
}

func truncateAll() {
	tables := []string{
		"project", "agent", "task", "content_block", "milestone",
		"milestone_archive", "agent_session", "tool_call_trace",
		"change", "role_override", "file_lock", "branch",
		"pull_request", "llm_endpoint", "dialogue_message",
		"policy", "experience", "skill_candidate", "task_tag",
		"knowledge_artifact", "episode", "refinery_run",
	}
	for _, t := range tables {
		model.DB.Exec("DELETE FROM `" + t + "`")
	}
}

func wirePlatform(cfg *config.Config, provider string) string {
	creds, format, defaultModel := pickCreds(&cfg.LLM, provider)
	if creds.APIKey == "" {
		fmt.Fprintf(os.Stderr, "✗ llm.%s.api_key is empty in configs/config.yaml\n", provider)
		os.Exit(2)
	}
	endpointID := model.GenerateID("llm")
	ep := &model.LLMEndpoint{
		ID:           endpointID,
		Name:         "live-" + provider,
		Format:       format,
		BaseURL:      creds.BaseURL,
		APIKey:       creds.APIKey,
		Models:       fmt.Sprintf(`[{"id":%q,"name":%q,"supports_tools":true}]`, defaultModel, defaultModel),
		DefaultModel: defaultModel,
		Status:       "active",
	}
	if err := model.DB.Create(ep).Error; err != nil {
		log.Fatalf("create LLMEndpoint: %v", err)
	}
	if err := llm.LoadEndpoint(endpointID); err != nil {
		log.Fatalf("load endpoint: %v", err)
	}
	note("Endpoint %s registered (format=%s model=%s key=%s)",
		endpointID, format, defaultModel, redactKey(creds.APIKey))

	runner.NativeRegistryBuilder = runner.PlatformRegistryBuilder
	runner.PlatformToolSink = func(sessionID, changeID, projectID, toolName string, args map[string]interface{}) {
		// Tap the sink so we see every tool call with its args before
		// the real handler runs. Args are truncated for readability.
		argsBlob, _ := compactJSON(args, 200)
		r.mu.Lock()
		r.toolCalls = append(r.toolCalls, toolCallRecord{
			SessionID: sessionID, Role: roleFromSession(sessionID),
			Tool: toolName, Args: argsBlob,
		})
		r.mu.Unlock()
		fmt.Printf("    🔧 tool_call  session=%s  tool=%s  args=%s\n",
			shortID(sessionID), toolName, argsBlob)
		service.HandleToolCallResult(sessionID, changeID, projectID, toolName, args)
	}
	runner.SessionCompletionHandler = service.HandleSessionCompletion
	runner.StreamEmitter = func(projectID, eventType string, payload map[string]interface{}) {
		r.mu.Lock()
		r.events = append(r.events, emittedEvent{ProjectID: projectID, Type: eventType, Payload: payload})
		r.mu.Unlock()
	}
	agent.RegisterDispatcher(runner.Dispatch)
	service.InitDataPath(cfg.DataDir)

	// Force EVERY platform role onto this endpoint so we exercise the
	// real-LLM path uniformly. (Without explicit overrides, the
	// dispatcher's fresh-install fallback would route here anyway —
	// but being explicit makes the report cleaner and future-proofs
	// against someone adding a role that isn't in the fallback's
	// first-endpoint pick.)
	roles := []agent.Role{
		agent.RoleAudit1, agent.RoleAudit2, agent.RoleFix,
		agent.RoleEvaluate, agent.RoleMerge, agent.RoleMaintain,
		agent.RoleConsult, agent.RoleAssess, agent.RoleChief,
		agent.RoleAnalyze,
	}
	for _, role := range roles {
		if err := agent.SetRoleOverride(role, endpointID, defaultModel); err != nil {
			log.Fatalf("role override %s: %v", role, err)
		}
	}

	return endpointID
}

// ──────────────────────────────────────────────────────────────────
//  Tiny helpers for the live log.
// ──────────────────────────────────────────────────────────────────

func shortID(id string) string {
	if len(id) > 14 {
		return id[len(id)-12:]
	}
	return id
}

func roleFromSession(sessionID string) string {
	var s model.AgentSession
	if err := model.DB.Select("role").Where("id = ?", sessionID).First(&s).Error; err == nil {
		return s.Role
	}
	return "?"
}

func compactJSON(v interface{}, limit int) (string, error) {
	blob := fmt.Sprintf("%v", v)
	if len(blob) > limit {
		return blob[:limit] + "…", nil
	}
	return blob, nil
}

// waitForStatus polls the in-memory session manager until the session
// reaches one of the terminal statuses or the deadline expires. Real
// LLM calls can take 5-20s per turn × MaxIterations — so we allow up
// to 4 minutes per session.
func waitForStatus(sessionID string, want []string, timeout time.Duration) *agent.Session {
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
		time.Sleep(250 * time.Millisecond)
	}
	return agent.DefaultManager.GetSession(sessionID)
}

func waitForCount(query func() int64, want int64, timeout time.Duration) int64 {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if n := query(); n >= want {
			return n
		}
		time.Sleep(100 * time.Millisecond)
	}
	return query()
}

// ──────────────────────────────────────────────────────────────────
//  Main: orchestrate the scenario against the real LLM.
// ──────────────────────────────────────────────────────────────────

func main() {
	log.SetFlags(0)
	provider := flag.String("provider", "minimax", "llm.<provider> from configs/config.yaml")
	skipAnalyze := flag.Bool("skip-analyze", false, "skip the analyze phase (save tokens while iterating)")
	flag.Parse()

	fmt.Println(strings.Repeat("═", 72))
	fmt.Println("  A3C Platform — LIVE End-to-End: AI-Driven Planning Tool")
	fmt.Printf("  Provider: %s\n", *provider)
	fmt.Println(strings.Repeat("═", 72))

	r.startedAt = time.Now()

	// Phase 0: infra + project seed ────────────────────────────────
	section("Phase 0: Bootstrap (MySQL + Redis + real LLM endpoint)")
	cfg, err := bootstrapDB()
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	db := model.DB

	projectID := "proj_live_planner"
	humanID := "agent_human_live"
	db.Create(&model.Project{ID: projectID, Name: "AI-Driven Planning Tool (live)", Status: "ready"})
	db.Create(&model.Agent{
		ID: humanID, Name: "operator", Status: "online",
		CurrentProjectID: &projectID, AccessKey: "hk_live", IsHuman: true,
	})
	db.Create(&model.ContentBlock{
		ID: "cb_dir_live", ProjectID: projectID, BlockType: "direction",
		Content: "做一个 AI 驱动的任务规划工具，MVP 先做最小调度器：任务增删改查 + 截止时间校验。保持代码可读性，拒绝过度抽象。",
		Version: 1,
	})
	repoPath := seedProjectRepo(projectID, cfg.DataDir)
	note("Seeded repo at %s (has planner/schedule.go with a deliberate Deadline-validation bug)", repoPath)

	_ = wirePlatform(cfg, *provider)

	// Phase 1: Chief chat ─────────────────────────────────────────
	section("Phase 1: Chief chat")
	service.TriggerChiefChat(projectID, "我想做一个 AI 驱动的任务规划表。第一个里程碑先做最小调度器，包含任务增删改查和截止时间校验。")
	time.Sleep(500 * time.Millisecond)
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("project_id = ? AND role = ?", projectID, "chief").Count(&n)
		return n
	}, 1, 5*time.Second)
	var chiefSess model.AgentSession
	db.Where("project_id = ? AND role = ?", projectID, "chief").Order("created_at DESC").First(&chiefSess)
	finalChief := waitForStatus(chiefSess.ID, []string{"completed", "failed"}, 4*time.Minute)
	check(finalChief != nil && finalChief.Status == "completed",
		fmt.Sprintf("Chief chat completes (status=%q)", safeStatus(finalChief)))
	if finalChief != nil && finalChief.Output != "" {
		fmt.Printf("    📝 chief.Output: %s\n", truncate(finalChief.Output, 240))
	}

	var dlgChief []model.DialogueMessage
	db.Where("project_id = ? AND channel = ?", projectID, "chief").Order("created_at ASC").Find(&dlgChief)
	check(len(dlgChief) >= 2,
		fmt.Sprintf("chief dialogue ≥2 turns (got %d)", len(dlgChief)))

	// Phase 2: Maintain creates milestone ─────────────────────────
	section("Phase 2: Maintain agent creates first milestone")
	if err := service.TriggerMaintainAgent(projectID, "timer", "首次进入项目，请基于 Direction 创建第一个 milestone（M1：核心调度器），并列出 2-4 条任务作为 checklist。务必调用 write_milestone 工具输出 markdown。"); err != nil {
		log.Fatalf("maintain timer: %v", err)
	}
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("project_id = ? AND role = ? AND trigger_reason = ?", projectID, "maintain", "timer").Count(&n)
		return n
	}, 1, 5*time.Second)
	var maintSess model.AgentSession
	db.Where("project_id = ? AND role = ? AND trigger_reason = ?", projectID, "maintain", "timer").Order("created_at DESC").First(&maintSess)
	finalMaint := waitForStatus(maintSess.ID, []string{"completed", "failed"}, 4*time.Minute)
	check(finalMaint != nil && finalMaint.Status == "completed",
		fmt.Sprintf("Maintain (timer) completes (status=%q)", safeStatus(finalMaint)))

	var milestones []model.Milestone
	db.Where("project_id = ?", projectID).Find(&milestones)
	check(len(milestones) >= 1,
		fmt.Sprintf("Milestone row created by write_milestone (got %d row(s))", len(milestones)))
	if len(milestones) > 0 {
		fmt.Printf("    📝 milestone.Name=%q\n", milestones[0].Name)
		fmt.Printf("    📝 milestone.Description[:200]=%s\n", truncate(milestones[0].Description, 200))
	}

	// Phase 3: Dashboard input → Maintain creates a task ──────────
	section("Phase 3: Dashboard input — create first task")
	if err := service.TriggerMaintainAgent(projectID, "dashboard_task_input",
		"请给 Schedule.AddTask 加一条任务：要求在 Deadline 早于 StartTime 时返回 ErrInvalidSchedule。用 create_task 工具。"); err != nil {
		log.Fatalf("maintain dashboard: %v", err)
	}
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("project_id = ? AND trigger_reason = ?", projectID, "dashboard_task_input").Count(&n)
		return n
	}, 1, 5*time.Second)
	var maintDash model.AgentSession
	db.Where("project_id = ? AND trigger_reason = ?", projectID, "dashboard_task_input").Order("created_at DESC").First(&maintDash)
	finalDash := waitForStatus(maintDash.ID, []string{"completed", "failed"}, 4*time.Minute)
	check(finalDash != nil && finalDash.Status == "completed",
		fmt.Sprintf("Maintain (dashboard_task_input) completes (status=%q)", safeStatus(finalDash)))

	var tasks []model.Task
	db.Where("project_id = ?", projectID).Find(&tasks)
	check(len(tasks) >= 1, fmt.Sprintf("create_task produced %d task row(s)", len(tasks)))
	if len(tasks) > 0 {
		fmt.Printf("    📝 task[0].Name=%q priority=%s\n", tasks[0].Name, tasks[0].Priority)
	}

	// Phase 4: Client agent submits a change ──────────────────────
	section("Phase 4: Client agent submits a change")
	clientID := "agent_client_live"
	db.Create(&model.Agent{ID: clientID, Name: "planner-worker", Status: "online",
		CurrentProjectID: &projectID, AccessKey: "hk_client_live"})
	if len(tasks) == 0 {
		db.Create(&model.Task{ID: model.GenerateID("task"), ProjectID: projectID,
			Name: "fallback seed task", Priority: "high", Status: "pending"})
		db.Where("project_id = ?", projectID).Find(&tasks)
	}
	task := tasks[0]
	task.AssigneeID = &clientID
	task.Status = "claimed"
	db.Save(&task)

	changeID := model.GenerateID("chg")
	db.Create(&model.Change{
		ID: changeID, ProjectID: projectID,
		TaskID:            &task.ID,
		AgentID:           clientID,
		Version:           "v0.1.0",
		Status:            "pending",
		ModifiedFiles:     `["planner/schedule.go"]`,
		NewFiles:          `[]`,
		DeletedFiles:      `[]`,
		Diff:              `{"planner/schedule.go":"AddTask currently accepts any Task without checking Deadline vs StartTime."}`,
		InjectedArtifacts: `[]`,
		Description:       "Implemented Schedule.AddTask (missing deadline validation, for audit_1 to catch).",
		CreatedAt:         time.Now(),
	})
	note("Change %s submitted for task %q", changeID, task.Name)

	if err := service.StartAuditWorkflow(changeID); err != nil {
		log.Fatalf("audit workflow: %v", err)
	}

	// Phase 5: Audit_1 ─────────────────────────────────────────────
	section("Phase 5: Audit_1 inspects the change")
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("change_id = ? AND role = ?", changeID, "audit_1").Count(&n)
		return n
	}, 1, 5*time.Second)
	var audit1 model.AgentSession
	db.Where("change_id = ? AND role = ?", changeID, "audit_1").Order("created_at DESC").First(&audit1)
	waitForStatus(audit1.ID, []string{"completed", "failed"}, 4*time.Minute)

	var postChange model.Change
	db.Where("id = ?", changeID).First(&postChange)
	auditLvl := derefStr(postChange.AuditLevel)
	check(postChange.AuditLevel != nil,
		fmt.Sprintf("Audit_1 produced a verdict (audit_level=%s, status=%q)", auditLvl, postChange.Status))
	fmt.Printf("    📝 audit verdict: %s (reason: %s)\n", auditLvl, truncate(postChange.AuditReason, 160))

	// If L1, the platform auto-dispatches Fix → Audit_2. Wait for the
	// change to settle into any terminal-ish status before moving on.
	waitForFor := 5 * time.Minute
	deadline := time.Now().Add(waitForFor)
	for time.Now().Before(deadline) {
		db.Where("id = ?", changeID).First(&postChange)
		if postChange.Status == "approved" || postChange.Status == "rejected" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Printf("    📝 final change.status=%q audit_level=%s\n",
		postChange.Status, derefStr(postChange.AuditLevel))

	// Phase 6: PR pipeline ─────────────────────────────────────────
	section("Phase 6: PR pipeline — Evaluate → BizReview → Merge")
	branch := &model.Branch{
		ID: model.GenerateID("brn"), ProjectID: projectID,
		Name: "feat/deadline-validation", BaseVersion: "v0.1.0",
		Status: "open", CreatorID: clientID, CreatedAt: time.Now(),
	}
	db.Create(branch)

	pr := &model.PullRequest{
		ID: model.GenerateID("pr"), ProjectID: projectID,
		BranchID: branch.ID, Title: "feat: add deadline validation to Schedule.AddTask",
		Description: "Ensures AddTask rejects Tasks whose Deadline precedes StartTime by returning ErrInvalidSchedule.",
		SelfReview:  `{"ok": true, "risk": "low", "tests_added": false}`,
		Status:      "pending_human_review", SubmitterID: clientID,
		DiffStat:    `[{"file":"planner/schedule.go","insertions":4,"deletions":0}]`,
		DiffFull:    "diff --git a/planner/schedule.go b/planner/schedule.go\n@@ AddTask @@\n+ if !t.Deadline.After(t.StartTime) {\n+     return ErrInvalidSchedule\n+ }",
		CreatedAt:   time.Now(),
	}
	db.Create(pr)
	note("PR %s submitted", pr.ID)

	if err := service.TriggerEvaluateAgent(pr); err != nil {
		log.Fatalf("evaluate: %v", err)
	}
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("pr_id = ? AND role = ?", pr.ID, "evaluate").Count(&n)
		return n
	}, 1, 5*time.Second)
	var evalSess model.AgentSession
	db.Where("pr_id = ? AND role = ?", pr.ID, "evaluate").Order("created_at DESC").First(&evalSess)
	waitForStatus(evalSess.ID, []string{"completed", "failed"}, 4*time.Minute)
	db.Where("id = ?", evalSess.ID).First(&evalSess)
	check(evalSess.Status == "completed",
		fmt.Sprintf("Evaluate completes (status=%q)", evalSess.Status))
	db.Where("id = ?", pr.ID).First(pr)
	fmt.Printf("    📝 pr.TechReview[:260]=%s\n", truncate(pr.TechReview, 260))

	if err := service.TriggerMaintainBizReview(pr); err != nil {
		log.Fatalf("biz review: %v", err)
	}
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("pr_id = ? AND role = ?", pr.ID, "maintain").Count(&n)
		return n
	}, 1, 5*time.Second)
	var bizSess model.AgentSession
	db.Where("pr_id = ? AND role = ?", pr.ID, "maintain").Order("created_at DESC").First(&bizSess)
	waitForStatus(bizSess.ID, []string{"completed", "failed"}, 4*time.Minute)
	db.Where("id = ?", bizSess.ID).First(&bizSess)
	check(bizSess.Status == "completed",
		fmt.Sprintf("BizReview (Maintain) completes (status=%q)", bizSess.Status))
	db.Where("id = ?", pr.ID).First(pr)
	fmt.Printf("    📝 pr.BizReview[:260]=%s\n", truncate(pr.BizReview, 260))

	if err := service.TriggerMergeAgent(pr); err != nil {
		log.Fatalf("merge: %v", err)
	}
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("pr_id = ? AND role = ?", pr.ID, "merge").Count(&n)
		return n
	}, 1, 5*time.Second)
	var mergeSess model.AgentSession
	db.Where("pr_id = ? AND role = ?", pr.ID, "merge").Order("created_at DESC").First(&mergeSess)
	waitForStatus(mergeSess.ID, []string{"completed", "failed"}, 4*time.Minute)
	db.Where("id = ?", mergeSess.ID).First(&mergeSess)
	check(mergeSess.Status == "completed",
		fmt.Sprintf("Merge completes (status=%q)", mergeSess.Status))

	// Phase 7: Chief follow-up ─────────────────────────────────────
	section("Phase 7: Chief follow-up — multi-round dialogue")
	service.TriggerChiefChat(projectID, "M1 进展怎么样？有没有 PR 待审？")
	time.Sleep(500 * time.Millisecond)
	waitForCount(func() int64 {
		var n int64
		db.Model(&model.AgentSession{}).Where("project_id = ? AND role = ?", projectID, "chief").Count(&n)
		return n
	}, 2, 5*time.Second)
	var chief2 model.AgentSession
	db.Where("project_id = ? AND role = ?", projectID, "chief").Order("created_at DESC").First(&chief2)
	finalChief2 := waitForStatus(chief2.ID, []string{"completed", "failed"}, 4*time.Minute)
	check(finalChief2 != nil && finalChief2.Status == "completed",
		fmt.Sprintf("Chief follow-up completes (status=%q)", safeStatus(finalChief2)))
	if finalChief2 != nil {
		fmt.Printf("    📝 chief2.Output=%s\n", truncate(finalChief2.Output, 240))
	}

	// Sanity check: dialogue grew across sessions.
	var dlgAll []model.DialogueMessage
	db.Where("project_id = ? AND channel = ?", projectID, "chief").Order("created_at ASC").Find(&dlgAll)
	check(len(dlgAll) >= 4,
		fmt.Sprintf("chief dialogue ≥4 turns after follow-up (got %d)", len(dlgAll)))

	// Phase 8: Analyze (optional — expensive) ──────────────────────
	if !*skipAnalyze {
		section("Phase 8: Analyze — distill experiences into skills")
		// Seed a couple experiences so analyze has material.
		db.Create(&model.Experience{
			ID: "exp_live_1", ProjectID: projectID, TaskID: task.ID,
			Outcome: "L1_fixed", Status: "raw", CreatedAt: time.Now(),
		})
		db.Create(&model.Experience{
			ID: "exp_live_2", ProjectID: projectID, TaskID: task.ID,
			Outcome: "passed_audit2", Status: "raw", CreatedAt: time.Now(),
		})
		service.TriggerAnalyzeAgent(projectID)
		waitForCount(func() int64 {
			var n int64
			db.Model(&model.AgentSession{}).Where("project_id = ? AND role = ?", projectID, "analyze").Count(&n)
			return n
		}, 1, 5*time.Second)
		var anaSess model.AgentSession
		db.Where("project_id = ? AND role = ?", projectID, "analyze").Order("created_at DESC").First(&anaSess)
		finalAna := waitForStatus(anaSess.ID, []string{"completed", "failed"}, 4*time.Minute)
		check(finalAna != nil && finalAna.Status == "completed",
			fmt.Sprintf("Analyze completes (status=%q)", safeStatus(finalAna)))
	}

	// Final report ────────────────────────────────────────────────
	section("Final report")
	printReport(projectID, changeID, pr.ID)

	if len(r.failures) == 0 {
		fmt.Println()
		fmt.Println(strings.Repeat("═", 72))
		fmt.Printf("  ✅  ALL SHAPE CHECKS PASSED  (elapsed: %s)\n", time.Since(r.startedAt).Round(time.Second))
		fmt.Println(strings.Repeat("═", 72))
		return
	}
	fmt.Println(strings.Repeat("═", 72))
	fmt.Printf("  ❌  %d check(s) FAILED (elapsed %s):\n",
		len(r.failures), time.Since(r.startedAt).Round(time.Second))
	for _, f := range r.failures {
		fmt.Printf("       - %s\n", f)
	}
	fmt.Println(strings.Repeat("═", 72))
	os.Exit(1)
}

func printReport(projectID, changeID, prID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fmt.Println("  🔧  Tool calls (chronological):")
	for _, tc := range r.toolCalls {
		fmt.Printf("     %-15s %-22s %s\n", shortID(tc.SessionID), tc.Tool, tc.Args)
	}

	fmt.Println()
	fmt.Println("  📡  Events by type:")
	evCount := map[string]int{}
	for _, e := range r.events {
		evCount[e.Type]++
	}
	keys := make([]string, 0, len(evCount))
	for k := range evCount {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("     %-22s  %d\n", k, evCount[k])
	}

	fmt.Println()
	fmt.Println("  🗄️  DB state:")
	cnt := func(m interface{}) int64 {
		var n int64
		model.DB.Model(m).Where("project_id = ?", projectID).Count(&n)
		return n
	}
	fmt.Printf("     agent_session:     %d\n", cnt(&model.AgentSession{}))
	fmt.Printf("     tool_call_trace:   %d\n", cnt(&model.ToolCallTrace{}))
	fmt.Printf("     dialogue_message:  %d\n", cnt(&model.DialogueMessage{}))
	fmt.Printf("     task:              %d\n", cnt(&model.Task{}))
	fmt.Printf("     milestone:         %d\n", cnt(&model.Milestone{}))
	fmt.Printf("     change:            %d\n", cnt(&model.Change{}))
	fmt.Printf("     pull_request:      %d\n", cnt(&model.PullRequest{}))
	fmt.Printf("     experience:        %d\n", cnt(&model.Experience{}))
	fmt.Printf("     skill_candidate:   %d\n", cnt(&model.SkillCandidate{}))

	fmt.Println()
	fmt.Println("  🗣️  Chief transcript (chronological):")
	var rows []model.DialogueMessage
	model.DB.Where("project_id = ? AND channel = ?", projectID, "chief").Order("created_at ASC").Find(&rows)
	for i, m := range rows {
		label := "👤 user"
		if m.Role == "assistant" {
			label = "🤖 chief"
		}
		body := m.Content
		if idx := strings.Index(body, "---"); idx > 0 && idx < 50 {
			body = strings.TrimSpace(body[idx+3:])
		}
		fmt.Printf("     [%d] %-10s  %s\n", i+1, label, truncate(body, 220))
	}

	fmt.Println()
	fmt.Println("  📊  Change + PR trajectory:")
	var ch model.Change
	model.DB.Where("id = ?", changeID).First(&ch)
	fmt.Printf("     change.status=%s  audit_level=%s  reviewed_at=%v\n",
		ch.Status, derefStr(ch.AuditLevel), ch.ReviewedAt)
	var pr model.PullRequest
	model.DB.Where("id = ?", prID).First(&pr)
	fmt.Printf("     pr.status=%s  tech_review_len=%d  biz_review_len=%d  version_suggestion=%s\n",
		pr.Status, len(pr.TechReview), len(pr.BizReview), pr.VersionSuggestion)
}

func safeStatus(s *agent.Session) string {
	if s == nil {
		return "<nil>"
	}
	return s.Status
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func derefStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
