package handler

import (
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

// logSaveErr is a small wrapper around model.DB.Save that logs
// failures with a short context tag. Auth-state transitions don't
// abort the request when a save fails (the caller still gets a 200
// for login etc.), but the failure absolutely must not be silent —
// dropped saves here mean stale heartbeat / dangling locks /
// orphaned tasks. The handler used to ignore Save errors entirely;
// this helper preserves that non-fatal behaviour while at least
// surfacing the error in the server journal.
func logSaveErr(rec interface{}, what string) {
	if err := model.DB.Save(rec).Error; err != nil {
		log.Printf("[Auth] DB save %s: %v", what, err)
	}
}

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
		// Check if heartbeat is still alive (auto-cleanup stale agents)
		heartbeatKey := "a3c:agent:" + agent.ID + ":heartbeat"
		heartbeatExists, _ := model.RDB.Exists(model.DB.Statement.Context, heartbeatKey).Result()
		if heartbeatExists == 0 {
			// Heartbeat expired, auto-cleanup: release locks and tasks
			now := time.Now()
			agent.Status = "offline"
			agent.LastHeartbeat = &now
			logSaveErr(agent, "login/stale-cleanup agent")

			var locks []model.FileLock
			model.DB.Where("agent_id = ? AND released_at IS NULL", agent.ID).Find(&locks)
			for i := range locks {
				locks[i].ReleasedAt = &now
				logSaveErr(&locks[i], "login/stale-cleanup lock")
			}

			var tasks []model.Task
			model.DB.Where("assignee_id = ? AND status = 'claimed'", agent.ID).Find(&tasks)
			for i := range tasks {
				tasks[i].Status = "pending"
				tasks[i].AssigneeID = nil
				logSaveErr(&tasks[i], "login/stale-cleanup task")
			}

			// Release branch occupancy so other agents can enter the branch,
			// matching what StartHeartbeatChecker does for timed-out agents.
			model.DB.Model(&model.Branch{}).
				Where("occupant_id = ? AND status = 'active'", agent.ID).
				Update("occupant_id", nil)
			// Fall through to normal login
		} else {
			c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "AUTH_ALREADY_ONLINE", "message": "Agent is already online. Please logout first: ⚙ a3c_platform [action=logout]"}})
			return
		}
	}

	now := time.Now()
	agent.Status = "online"
	agent.LastHeartbeat = &now
	sessionID := model.GenerateID("session")
	agent.SessionID = sessionID
	logSaveErr(agent, "login/online-mark agent")

	// Match the 7-minute heartbeat timeout in scheduler.go to tolerate jitter.
	model.RDB.Set(model.DB.Statement.Context, "a3c:agent:"+agent.ID+":heartbeat", now.Unix(), 7*time.Minute)

	// Auto-ack existing broadcasts so new agents don't receive stale history
	if agent.CurrentProjectID != nil && *agent.CurrentProjectID != "" {
		service.SSEManager.AckAllBroadcasts(*agent.CurrentProjectID, agent.ID)
	}

	data := gin.H{
		"agent_id":   agent.ID,
		"agent_name": agent.Name,
	}

	if req.Project != "" {
		project, _ := repo.GetProjectByID(req.Project)
		if project != nil {
			agent.CurrentProjectID = &req.Project
			logSaveErr(agent, "login/select-project agent")
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
	var req struct {
		Key string `json:"key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	agent, err := repo.GetAgentByKey(req.Key)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}
	if agent == nil {
		c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "AUTH_INVALID_KEY", "message": "Invalid access key"}})
		return
	}
	now := time.Now()
	agent.Status = "offline"
	agent.LastHeartbeat = &now
	logSaveErr(agent, "logout agent")

	var locks []model.FileLock
	model.DB.Where("agent_id = ? AND released_at IS NULL", agent.ID).Find(&locks)
	for i := range locks {
		locks[i].ReleasedAt = &now
		logSaveErr(&locks[i], "logout lock")
	}

	var tasks []model.Task
	model.DB.Where("assignee_id = ? AND status = 'claimed'", agent.ID).Find(&tasks)
	releasedTasks := make([]string, 0)
	for i := range tasks {
		tasks[i].Status = "pending"
		tasks[i].AssigneeID = nil
		logSaveErr(&tasks[i], "logout task")
		releasedTasks = append(releasedTasks, tasks[i].ID)
	}

	// Release branch occupancy on logout too.
	model.DB.Model(&model.Branch{}).
		Where("occupant_id = ? AND status = 'active'", agent.ID).
		Update("occupant_id", nil)

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
}

func (h *AuthHandler) SelectProject(c *gin.Context) {
	var req struct {
		Project string `json:"project" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	agentID, _ := c.Get("agent_id")
	agent, err := repo.GetAgentByID(agentID.(string))
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}
	if agent == nil {
		c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "AUTH_INVALID_KEY", "message": "Agent not found"}})
		return
	}

	project, err := repo.GetProjectByID(req.Project)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}
	if project == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PROJECT_NOT_FOUND", "message": "Project not found"}})
		return
	}

	agent.CurrentProjectID = &req.Project
	if err := model.DB.Save(agent).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	direction, _ := repo.GetContentBlock(req.Project, "direction")
	milestone, _ := repo.GetCurrentMilestone(req.Project)
	version, _ := repo.GetContentBlock(req.Project, "version")

	projectCtx := gin.H{
		"id":        project.ID,
		"name":      project.Name,
		"direction": "",
		"milestone": "",
		"version":   "v1.0",
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

	// Get online agents in this project
	var onlineAgents []model.Agent
	model.DB.Where("current_project_id = ? AND status = 'online'", req.Project).Find(&onlineAgents)
	agentList := make([]gin.H, 0, len(onlineAgents))
	for _, a := range onlineAgents {
		agentList = append(agentList, gin.H{
			"id":   a.ID,
			"name": a.Name,
		})
	}

	// Notify other agents of this one coming online. Send a single project-wide
	// broadcast; the consumer side already filters out the sender via the SSE
	// ack mechanism (AckAllBroadcasts was called on our own login earlier).
	// Previously this fanned out N-1 copies which bloated Redis.
	service.BroadcastEvent(req.Project, "AGENT_ONLINE", gin.H{
		"agent_id":   agent.ID,
		"agent_name": agent.Name,
	})

	// Get active branches for this project
	branches, _ := service.ListBranchesWithOccupants(req.Project)

	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"agent_id":        agent.ID,
		"agent_name":      agent.Name,
		"project_context": projectCtx,
		"online_agents":   agentList,
		"branches":        branches,
	}})
}

