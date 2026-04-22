package handler

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/opencode"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

// callerIsHuman returns true iff the authenticated agent is flagged IsHuman=true.
// Used to restrict human-only endpoints (dashboard input, direction/milestone
// changes, Chief chat) to the frontend user and not arbitrary client agents.
func callerIsHuman(c *gin.Context) bool {
	agentIDRaw, _ := c.Get("agent_id")
	aid, _ := agentIDRaw.(string)
	if aid == "" {
		return false
	}
	var a model.Agent
	if err := model.DB.Where("id = ?", aid).First(&a).Error; err != nil {
		return false
	}
	return a.IsHuman
}

func dashboardGetAgentName(agentID string) string {
	var agent model.Agent
	if err := model.DB.Where("id = ?", agentID).First(&agent).Error; err != nil {
		return agentID
	}
	return agent.Name
}

type DashboardHandler struct{}

func NewDashboardHandler() *DashboardHandler {
	return &DashboardHandler{}
}

func (h *DashboardHandler) GetState(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	project, _ := repo.GetProjectByID(projectID)
	if project == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PROJECT_NOT_FOUND", "message": "Project not found"}})
		return
	}

	direction, _ := repo.GetContentBlock(projectID, "direction")
	milestone, _ := repo.GetCurrentMilestone(projectID)
	version, _ := repo.GetContentBlock(projectID, "version")
	tasks, _ := repo.GetTasksByProject(projectID)
	locks, _ := repo.GetLocksByProject(projectID)

	var agents []model.Agent
	model.DB.Where("current_project_id = ? AND status = 'online'", projectID).Find(&agents)
	agentList := make([]gin.H, 0)
	for _, a := range agents {
		var currentTask *string
		var claimedTasks []model.Task
		model.DB.Where("assignee_id = ? AND status = 'claimed'", a.ID).Find(&claimedTasks)
		if len(claimedTasks) > 0 {
			currentTask = &claimedTasks[0].ID
		}
		agentList = append(agentList, gin.H{
			"id":                 a.ID,
			"name":               a.Name,
			"status":             a.Status,
			"current_task":       currentTask,
			"is_platform_hosted": a.IsPlatformHosted,
		})
	}

	taskList := make([]gin.H, 0)
	for _, t := range tasks {
		taskList = append(taskList, gin.H{
			"id":            t.ID,
			"name":          t.Name,
			"description":   t.Description,
			"status":        t.Status,
			"assignee_id":   t.AssigneeID,
			"assignee_name": dashboardGetAgentNamePtr(t.AssigneeID),
			"priority":      t.Priority,
			"milestone_id":  t.MilestoneID,
		})
	}

	lockList := make([]gin.H, 0)
	for _, l := range locks {
		var files []string
		json.Unmarshal([]byte(l.Files), &files)
		lockList = append(lockList, gin.H{
			"lock_id":     l.ID,
			"task_id":     l.TaskID,
			"agent_name":  dashboardGetAgentName(l.AgentID),
			"files":       files,
			"reason":      l.Reason,
			"acquired_at": l.AcquiredAt,
			"expires_at":  l.ExpiresAt,
		})
	}

	data := gin.H{
		"version":   "v1.0",
		"tasks":     taskList,
		"locks":     lockList,
		"agents":    agentList,
		"auto_mode": project.AutoMode,
	}
	if direction != nil {
		data["direction"] = direction.Content
	}
	if milestone != nil {
		data["milestone"] = milestone.Name
		data["milestone_id"] = milestone.ID
	}
	if version != nil {
		data["version"] = version.Content
	}

	c.JSON(200, gin.H{"success": true, "data": data})
}

func dashboardGetAgentNamePtr(id *string) string {
	if id == nil {
		return ""
	}
	return dashboardGetAgentName(*id)
}

