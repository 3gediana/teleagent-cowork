package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

type AuthHandler struct{}

func NewAuthHandler() *AuthHandler {
	return &AuthHandler{}
}

type LoginRequest struct {
	Key     string `json:"key" binding:"required"`
	Project string `json:"project"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	agent, err := repo.GetAgentByKey(req.Key)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Internal error"}})
		return
	}
	if agent == nil {
		c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "AUTH_INVALID_KEY", "message": "Invalid access key"}})
		return
	}

	if agent.Status == "online" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "AUTH_ALREADY_ONLINE", "message": "Agent is already online. Logout first."}})
		return
	}

	now := time.Now()
	agent.Status = "online"
	agent.LastHeartbeat = &now
	sessionID := model.GenerateID("session")
	agent.SessionID = sessionID
	model.DB.Save(agent)

	model.RDB.Set(model.DB.Statement.Context, "a3c:agent:"+agent.ID+":heartbeat", now.Unix(), 300*time.Second)

	data := gin.H{
		"agent_id":   agent.ID,
		"agent_name": agent.Name,
	}

	if req.Project != "" {
		project, _ := repo.GetProjectByID(req.Project)
		if project != nil {
			agent.CurrentProjectID = &req.Project
			model.DB.Save(agent)
			direction, _ := repo.GetContentBlock(req.Project, "direction")
			milestone, _ := repo.GetCurrentMilestone(req.Project)
			version, _ := repo.GetContentBlock(req.Project, "version")

			projectCtx := gin.H{
				"id":         project.ID,
				"name":       project.Name,
				"direction":  "",
				"milestone":  "",
				"version":    "v1.0",
			}
			if direction != nil {
				projectCtx["direction"] = direction.Content
			}
			if milestone != nil {
				projectCtx["milestone"] = milestone.Name
			}
			if version != nil {
				projectCtx["version"] = version.Content
			}
			data["project_context"] = projectCtx
		}
	} else {
		var projects []model.Project
		model.DB.Find(&projects)
		list := make([]gin.H, 0, len(projects))
		for _, p := range projects {
			list = append(list, gin.H{"id": p.ID, "name": p.Name, "description": p.Description})
		}
		data["projects"] = list
	}

	c.JSON(200, gin.H{"success": true, "data": data})
}

func (h *AuthHandler) Logout(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	agent, _ := repo.GetAgentByID(agentID.(string))

	if agent != nil {
		now := time.Now()
		agent.Status = "offline"
		agent.LastHeartbeat = &now
		model.DB.Save(agent)

		var locks []model.FileLock
		model.DB.Where("agent_id = ? AND released_at IS NULL", agent.ID).Find(&locks)
		for i := range locks {
			locks[i].ReleasedAt = &now
			model.DB.Save(&locks[i])
		}

		var tasks []model.Task
		model.DB.Where("assignee_id = ? AND status = 'claimed'", agent.ID).Find(&tasks)
		releasedTasks := make([]string, 0)
		for i := range tasks {
			tasks[i].Status = "pending"
			tasks[i].AssigneeID = nil
			model.DB.Save(&tasks[i])
			releasedTasks = append(releasedTasks, tasks[i].ID)
		}

		model.RDB.Del(model.DB.Statement.Context, "a3c:agent:"+agent.ID+":heartbeat")

		releasedFiles := make([]string, 0)
		for _, l := range locks {
			releasedFiles = append(releasedFiles, l.ID)
		}

		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"released_locks": releasedFiles,
				"released_tasks": releasedTasks,
			},
		})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"released_locks": []string{}, "released_tasks": []string{}}})
}

func (h *AuthHandler) Heartbeat(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	agent, _ := repo.GetAgentByID(agentID.(string))

	if agent != nil {
		now := time.Now()
		agent.LastHeartbeat = &now
		agent.Status = "online"
		model.DB.Save(agent)
		model.RDB.Set(model.DB.Statement.Context, "a3c:agent:"+agent.ID+":heartbeat", now.Unix(), 300*time.Second)

		var locks []model.FileLock
		model.DB.Where("agent_id = ? AND released_at IS NULL AND expires_at > NOW()", agent.ID).Find(&locks)

		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"server_time": now.Format(time.RFC3339),
				"lock_ttl":     300,
			},
		})
		return
	}

	c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "AUTH_INVALID_KEY", "message": "Agent not found"}})
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req struct {
		Name      string `json:"name" binding:"required"`
		ProjectID string `json:"project_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if req.ProjectID != "" {
		if err := service.EnforceAgentLimit(req.ProjectID); err != nil {
			c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "PROJECT_FULL", "message": err.Error()}})
			return
		}
	}

	agent := model.Agent{
		ID:        model.GenerateID("agent"),
		Name:      req.Name,
		AccessKey: model.GenerateKey(),
		Status:    "offline",
	}
	if req.ProjectID != "" {
		agent.CurrentProjectID = &req.ProjectID
	}

	if err := model.DB.Create(&agent).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to create agent"}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"agent_id":   agent.ID,
			"name":        agent.Name,
			"access_key":  agent.AccessKey,
		},
	})
}