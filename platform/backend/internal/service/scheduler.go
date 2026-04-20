package service

import (
	"log"
	"time"

	"github.com/a3c/platform/internal/model"
)

var maintainTimerRunning = false

func StartMaintainTimer() {
	if maintainTimerRunning {
		return
	}
	maintainTimerRunning = true

	go func() {
		ticker := time.NewTicker(20 * time.Minute)
		defer ticker.Stop()

		log.Printf("[Maintain] 20-minute timer started")

		for range ticker.C {
			var projects []model.Project
			model.DB.Where("status = 'ready' OR status = 'idle'").Find(&projects)

			for _, project := range projects {
				TriggerMaintainAgent(project.ID, "periodic_20min", "")
				log.Printf("[Maintain] Periodic trigger for project %s (%s)", project.ID, project.Name)
			}
		}
	}()
}

func StartHeartbeatChecker() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		log.Printf("[Heartbeat] Background checker started (5-minute timeout)")

		for range ticker.C {
			var agents []model.Agent
			model.DB.Where("status = 'online'").Find(&agents)

			now := time.Now()
			for _, a := range agents {
				if a.LastHeartbeat == nil {
					continue
				}

				elapsed := now.Sub(*a.LastHeartbeat)
				if elapsed > 5*time.Minute {
					log.Printf("[Heartbeat] Agent %s (%s) timed out, releasing resources", a.ID, a.Name)

					a.Status = "offline"
					model.DB.Save(&a)

					var locks []model.FileLock
					model.DB.Where("agent_id = ? AND released_at IS NULL", a.ID).Find(&locks)
					for i := range locks {
						locks[i].ReleasedAt = &now
						model.DB.Save(&locks[i])
					}

					var tasks []model.Task
					model.DB.Where("assignee_id = ? AND status = 'claimed'", a.ID).Find(&tasks)
					for i := range tasks {
						tasks[i].Status = "pending"
						tasks[i].AssigneeID = nil
						model.DB.Save(&tasks[i])
					}

					model.RDB.Del(model.DB.Statement.Context, "a3c:agent:"+a.ID+":heartbeat")
				}
			}
		}
	}()
}

func EnforceAgentLimit(projectID string) error {
	var count int64
	model.DB.Model(&model.Agent{}).Where("current_project_id = ? AND status != 'offline'", projectID).Count(&count)
	if count >= 6 {
		return &AgentLimitError{Limit: 6, Current: int(count)}
	}
	return nil
}

type AgentLimitError struct {
	Limit   int
	Current int
}

func (e *AgentLimitError) Error() string {
	return "project is full"
}

func GetProjectAgentCount(projectID string) int {
	var count int64
	model.DB.Model(&model.Agent{}).Where("current_project_id = ?", projectID).Count(&count)
	return int(count)
}

func GetPendingOfflineMessages(agentID string) []string {
	key := "a3c:offline:" + agentID + ":messages"
	messages, err := model.RDB.LRange(model.DB.Statement.Context, key, 0, -1).Result()
	if err != nil {
		return nil
	}
	return messages
}

func StoreOfflineMessage(agentID string, message string) {
	key := "a3c:offline:" + agentID + ":messages"
	model.RDB.LPush(model.DB.Statement.Context, key, message)
	model.RDB.Expire(model.DB.Statement.Context, key, 24*time.Hour)
}