type DashboardInput struct {
	InputID     string    `json:"input_id"`
	TargetBlock string    `json:"target_block"`
	Content     string    `json:"content"`
	ProjectID   string    `json:"-"`
	AgentID     string    `json:"-"` // creator, for ownership check on Confirm
	Confirmed   bool      `json:"confirmed"`
	CreatedAt   time.Time `json:"-"`
}

// pendingInputs is accessed from concurrent HTTP handlers. It must be
// guarded by a mutex. Entries also expire after pendingInputTTL to prevent
// unbounded memory growth.
var (
	pendingInputs   = make(map[string]*DashboardInput)
	pendingInputsMu sync.Mutex
)

const pendingInputTTL = 30 * time.Minute

func storePendingInput(input *DashboardInput) {
	pendingInputsMu.Lock()
	defer pendingInputsMu.Unlock()
	input.CreatedAt = time.Now()
	pendingInputs[input.InputID] = input
	// Opportunistic GC of expired entries
	for id, in := range pendingInputs {
		if time.Since(in.CreatedAt) > pendingInputTTL {
			delete(pendingInputs, id)
		}
	}
}

func takePendingInput(id string) (*DashboardInput, bool) {
	pendingInputsMu.Lock()
	defer pendingInputsMu.Unlock()
	in, ok := pendingInputs[id]
	if !ok {
		return nil, false
	}
	if time.Since(in.CreatedAt) > pendingInputTTL {
		delete(pendingInputs, id)
		return nil, false
	}
	return in, true
}

func deletePendingInput(id string) {
	pendingInputsMu.Lock()
	delete(pendingInputs, id)
	pendingInputsMu.Unlock()
}

// dashboardSession tracks the active OpenCode serve session per project for multi-round dialogue
type dashboardSessionInfo struct {
	OpenCodeSessionID string
	TargetBlock       string
	AgentSessionID    string
	Model             string
}

var (
	dashboardSessions   = make(map[string]*dashboardSessionInfo) // projectID -> session info
	dashboardSessionsMu sync.RWMutex
)

func getDashboardSession(projectID string) *dashboardSessionInfo {
	dashboardSessionsMu.RLock()
	defer dashboardSessionsMu.RUnlock()
	return dashboardSessions[projectID]
}

func setDashboardSession(projectID string, info *dashboardSessionInfo) {
	dashboardSessionsMu.Lock()
	defer dashboardSessionsMu.Unlock()
	dashboardSessions[projectID] = info
}

func clearDashboardSession(projectID string) {
	dashboardSessionsMu.Lock()
	defer dashboardSessionsMu.Unlock()
	delete(dashboardSessions, projectID)
}

// SetDashboardSessionForProject exports the ability to register a dashboard session from the service layer
func SetDashboardSessionForProject(projectID, ocSessionID, agentSessionID, model string) {
	setDashboardSession(projectID, &dashboardSessionInfo{
		OpenCodeSessionID: ocSessionID,
		AgentSessionID:    agentSessionID,
		Model:             model,
	})
}

