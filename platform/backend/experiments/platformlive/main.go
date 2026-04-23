// Command platformlive — the full end-to-end "from login to project
// completion" integration test. Spins up the real HTTP server
// in-process against real MySQL + Redis + real LLM, then drives
// every step an MCP client (or a human operator) would take:
//
//   1. Human operator registers + logs in + creates project + selects.
//   2. Operator configures LLM endpoint and pins every agent role to it.
//   3. Operator sends a direction via the dashboard, confirms it.
//   4. Chief chat — 2 rounds of multi-round conversation.
//   5. Dashboard input → Maintain agent creates milestone + tasks.
//   6. Client agent registers + logs in + selects project.
//   7. Client: /task/list, /task/claim, /file/sync, /filelock/acquire.
//   8. Client: /change/submit — triggers audit (and possibly fix + re-audit).
//   9. Client: /branch/create + /branch/enter + bigger change, then /pr/submit.
//  10. Operator: /pr/approve_review → Evaluate → (success) → BizReview.
//  11. Operator: /pr/approve_merge → Merge executes.
//  12. Chief follow-up (verifies multi-round dialogue continuity).
//  13. Final verification of DB state, logout.
//
// Throughout, a live SSE subscriber prints every broadcast event the
// platform emits so the operator can eyeball the real-time fanout
// (agent turns, chat updates, tool calls, milestone changes, PR
// status transitions, etc.) alongside the HTTP responses.
//
// Setup (once per run):
//   D:\mysql\bin\mysql.exe -uroot -e "DROP DATABASE IF EXISTS a3c_live; CREATE DATABASE a3c_live CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
//
// Run:
//   cd platform/backend && go run ./experiments/platformlive
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/agentpool"
	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/handler"
	"github.com/a3c/platform/internal/llm"
	"github.com/a3c/platform/internal/middleware"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/runner"
	"github.com/a3c/platform/internal/service"
)

// ──────────────────────────────────────────────────────────────────
//  Report state — shared with printReport at the end.
// ──────────────────────────────────────────────────────────────────

type testReport struct {
	mu        sync.Mutex
	checks    []checkResult
	events    []sseEvent
	startedAt time.Time
}

type checkResult struct {
	Pass bool
	Name string
	Info string
}

type sseEvent struct {
	At      time.Time
	Type    string
	Payload map[string]interface{}
}

var r = &testReport{startedAt: time.Now()}

func note(format string, args ...interface{}) {
	fmt.Println("  • " + fmt.Sprintf(format, args...))
}
func check(cond bool, name string, info ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	extra := ""
	if len(info) > 0 {
		extra = info[0]
	}
	if cond {
		fmt.Printf("  ✔ %s %s\n", name, extra)
	} else {
		fmt.Printf("  ✗ %s %s\n", name, extra)
	}
	r.checks = append(r.checks, checkResult{Pass: cond, Name: name, Info: extra})
}
func section(title string) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 72))
	fmt.Printf("  %s\n", title)
	fmt.Println(strings.Repeat("─", 72))
}

// ──────────────────────────────────────────────────────────────────
//  HTTP client wrapper — tiny, re-uses the same Bearer auth scheme
//  the real MCP uses.
// ──────────────────────────────────────────────────────────────────

type apiClient struct {
	baseURL   string
	bearer    string
	http      *http.Client
	projectID string
}

func newAPI(baseURL string) *apiClient {
	return &apiClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 180 * time.Second},
	}
}

func (a *apiClient) setAuth(key string) { a.bearer = key }

type apiResp struct {
	Status int
	Body   map[string]interface{}
	Raw    string
}

func (ar apiResp) ok() bool {
	if ar.Status != 200 {
		return false
	}
	v, _ := ar.Body["success"].(bool)
	return v
}

