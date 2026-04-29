package handler

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
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

// Multi-round dashboard dialogue is now stored in
// model.DialogueMessage (see @platform/backend/internal/service/dialogue.go).
// The old dashboardSessionInfo / dashboardSessions map cached an
// opencode serve session id per project; with the native runner that
// cache is unnecessary — every Input dispatches a fresh agent session
// and the conversation is rehydrated from the DB on the next turn.
//
// `sync` import is still needed for pendingInputsMu declared below.
var _ sync.RWMutex // keep the import to avoid a churn in go vet output

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

	// Task inputs route to the Maintain agent. Multi-round context
	// is handled inside TriggerMaintainAgent: it persists this turn
	// to DialogueMessage, reads prior turns, and prefixes them into
	// the prompt. Reply delivery is asynchronous — the frontend
	// listens for CHAT_UPDATE on SSE.
	if req.TargetBlock == "task" {
		go func() {
			if err := service.TriggerMaintainAgent(projectID, "dashboard_task_input", req.Content); err != nil {
				log.Printf("[Dashboard] TriggerMaintainAgent failed: %v", err)
			}
		}()
		storePendingInput(input)
		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"input_id":       inputID,
				"block_type":     req.TargetBlock,
				"status":         "processing",
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
		model.SaveOrLog(&cb, "handler/dashboard-confirm")
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

	// Auto-clear the Maintain dialogue transcript so the next user
	// message starts fresh. The native runner is stateless per
	// session anyway — only the persisted history would leak stale
	// context if left behind.
	cleared := service.ClearDialogue(projectID, service.DialogueChannelMaintain)
	if cleared > 0 {
		service.BroadcastEvent(projectID, "CONTEXT_CLEARED", gin.H{
			"reason":        "confirmed",
			"channel":       service.DialogueChannelMaintain,
			"rows_affected": cleared,
		})
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"input_id":        req.InputID,
			"block_type":      input.TargetBlock,
			"version":         cb.Version,
			"confirmed":       true,
			"context_cleared": cleared > 0,
		},
	})
}

// ClearContext wipes the dialogue transcript for a given channel on
// a project. ?channel defaults to "maintain" (the main dashboard tab);
// pass ?channel=chief to clear the Chief chat instead.
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
	channel := c.Query("channel")
	if channel == "" {
		channel = service.DialogueChannelMaintain
	}

	cleared := service.ClearDialogue(projectID, channel)

	service.BroadcastEvent(projectID, "CONTEXT_CLEARED", gin.H{
		"reason":        "manual",
		"channel":       channel,
		"rows_affected": cleared,
	})

	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"project_id":    projectID,
		"channel":       channel,
		"rows_affected": cleared,
		"cleared":       true,
	}})
}

// GetMessages returns the dialogue history for a project + channel.
// Shape is { role, content, created_at, session_id } entries in
// chronological order — the frontend renders them directly.
//
// ?channel defaults to "maintain" for backwards compatibility with
// the old /dashboard/messages callers; pass ?channel=chief to fetch
// the Chief chat transcript.
func (h *DashboardHandler) GetMessages(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}
	channel := c.Query("channel")
	if channel == "" {
		channel = service.DialogueChannelMaintain
	}
	rows := service.LoadRecentDialogue(projectID, channel, 100)
	messages := make([]gin.H, 0, len(rows))
	for _, m := range rows {
		messages = append(messages, gin.H{
			"id":         m.ID,
			"role":       m.Role,
			"content":    m.Content,
			"session_id": m.SessionID,
			"created_at": m.CreatedAt,
		})
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"channel":  channel,
		"messages": messages,
	}})
}