func (h *DashboardHandler) Input(c *gin.Context) {
	// Dashboard is the human's channel to direct the Maintain Agent. Non-human
	// agents must NOT be able to change direction / milestones / tasks this way,
	// otherwise any compromised or misaligned agent could rewrite the project
	// plan. Only callers flagged is_human=true may use this endpoint.
	if !callerIsHuman(c) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "HUMAN_ONLY", "message": "Dashboard input is reserved for human users"}})
		return
	}

	var req struct {
		TargetBlock string `json:"target_block" binding:"required"`
		Content     string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if req.TargetBlock != "direction" && req.TargetBlock != "milestone" && req.TargetBlock != "task" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "target_block must be direction, milestone, or task"}})
		return
	}

	projectID := c.Query("project_id")
	inputID := model.GenerateID("inp")
	agentIDRaw, _ := c.Get("agent_id")
	agentID, _ := agentIDRaw.(string)

	input := &DashboardInput{
		InputID:     inputID,
		TargetBlock: req.TargetBlock,
		Content:     req.Content,
		ProjectID:   projectID,
		AgentID:     agentID,
	}

	// Check for existing active dashboard session (multi-round dialogue)
	existingSession := getDashboardSession(projectID)

	if existingSession != nil && existingSession.OpenCodeSessionID != "" {
		// Multi-round: send follow-up message to existing serve session
		scheduler := opencode.DefaultScheduler
		if scheduler != nil {
			msgResp, err := scheduler.SendToExistingSession(
				existingSession.OpenCodeSessionID,
				req.Content,
				"maintain",
				existingSession.Model,
			)
			if err != nil {
				log.Printf("[Dashboard] Failed to send follow-up message: %v", err)
				c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to send message to agent"}})
				return
			}

			// Extract text response from agent
			var agentText string
			for _, part := range msgResp.Parts {
				if part.Type == "text" {
					agentText += part.Text
				}
			}

			storePendingInput(input)

			// Broadcast chat update to frontend via SSE
			service.BroadcastEvent(projectID, "CHAT_UPDATE", gin.H{
				"role":    "agent",
				"content": agentText,
			})

			c.JSON(200, gin.H{
				"success": true,
				"data": gin.H{
					"input_id":          inputID,
					"block_type":        req.TargetBlock,
					"status":            "processing",
					"session_active":    true,
					"agent_response":    agentText,
					"opencode_session_id": existingSession.OpenCodeSessionID,
				},
			})
			return
		}
	}

	// No active session: trigger maintain agent (creates new serve session)
	if req.TargetBlock == "task" {
		go func() {
			service.TriggerMaintainAgent(projectID, "dashboard_task_input", req.Content)
		}()
		storePendingInput(input)
		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"input_id":    inputID,
				"block_type":  req.TargetBlock,
				"status":      "processing",
				"session_active": true,
			},
		})
		return
	}

	if req.TargetBlock == "direction" {
		storePendingInput(input)
		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"input_id":         inputID,
				"block_type":       req.TargetBlock,
				"status":           "pending_confirmation",
				"requires_confirm":  true,
			},
		})
		return
	}

	storePendingInput(input)
	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"input_id":    inputID,
		"block_type":  req.TargetBlock,
		"status":      "pending_confirmation",
		},
	})
}