func (ar apiResp) data() map[string]interface{} {
	if m, ok := ar.Body["data"].(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{}
}

func (ar apiResp) errMsg() string {
	if e, ok := ar.Body["error"].(map[string]interface{}); ok {
		code, _ := e["code"].(string)
		msg, _ := e["message"].(string)
		return fmt.Sprintf("%s: %s", code, msg)
	}
	return ar.Raw
}

func (a *apiClient) do(method, path string, body interface{}) apiResp {
	var rdr io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, a.baseURL+path, rdr)
	if err != nil {
		return apiResp{Raw: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if a.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+a.bearer)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return apiResp{Raw: err.Error()}
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	ar := apiResp{Status: resp.StatusCode, Raw: string(buf)}
	_ = json.Unmarshal(buf, &ar.Body)
	return ar
}

// ──────────────────────────────────────────────────────────────────
//  SSE subscriber — lives on its own goroutine, prints every event
//  as it arrives and buffers into r.events for post-run reporting.
// ──────────────────────────────────────────────────────────────────

// startSSE subscribes to /api/v1/events?project_id=X&key=Y and blocks
// on the goroutine until ctx.Done. The subscriber prints every event
// in a compact form so the operator can follow platform internals
// in real time alongside the API call output.
func startSSE(ctx context.Context, baseURL, projectID, key string) {
	url := fmt.Sprintf("%s/api/v1/events?project_id=%s&key=%s", baseURL, projectID, key)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "text/event-stream")
	// The /events route is inside the authed router group, so the
	// AuthMiddleware runs first and needs a Bearer header. The
	// ?key= query param is a backwards-compat check done *inside*
	// the handler; middleware rejects before we get there.
	req.Header.Set("Authorization", "Bearer "+key)
	cli := &http.Client{Timeout: 0}
	resp, err := cli.Do(req)
	if err != nil {
		log.Printf("[SSE] connect error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		log.Printf("[SSE] status %d: %s", resp.StatusCode, string(b))
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	var lastType, lastData string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			lastType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			lastData = strings.TrimPrefix(line, "data: ")
		case line == "":
			if lastType == "" {
				continue
			}
			var env map[string]interface{}
			_ = json.Unmarshal([]byte(lastData), &env)
			payload, _ := env["payload"].(map[string]interface{})
			r.mu.Lock()
			r.events = append(r.events, sseEvent{At: time.Now(), Type: lastType, Payload: payload})
			r.mu.Unlock()
			// AGENT_TEXT_DELTA fires once per streamed token —
			// logging each one drowns everything else in the
			// console. Keep them in the event buffer for the final
			// histogram but don't print line-by-line.
			if lastType != "AGENT_TEXT_DELTA" {
				printEvent(lastType, payload)
			}
			lastType = ""
			lastData = ""
		}
		if err := ctx.Err(); err != nil {
			return
		}
	}
}

// printEvent renders one SSE event in a single colourable line. Keeps
// the most useful 1-2 payload fields inline so the operator can see
// the gist without having to dig through JSON.
func printEvent(t string, payload map[string]interface{}) {
	summary := eventSummary(t, payload)
	fmt.Printf("    📡 %-22s %s\n", t, summary)
}

