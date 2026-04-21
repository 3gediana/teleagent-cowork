package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/opencode"
	"github.com/a3c/platform/internal/service"
)

type StatusHandler struct{}

func NewStatusHandler() *StatusHandler {
	return &StatusHandler{}
}

func (h *StatusHandler) Sync(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	agent, _ := repoGetAgentByID(agentID.(string))
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
	if err := model.DB.Where("assignee_id = ? AND status = 'claimed'", agentID.(string)).First(&myTask).Error; err == nil {
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
	agentID, _ := c.Get("agent_id")
	agent, _ := repoGetAgentByID(agentID.(string))

	now := time.Now()
	heartbeatOk := false
	if agent != nil {
		agent.LastHeartbeat = &now
		agent.Status = "online"
		model.DB.Save(agent)
		model.RDB.Set(model.DB.Statement.Context, "a3c:agent:"+agent.ID+":heartbeat", now.Unix(), 300*time.Second)
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

	// Inject important broadcast messages into agent's serve session for real-time awareness
	// This lets the agent "see" project changes without needing a separate tool call
	if len(messages) > 0 && agent != nil {
		ocSessionID := opencode.GetAgentServeSession(agent.ID)
		if ocSessionID != "" {
			scheduler := opencode.DefaultScheduler
			if scheduler != nil {
				for _, msg := range messages {
					header, _ := msg["header"].(gin.H)
					eventType, _ := header["type"].(string)
					// Only inject state-change events, not chat/tool noise
					if isImportantForAgent(eventType) {
						payload, _ := msg["payload"].(gin.H)
						injectText := fmt.Sprintf("[Project Update] %s: %v", eventType, payload)
						_, err := scheduler.SendToExistingSession(ocSessionID, injectText, "maintain", "", true)
						if err != nil {
							log.Printf("[Poll] Failed to inject %s into session %s: %v", eventType, ocSessionID, err)
						}
					}
				}
			}
		}
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"messages":     messages,
			"heartbeat_ok": heartbeatOk,
		},
	})
}

// isImportantForAgent filters which broadcast events should be injected into the agent's serve session
func isImportantForAgent(eventType string) bool {
	switch eventType {
	case "DIRECTION_CHANGE", "MILESTONE_UPDATE", "MILESTONE_SWITCH",
		"VERSION_UPDATE", "VERSION_ROLLBACK",
		"TASK_CLAIMED", "TASK_COMPLETED",
		"FILE_LOCKED", "FILE_UNLOCKED",
		"AUDIT_RESULT", "CHANGE_PENDING_CONFIRM":
		return true
	}
	return false
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