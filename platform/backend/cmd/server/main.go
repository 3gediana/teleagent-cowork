package main

import (
	"fmt"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/handler"
	"github.com/a3c/platform/internal/middleware"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/opencode"
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

	opencode.InitScheduler(cfg.OpenCode)

	service.InitDataPath(cfg.DataDir)

	agent.RegisterDispatcher(func(session *agent.Session) error {
		return opencode.DefaultScheduler.Dispatch(session)
	})

	// Wire dashboard session callback to bridge service → handler without import cycle
	service.DashboardSessionCallback = handler.SetDashboardSessionForProject

	// Wire tool call handler to bridge opencode → service without import cycle
	opencode.ToolCallHandler = service.HandleToolCallResult

	// Wire broadcast handler for real-time SSE push from scheduler to frontend
	opencode.BroadcastHandler = func(projectID, eventType string, payload map[string]interface{}) {
		service.SSEManager.BroadcastToProject(projectID, eventType, gin.H(payload), "")
	}

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

	v1.POST("/auth/login", authHandler.Login)
	v1.POST("/auth/logout", authHandler.Logout)
	v1.POST("/agent/register", authHandler.Register)

	v1.POST("/project/create", projectHandler.Create)
	v1.GET("/project/:id", projectHandler.Get)
	v1.GET("/project/list", projectHandler.List)

	auth := v1.Group("", middleware.AuthMiddleware())
	{
		auth.POST("/auth/heartbeat", authHandler.Heartbeat)
		auth.POST("/auth/select-project", authHandler.SelectProject)
		auth.POST("/task/create", taskHandler.Create)
		auth.POST("/task/claim", taskHandler.Claim)
		auth.POST("/task/complete", taskHandler.Complete)
		auth.POST("/task/release", taskHandler.Release)
		auth.DELETE("/task/:task_id", taskHandler.Delete)
		auth.GET("/task/list", taskHandler.List)

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

		auth.GET("/events", sseHandler.Events)

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
	}

	internal := v1.Group("/internal")
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

	service.StartMaintainTimer()
	service.StartHeartbeatChecker()
	service.StartAnalyzeTimer()

	if err := r.Run(addr); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}