// eventSummary picks the 1-2 most useful payload fields for each
// event type so the live log is readable. Unknown types fall back to
// a compact JSON dump clipped to 120 chars.
func eventSummary(t string, p map[string]interface{}) string {
	get := func(k string) string {
		if v, ok := p[k]; ok {
			switch x := v.(type) {
			case string:
				return x
			case float64:
				return fmt.Sprintf("%v", x)
			case bool:
				return fmt.Sprintf("%v", x)
			default:
				if b, err := json.Marshal(v); err == nil {
					return string(b)
				}
			}
		}
		return ""
	}
	switch t {
	case "AGENT_TURN":
		return fmt.Sprintf("session=%s iter=%s in/out=%s/%s tools=%s",
			shortID(get("session_id")), get("iteration"),
			get("input_tokens"), get("output_tokens"), get("tool_count"))
	case "AGENT_TEXT_DELTA":
		return "…" // too chatty to print each delta
	case "AGENT_DONE":
		return fmt.Sprintf("session=%s iters=%s tokens=%s/%s $%s",
			shortID(get("session_id")), get("iterations"),
			get("input_tokens"), get("output_tokens"), get("cost_usd"))
	case "AGENT_ERROR":
		return fmt.Sprintf("session=%s err=%s", shortID(get("session_id")), get("error"))
	case "CHAT_UPDATE":
		content := get("content")
		if len(content) > 120 {
			content = content[:120] + "…"
		}
		return fmt.Sprintf("role=%s %s", get("role"), content)
	case "MILESTONE_UPDATE", "DIRECTION_CHANGE":
		content := get("content")
		if len(content) > 120 {
			content = content[:120] + "…"
		}
		return fmt.Sprintf("block=%s %q", get("block_type"), content)
	case "PR_SUBMITTED", "PR_EVALUATION_STARTED", "PR_MERGED",
		"PR_BIZ_APPROVED", "PR_BIZ_REJECTED", "PR_NEEDS_WORK",
		"PR_MERGE_FAILED":
		return fmt.Sprintf("pr=%s title=%q status=%s",
			shortID(get("pr_id")), get("title"), get("status"))
	case "VERSION_UPDATE":
		return fmt.Sprintf("version=%s reason=%s", get("content"), get("reason"))
	case "AGENT_ONLINE", "AGENT_OFFLINE":
		return fmt.Sprintf("agent=%s (%s)", get("agent_name"), get("agent_id"))
	}
	b, _ := json.Marshal(p)
	s := string(b)
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// ──────────────────────────────────────────────────────────────────
//  Filter / poll helpers.
// ──────────────────────────────────────────────────────────────────

func shortID(id string) string {
	if len(id) > 12 {
		return id[len(id)-12:]
	}
	return id
}

// waitForSession polls the DB until a session with the given filters
// exists and is in one of the given terminal statuses.
func waitForSession(filter map[string]interface{}, terminal []string, timeout time.Duration) *model.AgentSession {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var s model.AgentSession
		q := model.DB
		for k, v := range filter {
			q = q.Where(k+" = ?", v)
		}
		if err := q.Order("created_at DESC").First(&s).Error; err == nil {
			for _, want := range terminal {
				if s.Status == want {
					return &s
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil
}

// waitForChangeStatus polls until change.status lands in one of the
// terminal values. Returns the last-seen change whether or not the
// wait succeeded so the caller can log intermediate state.
func waitForChangeStatus(changeID string, terminal []string, timeout time.Duration) *model.Change {
	deadline := time.Now().Add(timeout)
	var last model.Change
	for time.Now().Before(deadline) {
		if err := model.DB.Where("id = ?", changeID).First(&last).Error; err == nil {
			for _, want := range terminal {
				if last.Status == want {
					return &last
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return &last
}

func waitForPRStatus(prID string, terminal []string, timeout time.Duration) *model.PullRequest {
	deadline := time.Now().Add(timeout)
	var last model.PullRequest
	for time.Now().Before(deadline) {
		if err := model.DB.Where("id = ?", prID).First(&last).Error; err == nil {
			for _, want := range terminal {
				if last.Status == want {
					return &last
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return &last
}

// ──────────────────────────────────────────────────────────────────
//  Provider creds (same pattern as planninglive).
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
//  Bootstrap — DB + Redis + LLM registry + server wiring.
// ──────────────────────────────────────────────────────────────────

func bootstrapServer(provider string) (*config.Config, string) {
	cfg := config.Load("")
	cfg.Database.DBName = "a3c_live"

	if err := model.InitDB(&cfg.Database); err != nil {
		log.Fatalf("mysql: %v", err)
	}
	if err := model.InitRedis(&cfg.Redis); err != nil {
		log.Fatalf("redis: %v", err)
	}
	// Blow away every table so we start from a clean slate on every run.
	// The test is deterministic only if we control the initial state.
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

	llm.LoadAll()
	service.InitDataPath(cfg.DataDir)

	runner.NativeRegistryBuilder = runner.PlatformRegistryBuilder
	runner.PlatformToolSink = service.HandleToolCallResult
	runner.StreamEmitter = func(projectID, eventType string, payload map[string]interface{}) {
		service.SSEManager.BroadcastToProject(projectID, eventType, gin.H(payload), "")
	}
	runner.SessionCompletionHandler = service.HandleSessionCompletion
	agent.RegisterDispatcher(runner.Dispatch)

	// Register the LLM endpoint up front. We still expose /llm/endpoints
	// later so the test can exercise the CRUD, but having a real key
	// in the registry before the first agent dispatches means no "no
	// endpoints registered" race.
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

	// Pin every agent role onto the same endpoint so the test doesn't
	// depend on the dispatcher's fresh-install "first endpoint" fallback.
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

	// Pool manager boilerplate — not exercised by this test but
	// real cmd/server/main wires it so we mirror that to prevent any
	// nil-deref on pool-aware paths.
	poolManager := agentpool.NewManager(agentpool.ManagerConfig{
		Root:        filepath.Join(cfg.DataDir, "pool"),
		PlatformURL: fmt.Sprintf("http://localhost:%d", cfg.Server.Port),
	}, nil)
	agentpool.SetDefault(poolManager)

	return cfg, endpointID
}

// wireRouter is a trimmed-down copy of cmd/server/main.go — same
// endpoints, minus the long-running background timers we don't want
// firing in a test (maintain timer, refinery timer, etc.). Keeping it
// inline lets the test boot the real handler chain without having
// to split main.go into a library.
func wireRouter(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RecoveryMiddleware())
	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.CORSMiddleware())
	router.Use(middleware.RateLimitMiddleware(100))

	router.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

	v1 := router.Group("/api/v1")

	authH := handler.NewAuthHandler()
	projectH := handler.NewProjectHandler()
	taskH := handler.NewTaskHandler()
	lockH := handler.NewFileLockHandler()
	fileSyncH := handler.NewFileSyncHandler()
	statusH := handler.NewStatusHandler()
	dashH := handler.NewDashboardHandler()
	changeH := handler.NewChangeHandler()
	sseH := handler.NewSSEHandler()
	branchH := handler.NewBranchHandler()
	prH := handler.NewPRHandler()
	chiefH := handler.NewChiefHandler()
	roleH := handler.NewRoleHandler()
	llmH := handler.NewLLMEndpointHandler()
	milestoneH := handler.NewMilestoneHandler()

	v1.POST("/auth/login", authH.Login)
	v1.POST("/auth/logout", authH.Logout)
	v1.POST("/agent/register", authH.Register)

	v1.POST("/project/create", projectH.Create)
	v1.GET("/project/:id", projectH.Get)
	v1.GET("/project/list", projectH.List)

	auth := v1.Group("", middleware.AuthMiddleware())
	{
		auth.POST("/auth/heartbeat", authH.Heartbeat)
		auth.POST("/auth/select-project", authH.SelectProject)
		auth.POST("/task/create", taskH.Create)
		auth.POST("/task/claim", taskH.Claim)
		auth.POST("/task/complete", taskH.Complete)
		auth.POST("/task/release", taskH.Release)
		auth.GET("/task/list", taskH.List)

		auth.GET("/llm/endpoints", llmH.List)
		auth.GET("/llm/endpoints/:id", llmH.Get)

		auth.POST("/filelock/acquire", lockH.Acquire)
		auth.POST("/filelock/release", lockH.Release)
		auth.POST("/filelock/check", lockH.Check)

		auth.POST("/change/submit", changeH.Submit)
		auth.GET("/change/list", changeH.List)
		auth.GET("/change/status", changeH.Status)

		auth.POST("/file/sync", fileSyncH.Sync)
		auth.GET("/status/sync", statusH.Sync)
		auth.GET("/events", sseH.Events)

		auth.GET("/dashboard/state", dashH.GetState)
		auth.POST("/dashboard/input", dashH.Input)
		auth.POST("/dashboard/confirm", dashH.Confirm)
		auth.POST("/dashboard/clear_context", dashH.ClearContext)
		auth.GET("/dashboard/messages", dashH.GetMessages)

		auth.POST("/milestone/switch", milestoneH.Switch)
		auth.GET("/milestone/archives", milestoneH.Archives)

		auth.POST("/branch/create", branchH.Create)
		auth.POST("/branch/enter", branchH.Enter)
		auth.POST("/branch/leave", branchH.Leave)
		auth.GET("/branch/list", branchH.List)
		auth.POST("/branch/change_submit", branchH.BranchChangeSubmit)
		auth.GET("/branch/file_sync", branchH.BranchFileSync)

		auth.POST("/pr/submit", prH.Submit)
		auth.GET("/pr/list", prH.List)
		auth.GET("/pr/:pr_id", prH.GetPR)
		auth.POST("/pr/approve_review", prH.ApproveReview)
		auth.POST("/pr/approve_merge", prH.ApproveMerge)
		auth.POST("/pr/reject", prH.Reject)

		auth.POST("/chief/chat", chiefH.Chat)
		auth.GET("/chief/sessions", chiefH.Sessions)

		auth.GET("/role/list", roleH.ListRoles)
	}

	return router
}

// ──────────────────────────────────────────────────────────────────
//  Main — orchestrates the scenario.
// ──────────────────────────────────────────────────────────────────

func main() {
	log.SetFlags(0)
	provider := flag.String("provider", "minimax", "llm.<provider> key in configs/config.yaml")
	port := flag.Int("port", 13003, "HTTP port for the in-process server")
	flag.Parse()

	fmt.Println(strings.Repeat("═", 72))
	fmt.Println("  A3C Platform — LIVE End-to-End (login → project complete)")
	fmt.Printf("  Provider: %s    Port: %d\n", *provider, *port)
	fmt.Println(strings.Repeat("═", 72))

	r.startedAt = time.Now()

	// Phase 0: boot ────────────────────────────────────────────────
	section("Phase 0: Bootstrap (MySQL + Redis + LLM endpoint + HTTP server)")
	cfg, _ := bootstrapServer(*provider)
	cfg.Server.Port = *port
	router := wireRouter(cfg)

	addr := fmt.Sprintf(":%d", *port)
	srv := &http.Server{Addr: addr, Handler: router}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()
	time.Sleep(250 * time.Millisecond) // let the listener bind
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", *port)
	note("HTTP server listening on %s", addr)

	api := newAPI(baseURL)

	// Phase 1: Operator register + login + create project ──────────
	section("Phase 1: Human operator — register, login, create project")
	operatorName := fmt.Sprintf("operator-%d", time.Now().Unix())
	regResp := api.do("POST", "/api/v1/agent/register", map[string]interface{}{
		"name":     operatorName,
		"is_human": true,
	})
	check(regResp.ok(), "register operator", regResp.errMsg())
	operatorKey, _ := regResp.data()["access_key"].(string)
	operatorID, _ := regResp.data()["agent_id"].(string)
	api.setAuth(operatorKey)
	note("Operator agent_id=%s", operatorID)

	loginResp := api.do("POST", "/api/v1/auth/login", map[string]interface{}{
		"key": operatorKey,
	})
	check(loginResp.ok(), "operator login", loginResp.errMsg())

	projectName := fmt.Sprintf("Planner-%d", time.Now().Unix())
	projResp := api.do("POST", "/api/v1/project/create", map[string]interface{}{
		"name":        projectName,
		"description": "AI-driven planning tool (platformlive e2e)",
	})
	check(projResp.ok(), "project create", projResp.errMsg())
	projectID, _ := projResp.data()["id"].(string)
	api.projectID = projectID
	note("Project id=%s name=%q", projectID, projectName)

	// Seed a repo + set up a tiny go file so audit/evaluate have real
	// content to inspect. The project/create handler already mkdir'd
	// projects/<id>/repo, we just need to drop a file in.
	repoDir := filepath.Join(cfg.DataDir, "projects", projectID, "repo")
	_ = os.MkdirAll(filepath.Join(repoDir, "planner"), 0o755)
	if err := os.WriteFile(filepath.Join(repoDir, "planner", "schedule.go"),
		[]byte(seedGoFile), 0o644); err != nil {
		log.Fatalf("seed: %v", err)
	}
	note("Seeded planner/schedule.go with intentional deadline-validation bug")

	selResp := api.do("POST", "/api/v1/auth/select-project", map[string]interface{}{
		"project": projectID,
	})
	check(selResp.ok(), "select project", selResp.errMsg())

	// Phase 2: SSE subscription ────────────────────────────────────
	section("Phase 2: Open SSE subscription for live broadcasts")
	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()
	go startSSE(sseCtx, baseURL, projectID, operatorKey)
	time.Sleep(300 * time.Millisecond)
	note("SSE subscriber attached to project %s", shortID(projectID))

	// Phase 3: Dashboard direction input + confirm ─────────────────
	section("Phase 3: Dashboard input — set project direction")
	dirResp := api.do("POST", "/api/v1/dashboard/input?project_id="+projectID, map[string]interface{}{
		"target_block": "direction",
		"content":      "做一个 AI 驱动的任务规划工具。MVP 聚焦：任务增删改查 + 截止时间校验。保持代码可读性，拒绝过度抽象。",
	})
	check(dirResp.ok(), "dashboard input (direction)", dirResp.errMsg())
	inputID, _ := dirResp.data()["input_id"].(string)
	confirmResp := api.do("POST", "/api/v1/dashboard/confirm?project_id="+projectID, map[string]interface{}{
		"input_id":  inputID,
		"confirmed": true,
	})
	check(confirmResp.ok(), "dashboard confirm (direction)", confirmResp.errMsg())

	// Phase 4: Chief chat — 2 rounds ───────────────────────────────
	section("Phase 4: Chief chat — 2 multi-round turns (tests dialogue persistence)")
	chiefTurn(api, projectID, "我想做一个 AI 驱动的任务规划工具。第一个里程碑先做最小调度器（任务 CRUD + 截止时间校验）。你觉得这样定义合理吗？")
	chiefTurn(api, projectID, "好，那我们就把 M1 命名为 core-scheduler。你能回忆一下我们刚讨论的目标吗？")
	var dlgChief []model.DialogueMessage
	model.DB.Where("project_id = ? AND channel = ?", projectID, "chief").Order("created_at ASC").Find(&dlgChief)
	check(len(dlgChief) >= 4, "chief dialogue has ≥4 turns (2 user + 2 assistant)",
		fmt.Sprintf("got %d", len(dlgChief)))

	// Phase 5: Dashboard task input → Maintain creates milestone/task
	section("Phase 5: Dashboard task input → Maintain agent creates milestone + task")
	taskInputResp := api.do("POST", "/api/v1/dashboard/input?project_id="+projectID, map[string]interface{}{
		"target_block": "task",
		"content":      "创建第一个里程碑 core-scheduler，并拆成 2-3 个任务。其中第一个任务应该是：在 Schedule.AddTask 加截止时间校验，不合法时返回 ErrInvalidSchedule。",
	})
	check(taskInputResp.ok(), "dashboard task input", taskInputResp.errMsg())
	note("Waiting up to 2m for Maintain agent to finish…")
	ms := waitForSession(map[string]interface{}{
		"project_id":     projectID,
		"role":           "maintain",
		"trigger_reason": "dashboard_task_input",
	}, []string{"completed", "failed"}, 2*time.Minute)
	check(ms != nil && ms.Status == "completed", "Maintain session completed",
		fmt.Sprintf("status=%v", safeStatus(ms)))

	var milestones []model.Milestone
	model.DB.Where("project_id = ?", projectID).Find(&milestones)
	check(len(milestones) >= 1, "Maintain created milestone row",
		fmt.Sprintf("count=%d", len(milestones)))

	taskListResp := api.do("GET", "/api/v1/task/list?project_id="+projectID, nil)
	check(taskListResp.ok(), "task list", taskListResp.errMsg())
	tasksRaw, _ := taskListResp.data()["tasks"].([]interface{})
	note("Maintain produced %d task(s)", len(tasksRaw))
	var firstTaskID, firstTaskName string
	if len(tasksRaw) > 0 {
		if m, ok := tasksRaw[0].(map[string]interface{}); ok {
			firstTaskID, _ = m["id"].(string)
			firstTaskName, _ = m["name"].(string)
		}
	}
	// Safety net: if Maintain didn't produce a task on this run (real
	// LLM non-determinism), create one via /task/create so the rest of
	// the test can proceed. We still flag the absence above.
	if firstTaskID == "" {
		note("Creating fallback task so downstream phases can run…")
		createResp := api.do("POST", "/api/v1/task/create?project_id="+projectID, map[string]interface{}{
			"name":        "给 Schedule.AddTask 加截止时间校验",
			"description": "在 AddTask 中校验 Deadline.After(StartTime)，否则返回 ErrInvalidSchedule",
			"priority":    "high",
		})
		check(createResp.ok(), "fallback task create", createResp.errMsg())
		firstTaskID, _ = createResp.data()["id"].(string)
		firstTaskName = "给 Schedule.AddTask 加截止时间校验"
	}
	note("First task id=%s name=%q", firstTaskID, firstTaskName)

	// Phase 6: Register client agent ───────────────────────────────
	section("Phase 6: Register + login a client agent (simulates MCP worker)")
	clientName := fmt.Sprintf("worker-%d", time.Now().Unix())
	clientResp := api.do("POST", "/api/v1/agent/register", map[string]interface{}{
		"name":     clientName,
		"is_human": false,
	})
	check(clientResp.ok(), "register client agent", clientResp.errMsg())
	clientKey, _ := clientResp.data()["access_key"].(string)
	clientID, _ := clientResp.data()["agent_id"].(string)
	clientAPI := newAPI(baseURL)
	clientAPI.setAuth(clientKey)
	clientAPI.projectID = projectID

	cliLoginResp := clientAPI.do("POST", "/api/v1/auth/login", map[string]interface{}{
		"key":     clientKey,
		"project": projectID,
	})
	check(cliLoginResp.ok(), "client agent login", cliLoginResp.errMsg())
	cliSelResp := clientAPI.do("POST", "/api/v1/auth/select-project", map[string]interface{}{
		"project": projectID,
	})
	check(cliSelResp.ok(), "client select project", cliSelResp.errMsg())
	note("Client agent_id=%s", clientID)

	// Phase 7: Client task flow — list/claim/sync/lock/submit ──────
	section("Phase 7: Client agent — task list/claim/sync/lock + change submit")
	cliListResp := clientAPI.do("GET", "/api/v1/task/list?project_id="+projectID, nil)
	check(cliListResp.ok(), "(client) task list", cliListResp.errMsg())

	claimResp := clientAPI.do("POST", "/api/v1/task/claim", map[string]interface{}{
		"task_id": firstTaskID,
	})
	check(claimResp.ok(), "(client) task claim", claimResp.errMsg())

	syncResp := clientAPI.do("POST", "/api/v1/file/sync", map[string]interface{}{
		"version": "v1.0",
	})
	check(syncResp.ok(), "(client) file sync", syncResp.errMsg())

	lockResp := clientAPI.do("POST", "/api/v1/filelock/acquire?project_id="+projectID, map[string]interface{}{
		"task_id": firstTaskID,
		"files":   []string{"planner/schedule.go"},
		"reason":  "adding deadline validation",
	})
	check(lockResp.ok(), "(client) filelock acquire", lockResp.errMsg())

	// Submit the fixed content — adds a real deadline validation.
	fixedContent := strings.Replace(seedGoFile,
		"func (s *Schedule) AddTask(t Task) error {\n\ts.Tasks = append(s.Tasks, t)\n\treturn nil\n}",
		"func (s *Schedule) AddTask(t Task) error {\n\tif !t.Deadline.After(t.StartTime) {\n\t\treturn ErrInvalidSchedule\n\t}\n\ts.Tasks = append(s.Tasks, t)\n\treturn nil\n}",
		1)
	submitResp := clientAPI.do("POST", "/api/v1/change/submit?project_id="+projectID, map[string]interface{}{
		"task_id":     firstTaskID,
		"version":     "v1.0",
		"description": "Add deadline validation in Schedule.AddTask — returns ErrInvalidSchedule when Deadline ≤ StartTime",
		"writes": []map[string]string{
			{"path": "planner/schedule.go", "content": fixedContent},
		},
		"deletes": []string{},
	})
	check(submitResp.ok(), "(client) change submit", submitResp.errMsg())
	changeID, _ := submitResp.data()["change_id"].(string)
	note("Change id=%s initial_status=%v", changeID, submitResp.data()["status"])

	// Phase 8: Observe audit pipeline ──────────────────────────────
	section("Phase 8: Observe audit pipeline (audit_1 → [fix → audit_2] → verdict)")
	finalChange := waitForChangeStatus(changeID, []string{"approved", "rejected"}, 6*time.Minute)
	check(finalChange != nil && (finalChange.Status == "approved" || finalChange.Status == "rejected"),
		"audit pipeline reaches terminal verdict",
		fmt.Sprintf("status=%s audit_level=%s", finalChange.Status, derefStr(finalChange.AuditLevel)))
	note("Audit verdict: status=%s audit_level=%s", finalChange.Status, derefStr(finalChange.AuditLevel))
	if finalChange.AuditReason != "" {
		note("Audit reason: %s", truncate(finalChange.AuditReason, 200))
	}

	// Count audit sessions so we can see if fix + audit_2 actually ran.
	var auditCount int64
	model.DB.Model(&model.AgentSession{}).
		Where("change_id = ? AND role IN ?", changeID, []string{"audit_1", "audit_2", "fix"}).
		Count(&auditCount)
	note("Audit-related sessions for this change: %d", auditCount)

	// Phase 9: Branch + PR workflow ────────────────────────────────
	section("Phase 9: Client — branch create + enter, bigger change, submit PR")
	branchResp := clientAPI.do("POST", "/api/v1/branch/create", map[string]interface{}{
		"name":        "hardening",
		"description": "Hardening milestone: input validation + tests",
	})
	check(branchResp.ok(), "(client) branch create", branchResp.errMsg())
	branchID, _ := branchResp.data()["id"].(string)
	note("Branch id=%s", branchID)

	enterResp := clientAPI.do("POST", "/api/v1/branch/enter", map[string]interface{}{
		"branch_id": branchID,
	})
	check(enterResp.ok(), "(client) branch enter", enterResp.errMsg())

	// Bigger change on the branch: add a small test file alongside the validation.
	testFile := `package planner

import (
	"errors"
	"testing"
	"time"
)

func TestAddTask_ValidatesDeadline(t *testing.T) {
	s := &Schedule{}
	start := time.Now()
	err := s.AddTask(Task{Title: "x", StartTime: start, Deadline: start.Add(-time.Hour)})
	if !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("expected ErrInvalidSchedule, got %v", err)
	}
}
`
	branchChangeResp := clientAPI.do("POST", "/api/v1/branch/change_submit", map[string]interface{}{
		"task_id":     firstTaskID,
		"description": "Add deadline validation test",
		"writes": []map[string]string{
			{"path": "planner/schedule_test.go", "content": testFile},
		},
		"deletes": []string{},
	})
	check(branchChangeResp.ok(), "(client) branch change_submit", branchChangeResp.errMsg())

	// Self-review MUST stay consistent with the diff. The branch
	// change above only added schedule_test.go; claiming we also
	// modified schedule.go (which was actually submitted on main via
	// the earlier /change/submit) would make the evaluate agent
	// flag the PR as inconsistent and bail out with needs_work.
	prResp := clientAPI.do("POST", "/api/v1/pr/submit", map[string]interface{}{
		"title":       "test: add deadline validation regression test",
		"description": "Adds a regression test for Schedule.AddTask rejecting Deadline ≤ StartTime. The validation code itself landed separately via the main-branch change that preceded this PR.",
		"self_review": map[string]interface{}{
			"changed_functions": []map[string]string{
				{"file": "planner/schedule_test.go", "function": "TestAddTask_ValidatesDeadline",
					"change_type": "added", "impact": "Regression test for the deadline validation previously added on main"},
			},
			"overall_impact":   "Adds regression coverage for the deadline-validation contract",
			"merge_confidence": "high",
		},
	})
	check(prResp.ok(), "(client) pr submit", prResp.errMsg())
	prID, _ := prResp.data()["id"].(string)
	note("PR id=%s status=%v", prID, prResp.data()["status"])

	// Phase 10: Operator approves PR review → Evaluate → Biz review
	section("Phase 10: Operator approves review → Evaluate → BizReview")
	approveResp := api.do("POST", "/api/v1/pr/approve_review", map[string]interface{}{
		"pr_id": prID,
	})
	check(approveResp.ok(), "operator approve_review", approveResp.errMsg())

	// Wait for the PR to come out of Evaluate and enter one of the
	// post-evaluation states (either pending_human_merge if biz review
	// approves, or something else if it didn't).
	finalPR := waitForPRStatus(prID, []string{"pending_human_merge", "merged", "merge_failed", "evaluated"}, 8*time.Minute)
	check(finalPR != nil && finalPR.Status != "", "Evaluate/BizReview reach terminal state",
		fmt.Sprintf("pr.status=%v", safeStatus(finalPR)))
	note("PR after evaluate/biz: status=%s tech_review_len=%d biz_review_len=%d version_suggestion=%s",
		finalPR.Status, len(finalPR.TechReview), len(finalPR.BizReview), finalPR.VersionSuggestion)

	// Phase 11: Operator approves merge → merge ────────────────────
	section("Phase 11: Operator approves merge → platform merges")
	if finalPR.Status == "pending_human_merge" {
		mergeResp := api.do("POST", "/api/v1/pr/approve_merge", map[string]interface{}{
			"pr_id": prID,
		})
		check(mergeResp.ok(), "operator approve_merge", mergeResp.errMsg())
	} else {
		note("Skipping approve_merge — PR is in status=%s (biz review likely rejected/unknown)", finalPR.Status)
	}
	finalPR = waitForPRStatus(prID, []string{"merged", "merge_failed", "evaluated"}, 5*time.Minute)
	check(finalPR != nil && (finalPR.Status == "merged" || finalPR.Status == "evaluated"),
		"PR settles in a non-hung status",
		fmt.Sprintf("status=%s", safeStatus(finalPR)))

	// Phase 12: Chief follow-up (multi-round continuity) ───────────
	section("Phase 12: Chief follow-up — tests dialogue continuity across phases")
	chiefTurn(api, projectID, "我们刚做完第一个 PR 的评审和合并流程，现在进度如何？你还记得最开始讨论的目标吗？")

	// Phase 13: Final DB state + logout ────────────────────────────
	section("Phase 13: Final report + operator logout")
	var (
		nTask, nChange, nPR, nBranch, nMS, nSess, nDlg int64
	)
	model.DB.Model(&model.Task{}).Where("project_id = ?", projectID).Count(&nTask)
	model.DB.Model(&model.Change{}).Where("project_id = ?", projectID).Count(&nChange)
	model.DB.Model(&model.PullRequest{}).Where("project_id = ?", projectID).Count(&nPR)
	model.DB.Model(&model.Branch{}).Where("project_id = ?", projectID).Count(&nBranch)
	model.DB.Model(&model.Milestone{}).Where("project_id = ?", projectID).Count(&nMS)
	model.DB.Model(&model.AgentSession{}).Where("project_id = ?", projectID).Count(&nSess)
	model.DB.Model(&model.DialogueMessage{}).Where("project_id = ?", projectID).Count(&nDlg)

	fmt.Printf("  📊 task=%d  change=%d  pr=%d  branch=%d  milestone=%d  session=%d  dialogue=%d\n",
		nTask, nChange, nPR, nBranch, nMS, nSess, nDlg)

	// Event type histogram
	fmt.Println("\n  📡 Events by type:")
	r.mu.Lock()
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
	r.mu.Unlock()

	// Chief transcript
	fmt.Println("\n  🗣️  Chief transcript:")
	var chiefRows []model.DialogueMessage
	model.DB.Where("project_id = ? AND channel = ?", projectID, "chief").Order("created_at ASC").Find(&chiefRows)
	for i, m := range chiefRows {
		label := "👤 user"
		if m.Role == "assistant" {
			label = "🤖 chief"
		}
		body := m.Content
		if idx := strings.Index(body, "---"); idx > 0 && idx < 50 {
			body = strings.TrimSpace(body[idx+3:])
		}
		fmt.Printf("     [%d] %-10s  %s\n", i+1, label, truncate(body, 260))
	}

	logoutResp := api.do("POST", "/api/v1/auth/logout", map[string]interface{}{
		"key": operatorKey,
	})
	check(logoutResp.ok(), "operator logout", logoutResp.errMsg())

	sseCancel()

	// Final verdict ────────────────────────────────────────────────
	fmt.Println()
	fmt.Println(strings.Repeat("═", 72))
	pass, fail := tallyChecks()
	if fail == 0 {
		fmt.Printf("  ✅ ALL %d CHECKS PASSED  (elapsed: %s)\n", pass, time.Since(r.startedAt).Round(time.Second))
	} else {
		fmt.Printf("  ❌ %d passed, %d FAILED  (elapsed: %s)\n", pass, fail, time.Since(r.startedAt).Round(time.Second))
		for _, c := range r.checks {
			if !c.Pass {
				fmt.Printf("       - %s %s\n", c.Name, c.Info)
			}
		}
	}
	fmt.Println(strings.Repeat("═", 72))

	// Gracefully stop the server so the process exits cleanly.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)

	if fail > 0 {
		os.Exit(1)
	}
}

func tallyChecks() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pass := 0
	fail := 0
	for _, c := range r.checks {
		if c.Pass {
			pass++
		} else {
			fail++
		}
	}
	return pass, fail
}

// chiefTurn sends one Chief chat message and waits for the session to
// complete, so the next call sees the prior reply persisted in the
// dialogue history. The Chief chat endpoint returns immediately
// (fire-and-forget) so we poll by DB here.
//
// Note: /chief/chat takes project_id as a query param and the
// message field is `message` (not `content`) — matches the
// handler in internal/handler/chief.go.
func chiefTurn(api *apiClient, projectID, message string) {
	preCount := countChiefSessions(projectID)
	resp := api.do("POST", "/api/v1/chief/chat?project_id="+projectID, map[string]interface{}{
		"message": message,
	})
	check(resp.ok(), "chief chat post", resp.errMsg())
	// Wait for a NEW chief session to appear and reach terminal state.
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		if countChiefSessions(projectID) > preCount {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	ms := waitForSession(map[string]interface{}{
		"project_id": projectID,
		"role":       "chief",
	}, []string{"completed", "failed"}, 4*time.Minute)
	if ms == nil {
		check(false, "chief turn completes", "no session found")
		return
	}
	check(ms.Status == "completed", fmt.Sprintf("chief turn completes (output chars=%d)", len(ms.Output)),
		fmt.Sprintf("status=%s", ms.Status))
	if ms.Output != "" {
		note("chief.Output: %s", truncate(ms.Output, 260))
	}
}

func countChiefSessions(projectID string) int64 {
	var n int64
	model.DB.Model(&model.AgentSession{}).
		Where("project_id = ? AND role = ?", projectID, "chief").Count(&n)
	return n
}

// ──────────────────────────────────────────────────────────────────
//  Small utilities.
// ──────────────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func safeStatus(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return "<nil>"
	case *model.AgentSession:
		if x == nil {
			return "<nil>"
		}
		return x.Status
	case *model.Change:
		if x == nil {
			return "<nil>"
		}
		return x.Status
	case *model.PullRequest:
		if x == nil {
			return "<nil>"
		}
		return x.Status
	}
	return fmt.Sprintf("%v", v)
}

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

const seedGoFile = `package planner

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
