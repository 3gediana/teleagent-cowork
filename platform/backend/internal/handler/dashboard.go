package handler

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

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
			"id":           a.ID,
			"name":         a.Name,
			"status":       a.Status,
			"current_task": currentTask,
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
		"version": "v1.0",
		"tasks":   taskList,
		"locks":   lockList,
		"agents":  agentList,
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
	InputID     string `json:"input_id"`
	TargetBlock string `json:"target_block"`
	Content     string `json:"content"`
	ProjectID   string `json:"-"`
	Confirmed   bool   `json:"confirmed"`
}

var pendingInputs = make(map[string]*DashboardInput)

func (h *DashboardHandler) Input(c *gin.Context) {
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

	input := &DashboardInput{
		InputID:     inputID,
		TargetBlock: req.TargetBlock,
		Content:     req.Content,
		ProjectID:   projectID,
	}

	if req.TargetBlock == "task" {
		go func() {
			service.TriggerMaintainAgent(projectID, "dashboard_task_input", req.Content)
		}()
		pendingInputs[inputID] = input
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
		pendingInputs[inputID] = input
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

	pendingInputs[inputID] = input
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
	var req struct {
		InputID   string `json:"input_id" binding:"required"`
		Confirmed bool   `json:"confirmed"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	input, ok := pendingInputs[req.InputID]
	if !ok {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "Input not found or expired"}})
		return
	}

	if !req.Confirmed {
		delete(pendingInputs, req.InputID)
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

	delete(pendingInputs, req.InputID)

	eventType := "MILESTONE_UPDATE"
	if input.TargetBlock == "direction" {
		eventType = "DIRECTION_CHANGE"
	}
	service.BroadcastEvent(projectID, eventType, gin.H{
		"block_type": input.TargetBlock,
		"content":    input.Content,
		"reason":     "dashboard confirm",
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"input_id":    req.InputID,
			"block_type":  input.TargetBlock,
			"version":     cb.Version,
			"confirmed":   true,
		},
	})
}

func (h *DashboardHandler) ClearContext(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	agent.DefaultManager.ClearSession(req.SessionID)

	c.JSON(200, gin.H{"success": true, "data": gin.H{"session_id": req.SessionID, "cleared": true}})
}