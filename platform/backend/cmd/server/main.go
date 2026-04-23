package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
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
	log.Printf("Config loaded: DataDir=%s", cfg.DataDir)

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

	// Platform-hosted agent pool — spawns opencode subprocesses on
	// the same host, auto-injects skills from the DB + baseline
	// client/skill/using-a3c-platform, and treats them like normal
	// client agents. See internal/agentpool/pool.go. The pool is
	// opt-in: if no handler ever calls Spawn, zero subprocesses
	// are created.
	// Allow operators to pin the spawner command explicitly. The pool's
	// default is "opencode" resolved against PATH, which doesn't help
	// on Windows deployments where opencode ships as opencode.cmd under
	// a non-PATH directory like D:\openclaw\npm. A3C_OPENCODE_CMD is
	// the absolute path (or basename) to invoke; A3C_OPENCODE_ARGS is
	// a whitespace-separated prefix such as "serve" — the pool still
	// appends "--port <N>" automatically when the operator did not
	// already include one.
	poolCmd := strings.TrimSpace(os.Getenv("A3C_OPENCODE_CMD"))
	var poolArgs []string
	if raw := strings.TrimSpace(os.Getenv("A3C_OPENCODE_ARGS")); raw != "" {
		poolArgs = strings.Fields(raw)
	}
	// PoolSessionCreator doubles as both SessionCreator and
	// ContextProbe — one object, two interfaces — so the pool only
	// needs the one wire-up here rather than two parallel clients
	// pointing at the same opencode serves.
	poolOC := opencodepkg.NewPoolSessionCreator(0)
	poolManager := agentpool.NewManager(agentpool.ManagerConfig{
		Root:                 fmt.Sprintf("%s/pool", cfg.DataDir),
		PlatformURL:          fmt.Sprintf("http://localhost:%d", cfg.Server.Port),
		Command:              poolCmd,
		Args:                 poolArgs,
		ContextWatchInterval: 30 * time.Second, // poll cadence — see ManagerConfig doc
		// ArchiveThresholdTokens omitted = ApplyDefaults sets 150_000.
	}, nil).
		WithSessionCreator(poolOC).
		WithContextProbe(poolOC).
		WithArchiveNotifier(service.NewPoolArchiveNotifier())
	agentpool.SetDefault(poolManager)
	if poolCmd != "" {
		log.Printf("[Pool] spawner command override: %s %v", poolCmd, poolArgs)
	}
	// Start the context watcher goroutine once the pool manager is
	// wired. context.Background() gives it a server-lifetime ceiling;
	// pool.ShutdownAll() stops it cleanly on graceful shutdown.
	poolManager.StartContextWatcher(context.Background())

	// Unauthenticated bootstrap endpoints only. Anything that mutates
	// or reads project state now lives in the auth group below — previously
	// /project/create, /project/:id and /project/list were open which let
	// any unauthenticated caller enumerate and create projects.
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

		auth.POST("/change/submit", changeHandler.Submit)
		auth.GET("/change/list", changeHandler.List)
		auth.GET("/change/status", changeHandler.Status)
		auth.POST("/change/review", changeHandler.Review)
		auth.POST("/change/approve_for_review", changeHandler.ApproveForReview)

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

		// Chief Agent APIs
		auth.POST("/chief/chat", chiefHandler.Chat)
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
		auth.POST("/refinery/run", refineryHandler.Run)
		auth.GET("/refinery/runs", refineryHandler.Runs)
		auth.GET("/refinery/artifacts", refineryHandler.Artifacts)
		auth.GET("/refinery/growth", refineryHandler.Growth)
		auth.PUT("/refinery/artifacts/:id/status", refineryHandler.UpdateArtifactStatus)

		// Platform-hosted agent pool (spawn opencode subprocesses on
		// the same host). Human-gated — only the dashboard operator
		// can bring pool agents up / tear them down.
		auth.GET("/agentpool/list", agentPoolHandler.List)
		auth.POST("/agentpool/spawn", agentPoolHandler.Spawn)
		auth.POST("/agentpool/shutdown", agentPoolHandler.Shutdown)
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

	if err := r.Run(addr); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}