func (h *AuthHandler) Heartbeat(c *gin.Context) {
	agentIDRaw, _ := c.Get("agent_id")
	agentID, ok := agentIDRaw.(string)
	if !ok || agentID == "" || agentID == "human" {
		c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "AUTH_INVALID_KEY", "message": "Agent not found"}})
		return
	}
	agent, _ := repo.GetAgentByID(agentID)

	if agent != nil {
		now := time.Now()
		agent.LastHeartbeat = &now
		agent.Status = "online"
		logSaveErr(agent, "heartbeat agent")
		model.RDB.Set(model.DB.Statement.Context, "a3c:agent:"+agent.ID+":heartbeat", now.Unix(), 7*time.Minute)

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
		IsHuman   bool   `json:"is_human"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	// is_human bootstrap guard. Before this check, anyone could claim
	// is_human=true on register and instantly bypass every
	// requireHuman() gate (Chief chat, LLM endpoint CRUD, tag
	// lifecycle, etc.). Now:
	//   - If no human exists yet, allow is_human=true so the very
	//     first caller can bootstrap the dashboard.
	//   - Otherwise require the caller to prove they are already a
	//     human by sending a valid Bearer token whose agent has
	//     IsHuman=true. Register lives outside AuthMiddleware so we
	//     do this inline.
	if req.IsHuman {
		var humanCount int64
		model.DB.Model(&model.Agent{}).Where("is_human = ?", true).Count(&humanCount)
		if humanCount > 0 {
			authHeader := c.GetHeader("Authorization")
			token := strings.TrimPrefix(authHeader, "Bearer ")
			if authHeader == "" || token == authHeader || token == "" {
				c.JSON(403, gin.H{"success": false, "error": gin.H{
					"code":    "HUMAN_APPROVAL_REQUIRED",
					"message": "is_human=true requires a Bearer token for an existing human agent; bootstrap is already done",
				}})
				return
			}
			caller, err := repo.GetAgentByKey(token)
			if err != nil || caller == nil || !caller.IsHuman {
				c.JSON(403, gin.H{"success": false, "error": gin.H{
					"code":    "HUMAN_APPROVAL_REQUIRED",
					"message": "only an existing human agent can promote another agent to human",
				}})
				return
			}
		}
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
		IsHuman:   req.IsHuman,
	}
	if req.ProjectID != "" {
		agent.CurrentProjectID = &req.ProjectID
	}

	var existing model.Agent
	model.DB.Where("name = ?", req.Name).First(&existing)
	if existing.ID != "" {
		// SECURITY: do NOT return the existing agent's access_key. Previously
		// this endpoint would hand out any registered agent's credentials to
		// anyone knowing its name.
		c.JSON(409, gin.H{
			"success": false,
			"error": gin.H{
				"code":    "AGENT_NAME_TAKEN",
				"message": "Agent name already registered. Use your original access key or choose a different name.",
			},
		})
		return
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