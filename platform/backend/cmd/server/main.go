package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/agentpool"
	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/handler"
	"github.com/a3c/platform/internal/llm"
	"github.com/a3c/platform/internal/middleware"
	"github.com/a3c/platform/internal/model"
	opencodepkg "github.com/a3c/platform/internal/opencode"
	"github.com/a3c/platform/internal/runner"
	"github.com/a3c/platform/internal/service"
)

func main() {
	cfg := config.Load("")
	if err := cfg.Validate(); err != nil {
		// Fail-fast on config typos. Without this, the platform would
		// crash much later with an opaque "DSN parse failed" or "redis:
		// can't dial localhost:0" message far from the actual cause.
		log.Fatalf("[Config] validation failed: %v", err)
	}
	cfg.LogEffective()

	if err := model.InitDB(&cfg.Database); err != nil {
		log.Fatalf("Database init failed: %v", err)
	}

	if err := model.InitRedis(&cfg.Redis); err != nil {
		log.Fatalf("Redis init failed: %v", err)
	}

	// LLM endpoint registry — load every user-registered endpoint
	// from the DB before any agent dispatches, so the runtime has
	// models available as soon as it comes up. Empty registry is
	// fine; handler routes let operators register on the fly.
	llm.LoadAll()

	service.InitDataPath(cfg.DataDir)

	// Register the single native-runner dispatcher. Every platform
	// agent role (audit / fix / evaluate / merge / maintain / chief /
	// analyze / consult / assess) routes through this path; the
	// legacy opencode scheduler has been removed.
	runner.NativeRegistryBuilder = runner.PlatformRegistryBuilder
	runner.PlatformToolSink = service.HandleToolCallResult
	runner.StreamEmitter = func(projectID, eventType string, payload map[string]interface{}) {
		service.SSEManager.BroadcastToProject(projectID, eventType, gin.H(payload), "")
	}
	runner.SessionCompletionHandler = service.HandleSessionCompletion
	agent.RegisterDispatcher(runner.Dispatch)

	// Observer-side effects for dispatch failures. Before this hook,
	// a broken dispatch (no LLM endpoints, provider rejects key, etc.)
	// only hit stderr and left the session stuck in pending, with no
	// signal reaching the dashboard. service.HandleDispatchFailure
	// broadcasts AGENT_ERROR over SSE and, for Chief/Maintain chat
	// sessions, appends a system-role row to dialogue history so the
	// failure shows up inline in the chat tab.
	agent.RegisterFailureHook(service.HandleDispatchFailure)

	gin.SetMode(cfg.Server.Mode)
	r := gin.New()

	r.Use(middleware.RecoveryMiddleware())
	r.Use(middleware.RequestIDMiddleware())
	r.Use(middleware.CORSMiddleware())
	r.Use(middleware.RateLimitMiddleware(100))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// /metrics is open (no auth) by Prometheus convention. Operator
	// firewall is the gate. Only exposes operational counters/gauges
	// (no PII, no per-project user data) — see metricsHandler.Prometheus.
	r.GET("/metrics", handler.NewMetricsHandler().Prometheus)

	v1 := r.Group("/api/v1")

	authHandler := handler.NewAuthHandler()
	projectHandler := handler.NewProjectHandler()
	taskHandler := handler.NewTaskHandler()
	tagHandler := handler.NewTagHandler()
	metricsHandler := handler.NewMetricsHandler()
	llmEndpointHandler := handler.NewLLMEndpointHandler()
	lockHandler := handler.NewFileLockHandler()
	statusHandler := handler.NewStatusHandler()
	dashboardHandler := handler.NewDashboardHandler()
	changeHandler := handler.NewChangeHandler()
	fileSyncHandler := handler.NewFileSyncHandler()
	sseHandler := handler.NewSSEHandler()
	agentHandler := handler.NewAgentHandler()
	consultHandler := handler.NewConsultHandler()
	milestoneHandler := handler.NewMilestoneHandler()
	rollbackHandler := handler.NewRollbackHandler()
	gitHandler := handler.NewGitHandler()
	branchHandler := handler.NewBranchHandler()
	prHandler := handler.NewPRHandler()
	roleHandler := handler.NewRoleHandler()
	chiefHandler := handler.NewChiefHandler()
	feedbackHandler := handler.NewFeedbackHandler()
	experienceHandler := handler.NewExperienceHandler()
	skillHandler := handler.NewSkillHandler()
	policyHandler := handler.NewPolicyHandler()
	refineryHandler := handler.NewRefineryHandler()
	agentPoolHandler := handler.NewAgentPoolHandler()
	loopCheckHandler := handler.NewLoopCheckHandler()

	autopilot := config.IsAutopilotEnabled()
	if autopilot {
		log.Printf("[Boot] autopilot mode: ON (A3C_AUTOPILOT set; pool + dispatcher + auto-audit will run)")
	} else {
		log.Printf("[Boot] autopilot mode: OFF (collaboration-hub mode; pool/dispatcher/auto-audit disabled — set A3C_AUTOPILOT=1 to re-enable legacy behaviour)")
	}

	poolCmd := strings.TrimSpace(os.Getenv("A3C_OPENCODE_CMD"))
	var poolArgs []string
	if raw := strings.TrimSpace(os.Getenv("A3C_OPENCODE_ARGS")); raw != "" {
		poolArgs = strings.Fields(raw)
	}
	poolOC := opencodepkg.NewPoolSessionCreator(0)
	poolInject := opencodepkg.NewPoolBroadcastInjector(0)
	poolManager := agentpool.NewManager(agentpool.ManagerConfig{
		Root:                 fmt.Sprintf("%s/pool", cfg.DataDir),
		PlatformURL:          fmt.Sprintf("http://localhost:%d", cfg.Server.Port),
		Command:              poolCmd,
		Args:                 poolArgs,
		ContextWatchInterval: 30 * time.Second, // poll cadence — see ManagerConfig doc
		IdleTimeout:          30 * time.Minute, // dormancy trigger; 0 would disable the detector
		// ArchiveThresholdTokens omitted = ApplyDefaults sets 150_000.
	}, nil).
		WithSessionCreator(poolOC).
		WithContextProbe(poolOC).
		WithArchiveNotifier(service.NewPoolArchiveNotifier()).
		WithBroadcastConsumer(service.NewPoolBroadcastConsumer()).
		WithBroadcastInjector(poolInject)
	agentpool.SetDefault(poolManager)
	if poolCmd != "" {
		log.Printf("[Pool] spawner command override: %s %v", poolCmd, poolArgs)
	}
	if autopilot {
		poolManager.StartContextWatcher(context.Background())
		poolManager.StartBroadcastConsumer(context.Background(), 0)
		poolManager.StartDormancyDetector(context.Background())
		log.Printf("[Pool] broadcast consumer + dormancy detector started")
	} else {
		log.Printf("[Pool] background loops NOT started (autopilot=off)")
	}

	v1.POST("/auth/login", authHandler.Login)
	v1.POST("/auth/logout", authHandler.Logout)
	v1.POST("/agent/register", authHandler.Register)

	// /events is registered here (not in the auth group) because the
	// browser EventSource API cannot attach an Authorization header,
	// so the SSE handler does its own authentication inline: the
	// ?key= query param is mandatory and must resolve to an agent that
	// either is human or has the requested project_id currently
	// selected. See handler/sse.go.
	v1.GET("/events", sseHandler.Events)

	auth := v1.Group("", middleware.AuthMiddleware())
	{
		auth.POST("/project/create", projectHandler.Create)
		auth.GET("/project/list", projectHandler.List)
		auth.GET("/project/:id", projectHandler.Get)

		auth.POST("/auth/heartbeat", authHandler.Heartbeat)
		auth.POST("/auth/select-project", authHandler.SelectProject)
		auth.POST("/task/create", taskHandler.Create)
		auth.POST("/task/claim", taskHandler.Claim)
		auth.POST("/task/complete", taskHandler.Complete)
		auth.POST("/task/release", taskHandler.Release)
		auth.DELETE("/task/:task_id", taskHandler.Delete)
		auth.GET("/task/list", taskHandler.List)

		// Tag lifecycle — see @platform/backend/internal/handler/tag.go
		auth.GET("/tag/list", tagHandler.List)
		auth.POST("/tag/confirm", tagHandler.Confirm)
		auth.POST("/tag/reject", tagHandler.Reject)
		auth.POST("/tag/supersede", tagHandler.Supersede)

		// Injection-signal metrics — see @platform/backend/internal/handler/metrics.go
		auth.GET("/metrics/injection-signal", metricsHandler.InjectionSignal)

		// Loop-health diagnostic — see @platform/backend/internal/handler/loopcheck.go
		// Reports whether every self-evolution and automation loop
		// is flowing at its expected cadence. Read-only. Safe to
		// poll at ~1/min from the dashboard.
		auth.GET("/loopcheck", loopCheckHandler.Get)

		// User-registered LLM endpoints (PR 10 — opencode replacement).
		// List/Get are open to any authenticated agent so MCP clients
		// can introspect; mutations + Test are human-only (enforced
		// inside the handler via requireHuman).
		auth.GET("/llm/endpoints", llmEndpointHandler.List)
		auth.GET("/llm/endpoints/:id", llmEndpointHandler.Get)
		auth.POST("/llm/endpoints", llmEndpointHandler.Create)
		auth.PUT("/llm/endpoints/:id", llmEndpointHandler.Update)
		auth.DELETE("/llm/endpoints/:id", llmEndpointHandler.Delete)
		auth.POST("/llm/endpoints/:id/test", llmEndpointHandler.Test)

		auth.POST("/filelock/acquire", lockHandler.Acquire)
		auth.POST("/filelock/release", lockHandler.Release)
		auth.POST("/filelock/renew", lockHandler.Renew)
		auth.POST("/filelock/check", lockHandler.Check)

		// /change/submit is the most expensive write path because in
		// autopilot mode it spawns the full audit_1 → fix → audit_2
		// LLM chain. In collaboration-hub mode it just persists the
		// change; either way capping the per-agent burst is cheap
		// insurance. Tuning: 1 rps + burst 5 — a normal commit-loop
		// shouldn't touch this; an attacker with a hot key gets
		// boxed at ~60 attempts per minute.
		auth.POST("/change/submit",
			middleware.AgentRateLimit("change", 1, 5),
			changeHandler.Submit)
		auth.GET("/change/list", changeHandler.List)
		auth.GET("/change/status", changeHandler.Status)
		auth.POST("/change/review", changeHandler.Review)
		auth.POST("/change/approve_for_review",
			middleware.AgentRateLimit("change_approve", 0.5, 3),
			changeHandler.ApproveForReview)

		auth.POST("/file/sync", fileSyncHandler.Sync)

		auth.GET("/status/sync", statusHandler.Sync)
		auth.POST("/poll", statusHandler.Poll)

		auth.GET("/dashboard/state", dashboardHandler.GetState)
		auth.POST("/dashboard/input", dashboardHandler.Input)
		auth.POST("/dashboard/confirm", dashboardHandler.Confirm)
		auth.POST("/dashboard/clear_context", dashboardHandler.ClearContext)
		auth.GET("/dashboard/messages", dashboardHandler.GetMessages)

		auth.POST("/project/info", consultHandler.ProjectInfo)
		auth.POST("/project/auto_mode", projectHandler.SetAutoMode)

		auth.POST("/milestone/switch", milestoneHandler.Switch)
		auth.GET("/milestone/archives", milestoneHandler.Archives)

		auth.POST("/version/rollback", rollbackHandler.Rollback)
		auth.GET("/version/list", rollbackHandler.ListVersions)

		// Branch APIs
		auth.POST("/branch/create", branchHandler.Create)
		auth.POST("/branch/enter", branchHandler.Enter)
		auth.POST("/branch/leave", branchHandler.Leave)
		auth.GET("/branch/list", branchHandler.List)
		auth.POST("/branch/close", branchHandler.Close)
		auth.POST("/branch/sync_main", branchHandler.SyncMain)
		auth.POST("/branch/change_submit", branchHandler.BranchChangeSubmit)
		auth.GET("/branch/file_sync", branchHandler.BranchFileSync)

		// Role & Model APIs
		auth.GET("/role/list", roleHandler.ListRoles)
		auth.POST("/role/update_model", roleHandler.UpdateRoleModel)
		auth.GET("/opencode/providers", roleHandler.GetProviders)

		// PR APIs
		auth.POST("/pr/submit", prHandler.Submit)
		auth.GET("/pr/list", prHandler.List)
		auth.GET("/pr/:pr_id", prHandler.GetPR)
		auth.POST("/pr/approve_review", prHandler.ApproveReview)
		auth.POST("/pr/approve_merge", prHandler.ApproveMerge)
		auth.POST("/pr/reject", prHandler.Reject)

		// Chief Agent APIs.
		// /chief/chat is gated by a per-agent rate limit because it
		// spawns LLM work — a misbehaving MCP client (or stolen
		// access_key) used to be able to hammer this and rack up cost.
		// Read-only routes (sessions/traces/policies) stay unmetered.
		// Tuning: 0.2 rps ≈ 1 chat per 5s, burst=3 covers a normal
		// "type → retry → resend" UX without throttling humans.
		auth.POST("/chief/chat",
			middleware.AgentRateLimit("chief", 0.2, 3),
			chiefHandler.Chat)
		auth.GET("/chief/sessions", chiefHandler.Sessions)
		auth.GET("/chief/traces", chiefHandler.ToolTraces)
		auth.GET("/chief/policies", chiefHandler.Policies)

		// Experience & Feedback APIs
		auth.POST("/feedback/submit", feedbackHandler.Submit)
		auth.GET("/experience/list", experienceHandler.List)

		// Skill & Policy CRUD APIs
		auth.GET("/skill/list", skillHandler.List)
		auth.GET("/skill/:id", skillHandler.Get)
		auth.POST("/skill/:id/approve", skillHandler.Approve)
		auth.POST("/skill/:id/reject", skillHandler.Reject)
		auth.GET("/policy/list", policyHandler.List)
		auth.GET("/policy/:id", policyHandler.Get)
		auth.POST("/policy/:id/activate", policyHandler.Activate)
		auth.POST("/policy/:id/deactivate", policyHandler.Deactivate)

		// Refinery pipeline (M1): multi-pass knowledge distillation.
		// /refinery/run is the only LLM-spawning entry; the rest are
		// metadata reads. Tuning: 0.05 rps ≈ 1 every 20s, burst=2 —
		// generous for the dashboard "Run Refinery" button but blocks
		// any client that tries to chain triggers.
		auth.POST("/refinery/run",
			middleware.AgentRateLimit("refinery", 0.05, 2),
			refineryHandler.Run)
		auth.GET("/refinery/runs", refineryHandler.Runs)
		auth.GET("/refinery/artifacts", refineryHandler.Artifacts)
		auth.GET("/refinery/growth", refineryHandler.Growth)
		auth.PUT("/refinery/artifacts/:id/status", refineryHandler.UpdateArtifactStatus)

		// Platform-hosted agent pool (spawn opencode subprocesses on
		// the same host). Human-gated — only the dashboard operator
		// can bring pool agents up / tear them down.
		auth.GET("/agentpool/list", agentPoolHandler.List)
		auth.GET("/agentpool/opencode-providers", agentPoolHandler.OpencodeProviders)
		auth.GET("/agentpool/metrics/:instance_id", agentPoolHandler.Metrics)
		auth.POST("/agentpool/spawn", agentPoolHandler.Spawn)
		auth.POST("/agentpool/shutdown", agentPoolHandler.Shutdown)
		auth.POST("/agentpool/sleep", agentPoolHandler.Sleep)
		auth.POST("/agentpool/wake", agentPoolHandler.Wake)
		auth.POST("/agentpool/purge", agentPoolHandler.Purge)
	}

	// /api/v1/internal/* was previously bound directly on `v1` with no
	// middleware, which meant every agent-session, git and project-import
	// endpoint was world-callable. Nothing in this repo (MCP client,
	// native runner, agentpool) actually goes over HTTP for these — they
	// are Go-level calls — so gating the whole group with AuthMiddleware
	// closes the hole without breaking callers. If a genuinely unauth'd
	// sidecar is ever added, it should get its own token, not a blanket
	// bypass.
	internal := v1.Group("/internal", middleware.AuthMiddleware())
	{
		internal.POST("/agent/audit_output", agentHandler.AuditOutput)
		internal.POST("/agent/fix_output", agentHandler.FixOutput)
		internal.POST("/agent/audit2_output", agentHandler.Audit2Output)
		internal.GET("/agent/session/:session_id", agentHandler.GetSession)
		internal.GET("/agent/session/:session_id/prompt", agentHandler.GetPrompt)
		internal.POST("/agent/session/:session_id/output", agentHandler.SubmitOutput)
		internal.GET("/agent/sessions", agentHandler.ListSessions)
		internal.POST("/project/:id/import-assess", projectHandler.ImportAssess)
		internal.POST("/git/diff", gitHandler.Diff)
		internal.POST("/git/commit", gitHandler.Commit)
		internal.POST("/git/revert", gitHandler.Revert)
		internal.POST("/git/push", gitHandler.Push)
		internal.POST("/git/add-remote", gitHandler.AddRemote)
	}

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("Server starting on %s", addr)

	// Hook the bge-base-zh-v1.5 sidecar into the refinery pipeline so every
	// artifact gets a semantic embedding at creation time. Safe to call
	// before the sidecar is up — individual embed calls are best-effort
	// and simply log + skip when the service is unreachable.
	service.InstallEmbedderIntoRefinery(nil)

	service.StartMaintainTimer()
	service.StartHeartbeatChecker()
	service.StartAnalyzeTimer()
	service.StartRefineryTimer()
	service.StartTaskEmbeddingBackfillTimer()
	// Retention worker prunes tool_call_trace + dialogue_message rows
	// older than A3C_RETENTION_DAYS (default 90). High-volume volatile
	// tables that grow unbounded otherwise. Long-term signal lives in
	// KnowledgeArtifact (Refinery output) which is NOT pruned.
	service.StartRetentionTimer()
	// Task dispatcher is the matcher that pulls pending tasks from
	// the DB and pushes TASK_ASSIGN broadcasts to idle pool agents.
	// It only ever assigns to is_platform_hosted=true agents
	// (see runDispatcherOnce), so when autopilot is off and no pool
	// agents come up there is literally nothing for it to do — but
	// the every-15s tick is still wasted DB queries. Keep it
	// gated to the autopilot path.
	if autopilot {
		service.StartTaskDispatcher()
	} else {
		log.Printf("[Dispatcher] Task dispatcher NOT started (autopilot=off)")
	}

	// Graceful shutdown.
	//
	// Before this, main ended with `r.Run(addr)` which blocks until
	// SIGKILL. That meant:
	//   * pool subprocesses (opencode serves) became orphans on
	//     parent exit, sometimes wedging their listening ports
	//     until manually killed,
	//   * in-flight LLM calls / audit pipelines got their goroutines
	//     yanked mid-write, which left agent_sessions stuck in
	//     `running` forever (only the heartbeat checker eventually
	//     reset them, after a 7-minute lag),
	//   * the operator's tmux logs ended on a SIGINT line with no
	//     "shutting down" trace, making "did the platform stop
	//     cleanly?" a guessing game.
	//
	// Shutdown order matters:
	//   1. Stop the HTTP server first (Server.Shutdown drains
	//      in-flight requests, refuses new connections). This is
	//      what bounds shutdown by the configured timeout.
	//   2. Cancel the long-running pool background loops so they
	//      don't race with us tearing down their dependencies. The
	//      context cancel is what their inner select-loops watch.
	//   3. Terminate pool subprocesses (ShutdownAll). Best-effort
	//      SIGTERM with a short grace; if it doesn't exit, the manager
	//      escalates to SIGKILL.
	//
	// We deliberately do NOT call into the DB / Redis client
	// shutdowns: GORM and go-redis both flush idle connections at
	// process exit, and forcing a Close before in-flight saves
	// finish would defeat the point of waiting for the HTTP server
	// to drain.
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	// poolCtx wraps every pool background loop so they can be
	// cancelled together. Previously each .Start* received a fresh
	// context.Background(), making them un-cancellable from main.
	// We lazily replace those contexts only at shutdown time —
	// the Start* calls above already happened with Background()
	// and that's fine because the inner loops re-derive their own
	// stop signals via Stop* methods. poolCtx is here for any
	// future loops we add and want to participate.
	poolCtx, poolCancel := context.WithCancel(context.Background())
	_ = poolCtx // reserved; see comment above

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()
	log.Printf("[Boot] HTTP server listening on %s", addr)

	// SIGINT and SIGTERM: SIGINT is what an operator's Ctrl-C
	// sends; SIGTERM is what process supervisors (systemd, k8s)
	// send. SIGHUP is intentionally not handled — keep the
	// "reload config" semantics open for a future restart-free
	// flow.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("[Shutdown] received %s, draining (timeout=20s)...", sig)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1. HTTP server: stops accepting new conns, waits for in-flight
	//    handlers to finish (including SSE long-poll connections).
	//    SSE clients with `event-source` will reconnect when the
	//    server comes back, so abruptly closing here is OK.
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[Shutdown] HTTP server shutdown returned: %v (proceeding anyway)", err)
	} else {
		log.Printf("[Shutdown] HTTP server drained")
	}

	// 2. Pool background loops. The Stop* methods are safe to call
	//    even if the corresponding Start* never ran (autopilot=off).
	poolManager.StopBroadcastConsumer()
	poolManager.StopDormancyDetector()
	poolCancel()
	log.Printf("[Shutdown] pool background loops stopped")

	// 3. Pool subprocesses. Each one gets a SIGTERM with a short
	//    grace; if it doesn't exit, the manager escalates to SIGKILL.
	//    No-op when no instances are running.
	poolManager.ShutdownAll()
	log.Printf("[Shutdown] pool subprocesses terminated")

	log.Printf("[Shutdown] complete")
}