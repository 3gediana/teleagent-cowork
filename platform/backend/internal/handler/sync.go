package handler

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"
)

type StatusHandler struct{}

func NewStatusHandler() *StatusHandler {
	return &StatusHandler{}
}

func (h *StatusHandler) Sync(c *gin.Context) {
	agentIDRaw, _ := c.Get("agent_id")
	agentID, ok := agentIDRaw.(string)
	if !ok || agentID == "" || agentID == "human" {
		c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "AUTH_INVALID_KEY", "message": "Agent not found"}})
		return
	}
	agent, _ := repoGetAgentByID(agentID)
	if agent == nil || agent.CurrentProjectID == nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "No project selected"}})
		return
	}

	projectID := *agent.CurrentProjectID

	direction, _ := repoGetContentBlock(projectID, "direction")
	milestone, _ := repoGetCurrentMilestone(projectID)
	version, _ := repoGetContentBlock(projectID, "version")
	tasks, _ := repoGetTasksByProject(projectID)
	locks, _ := repoGetLocksByProject(projectID)

	taskList := make([]gin.H, 0)
	for _, t := range tasks {
		assigneeName := ""
		if t.AssigneeID != nil {
			a, _ := repoGetAgentByID(*t.AssigneeID)
			if a != nil {
				assigneeName = a.Name
			}
		}
		taskItem := gin.H{
			"id":            t.ID,
			"name":          t.Name,
			"description":   t.Description,
			"status":        t.Status,
			"assignee_name": assigneeName,
			"priority":      t.Priority,
		}
		if t.MilestoneID != nil {
			taskItem["milestone_id"] = *t.MilestoneID
		}
		taskList = append(taskList, taskItem)
	}

	lockList := make([]gin.H, 0)
	for _, l := range locks {
		var files []string
		json.Unmarshal([]byte(l.Files), &files)
		lockList = append(lockList, gin.H{
			"lock_id":     l.ID,
			"task_id":     l.TaskID,
			"agent_name":  syncGetAgentName(l.AgentID),
			"files":       files,
			"reason":      l.Reason,
			"acquired_at": l.AcquiredAt.Format(time.RFC3339),
			"expires_at":  l.ExpiresAt.Format(time.RFC3339),
		})
	}

	data := gin.H{
		"version": "v1.0",
		"tasks":   taskList,
		"locks":   lockList,
	}
	if direction != nil {
		data["direction"] = direction.Content
	}
	if milestone != nil {
		data["milestone"] = milestone.Name
	}
	if version != nil {
		data["version"] = version.Content
	}

	// Add current agent's claimed task
	var myTask model.Task
	if err := model.DB.Where("assignee_id = ? AND status = 'claimed'", agentID).First(&myTask).Error; err == nil {
		data["my_task"] = gin.H{
			"id":          myTask.ID,
			"name":        myTask.Name,
			"description": myTask.Description,
			"priority":    myTask.Priority,
			"status":      myTask.Status,
		}
	}

	c.JSON(200, gin.H{"success": true, "data": data})
}

func (h *StatusHandler) Poll(c *gin.Context) {
	agentIDRaw, _ := c.Get("agent_id")
	agentID, ok := agentIDRaw.(string)
	if !ok || agentID == "" || agentID == "human" {
		c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "AUTH_INVALID_KEY", "message": "Agent not found"}})
		return
	}
	agent, _ := repoGetAgentByID(agentID)

	now := time.Now()
	heartbeatOk := false
	if agent != nil {
		agent.LastHeartbeat = &now
		agent.Status = "online"
		model.DB.Save(agent)
		model.RDB.Set(model.DB.Statement.Context, "a3c:agent:"+agent.ID+":heartbeat", now.Unix(), 7*time.Minute)
		heartbeatOk = true
	}

	projectID := ""
	if agent != nil && agent.CurrentProjectID != nil {
		projectID = *agent.CurrentProjectID
	}

	messages := make([]gin.H, 0)
	if projectID != "" {
		broadcasts := service.SSEManager.GetUnackedBroadcasts(projectID, agent.ID)
		for _, msg := range broadcasts {
			messages = append(messages, gin.H{
				"header":  msg.Header,
				"payload": msg.Payload,
				"meta":    msg.Meta,
			})
		}
	}

	// Also fetch directed messages (e.g. audit results) for this agent
	directedMessages := service.GetDirectedMessages(agentID)
	for _, dm := range directedMessages {
		messages = append(messages, dm)
	}

	// Note: the native runner is stateless per session, so we no
	// longer inject "[Project Updates]" into a live agent serve
	// session here. Agents receive the same events through the SSE
	// feed above and through their normal context rebuild on the
	// next task. If we reintroduce a long-lived agent runtime later,
	// this is the obvious place to hook a refreshed notification
	// path (by session id, not opencode-specific ids).

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"messages":     messages,
			"heartbeat_ok": heartbeatOk,
		},
	})
}

func syncGetAgentName(agentID string) string {
	var agent model.Agent
	if err := model.DB.Where("id = ?", agentID).First(&agent).Error; err != nil {
		return agentID
	}
	return agent.Name
}

func repoGetAgentByID(id string) (*model.Agent, error) {
	var a model.Agent
	if err := model.DB.Where("id = ?", id).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func repoGetContentBlock(projectID, blockType string) (*model.ContentBlock, error) {
	var cb model.ContentBlock
	if err := model.DB.Where("project_id = ? AND block_type = ?", projectID, blockType).First(&cb).Error; err != nil {
		return nil, err
	}
	return &cb, nil
}

func repoGetCurrentMilestone(projectID string) (*model.Milestone, error) {
	var m model.Milestone
	if err := model.DB.Where("project_id = ? AND status = 'in_progress'", projectID).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func repoGetTasksByProject(projectID string) ([]model.Task, error) {
	var tasks []model.Task
	err := model.DB.Where("project_id = ? AND status != 'deleted'", projectID).Find(&tasks).Error
	return tasks, err
}

func repoGetLocksByProject(projectID string) ([]model.FileLock, error) {
	var locks []model.FileLock
	err := model.DB.Where("project_id = ? AND released_at IS NULL AND expires_at > ?", projectID, time.Now()).Find(&locks).Error
	return locks, err
}