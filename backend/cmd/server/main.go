package main

import (
	"fmt"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/handler"
	"github.com/a3c/platform/internal/middleware"
	"github.com/a3c/platform/internal/model"
)

func main() {
	cfg := config.Load("")

	if err := model.InitDB(&cfg.Database); err != nil {
		log.Fatalf("Database init failed: %v", err)
	}

	if err := model.InitRedis(&cfg.Redis); err != nil {
		log.Fatalf("Redis init failed: %v", err)
	}

	gin.SetMode(cfg.Server.Mode)
	r := gin.Default()

	r.Use(middleware.CORSMiddleware())

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

	v1.POST("/auth/login", authHandler.Login)
	v1.POST("/auth/logout", authHandler.Logout)
	v1.POST("/auth/heartbeat", authHandler.Heartbeat)
	v1.POST("/agent/register", authHandler.Register)

	v1.POST("/project/create", projectHandler.Create)
	v1.GET("/project/:id", projectHandler.Get)
	v1.GET("/project/list", projectHandler.List)

	auth := v1.Group("", middleware.AuthMiddleware())
	{
		auth.POST("/task/create", taskHandler.Create)
		auth.POST("/task/claim", taskHandler.Claim)
		auth.POST("/task/complete", taskHandler.Complete)
		auth.DELETE("/task/:task_id", taskHandler.Delete)
		auth.GET("/task/list", taskHandler.List)

		auth.POST("/filelock/acquire", lockHandler.Acquire)
		auth.POST("/filelock/release", lockHandler.Release)
		auth.POST("/filelock/renew", lockHandler.Renew)

		auth.POST("/change/submit", changeHandler.Submit)
		auth.GET("/change/list", changeHandler.List)
		auth.POST("/change/review", changeHandler.Review)

		auth.POST("/file/sync", fileSyncHandler.Sync)

		auth.GET("/status/sync", statusHandler.Sync)
		auth.POST("/poll", statusHandler.Poll)

		auth.GET("/events", sseHandler.Events)

		auth.GET("/dashboard/state", dashboardHandler.GetState)
		auth.POST("/dashboard/input", dashboardHandler.Input)
		auth.POST("/dashboard/confirm", dashboardHandler.Confirm)
		auth.POST("/dashboard/clear_context", dashboardHandler.ClearContext)

		auth.POST("/project/info", consultHandler.ProjectInfo)

		auth.POST("/milestone/switch", milestoneHandler.Switch)
		auth.GET("/milestone/archives", milestoneHandler.Archives)

		auth.POST("/version/rollback", rollbackHandler.Rollback)
		auth.GET("/version/list", rollbackHandler.ListVersions)
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
	}

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("Server starting on %s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}