package service

import (
	"context"
	"log"
	"time"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service/refinery"
	"gorm.io/gorm"
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

		// The MCP poller heartbeats every 3 minutes (and poll() also bumps the
		// heartbeat on every 5s poll). We give a generous 7-minute grace window
		// here so a brief network hiccup doesn't kick an active agent offline.
		log.Printf("[Heartbeat] Background checker started (7-minute timeout)")

		for range ticker.C {
			var agents []model.Agent
			model.DB.Where("status = 'online'").Find(&agents)

			now := time.Now()
			for _, a := range agents {
				if a.LastHeartbeat == nil {
					continue
				}

				elapsed := now.Sub(*a.LastHeartbeat)
				if elapsed > 7*time.Minute {
					log.Printf("[Heartbeat] Agent %s (%s) timed out, releasing resources", a.ID, a.Name)

					err := model.DB.Transaction(func(tx *gorm.DB) error {
						if err := tx.Model(&model.Agent{}).Where("id = ?", a.ID).Update("status", "offline").Error; err != nil {
							return err
						}
						if err := tx.Model(&model.FileLock{}).
							Where("agent_id = ? AND released_at IS NULL", a.ID).
							Update("released_at", now).Error; err != nil {
							return err
						}
						if err := tx.Model(&model.Task{}).
							Where("assignee_id = ? AND status = 'claimed'", a.ID).
							Updates(map[string]interface{}{"status": "pending", "assignee_id": nil}).Error; err != nil {
							return err
						}
						// Release branch occupant
						if err := tx.Model(&model.Branch{}).
							Where("occupant_id = ? AND status = 'active'", a.ID).
							Update("occupant_id", nil).Error; err != nil {
							return err
						}
						return nil
					})

					if err != nil {
						log.Printf("[Heartbeat] Failed to release resources for agent %s: %v", a.ID, err)
					}

					model.RDB.Del(context.Background(), "a3c:agent:"+a.ID+":heartbeat")
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

var analyzeTimerRunning = false
var refineryTimerRunning = false

// StartRefineryTimer runs the Refinery pipeline weekly for each active project.
// This ensures knowledge artifacts are kept up-to-date without manual triggers.
func StartRefineryTimer() {
	if refineryTimerRunning {
		return
	}
	refineryTimerRunning = true

	go func() {
		ticker := time.NewTicker(7 * 24 * time.Hour) // weekly
		defer ticker.Stop()

		log.Printf("[Refinery] Weekly timer started (first run in 7 days)")

		for range ticker.C {
			runRefineryForAllProjects()
		}
	}()
}

func runRefineryForAllProjects() {
	var projects []model.Project
	model.DB.Where("status = 'ready' OR status = 'idle'").Find(&projects)

	for _, project := range projects {
		var sessionCount int64
		model.DB.Model(&model.AgentSession{}).Where("project_id = ?", project.ID).Count(&sessionCount)
		if sessionCount >= 5 { // only run if there's enough data
			go func(pid string) {
				r := refinery.New()
				if _, err := r.Run(pid, 24*14, "scheduled"); err != nil {
					log.Printf("[Refinery] Scheduled run failed for project %s: %v", pid, err)
				} else {
					log.Printf("[Refinery] Scheduled run completed for project %s", pid)
				}
			}(project.ID)
		}
	}

	// Global promoter: after project runs, consolidate highly-validated
	// artifacts into the cross-project pool. We wait briefly so the
	// per-project goroutines above have a chance to finish first.
	go func() {
		time.Sleep(5 * time.Minute)
		gr := refinery.NewGlobalOnly()
		if _, err := gr.Run("", 24*30, "scheduled_global"); err != nil {
			log.Printf("[Refinery] Scheduled global promotion failed: %v", err)
		} else {
			log.Printf("[Refinery] Scheduled global promotion completed")
		}
	}()
}

// StartAnalyzeTimer runs the Analyze Agent daily for each project with raw experiences.
func StartAnalyzeTimer() {
	if analyzeTimerRunning {
		return
	}
	analyzeTimerRunning = true

	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		log.Printf("[Analyze] Daily timer started (first run in 24h)")

		// Do NOT run immediately on startup. Previously we re-analyzed the
		// same raw experiences on every restart (common in dev) which
		// flooded the agent with low-signal noise. Wait for the first tick.
		for range ticker.C {
			runAnalyzeForAllProjects()
		}
	}()
}

// analyzeMinRawExperiences is the minimum number of new raw experiences before
// Analyze Agent is worth running. Kept conservative to favor signal quality.
const analyzeMinRawExperiences = 10

func runAnalyzeForAllProjects() {
	var projects []model.Project
	model.DB.Where("status = 'ready' OR status = 'idle'").Find(&projects)

	for _, project := range projects {
		var rawCount int64
		model.DB.Model(&model.Experience{}).Where("project_id = ? AND status = ?", project.ID, "raw").Count(&rawCount)
		if rawCount >= analyzeMinRawExperiences {
			TriggerAnalyzeAgent(project.ID)
		}
	}
}