func (h *DashboardHandler) Confirm(c *gin.Context) {
	if !callerIsHuman(c) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "HUMAN_ONLY", "message": "Dashboard confirm is reserved for human users"}})
		return
	}

	var req struct {
		InputID   string `json:"input_id" binding:"required"`
		Confirmed bool   `json:"confirmed"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	input, ok := takePendingInput(req.InputID)
	if !ok {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "Input not found or expired"}})
		return
	}

	// Ownership check: only the agent who created this input may confirm it.
	agentIDRaw, _ := c.Get("agent_id")
	aid, _ := agentIDRaw.(string)
	if input.AgentID != "" && input.AgentID != aid {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "FORBIDDEN", "message": "Only the creator can confirm this input"}})
		return
	}

	if !req.Confirmed {
		deletePendingInput(req.InputID)
		c.JSON(200, gin.H{"success": true, "data": gin.H{"input_id": req.InputID, "action": "cancelled"}})
		return
	}

	projectID := c.Query("project_id")
	if projectID == "" {
		projectID = input.ProjectID
	}

	var cb model.ContentBlock
	result := model.DB.Where("project_id = ? AND block_type = ?", projectID, input.TargetBlock).First(&cb)
	if result.Error != nil {
		cb = model.ContentBlock{
			ID:        model.GenerateID("cb"),
			ProjectID: projectID,
			BlockType: input.TargetBlock,
			Content:   input.Content,
			Version:   1,
		}
		model.DB.Create(&cb)
	} else {
		cb.Content = input.Content
		cb.Version++
		model.DB.Save(&cb)
	}

	deletePendingInput(req.InputID)

	eventType := "MILESTONE_UPDATE"
	if input.TargetBlock == "direction" {
		eventType = "DIRECTION_CHANGE"
	}
	service.BroadcastEvent(projectID, eventType, gin.H{
		"block_type": input.TargetBlock,
		"content":    input.Content,
		"reason":     "dashboard confirm",
	})

	// Auto-clear the dashboard session context after confirmation
	existingSession := getDashboardSession(projectID)
	if existingSession != nil && existingSession.OpenCodeSessionID != "" {
		scheduler := opencode.DefaultScheduler
		if scheduler != nil {
			if err := scheduler.DeleteServeSession(existingSession.OpenCodeSessionID); err != nil {
				log.Printf("[Dashboard] Failed to delete serve session after confirm: %v", err)
			}
		}
		// Cleanup maintain agent serve session mapping
		var maintainAgent model.Agent
		if model.DB.Where("current_project_id = ? AND status != 'offline' AND name LIKE ?", projectID, "maintain%").First(&maintainAgent).Error == nil {
			if sid := opencode.GetAgentServeSession(maintainAgent.ID); sid == existingSession.OpenCodeSessionID {
				opencode.UnregisterAgentServeSession(maintainAgent.ID)
			}
		}
		clearDashboardSession(projectID)
		agent.DefaultManager.ClearSession(existingSession.AgentSessionID)
		log.Printf("[Dashboard] Auto-cleared session context for project %s after confirm", projectID)

		// Notify frontend that context was cleared
		service.BroadcastEvent(projectID, "CONTEXT_CLEARED", gin.H{
			"reason": "confirmed",
		})
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"input_id":    req.InputID,
			"block_type":  input.TargetBlock,
			"version":     cb.Version,
			"confirmed":   true,
			"context_cleared": existingSession != nil,
		},
	})
}

func (h *DashboardHandler) ClearContext(c *gin.Context) {
	if !callerIsHuman(c) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "HUMAN_ONLY", "message": "Dashboard clear is reserved for human users"}})
		return
	}
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	existingSession := getDashboardSession(projectID)
	if existingSession != nil && existingSession.OpenCodeSessionID != "" {
		scheduler := opencode.DefaultScheduler
		if scheduler != nil {
			scheduler.DeleteServeSession(existingSession.OpenCodeSessionID)
		}
		// Cleanup maintain agent serve session mapping
		var maintainAgent model.Agent
		if model.DB.Where("current_project_id = ? AND status != 'offline' AND name LIKE ?", projectID, "maintain%").First(&maintainAgent).Error == nil {
			if sid := opencode.GetAgentServeSession(maintainAgent.ID); sid == existingSession.OpenCodeSessionID {
				opencode.UnregisterAgentServeSession(maintainAgent.ID)
			}
		}
		agent.DefaultManager.ClearSession(existingSession.AgentSessionID)
		clearDashboardSession(projectID)
	}

	// Broadcast context cleared event
	service.BroadcastEvent(projectID, "CONTEXT_CLEARED", gin.H{
		"reason": "manual",
	})

	c.JSON(200, gin.H{"success": true, "data": gin.H{"project_id": projectID, "cleared": true}})
}

// GetMessages returns the dialogue history for the current dashboard session
func (h *DashboardHandler) GetMessages(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	existingSession := getDashboardSession(projectID)
	if existingSession == nil || existingSession.OpenCodeSessionID == "" {
		c.JSON(200, gin.H{"success": true, "data": gin.H{"messages": []interface{}{}}})
		return
	}

	scheduler := opencode.DefaultScheduler
	if scheduler == nil {
		c.JSON(200, gin.H{"success": true, "data": gin.H{"messages": []interface{}{}}})
		return
	}

	messages, err := scheduler.GetSessionMessages(existingSession.OpenCodeSessionID)
	if err != nil {
		log.Printf("[Dashboard] Failed to get messages: %v", err)
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to get messages"}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"messages": messages}})
}