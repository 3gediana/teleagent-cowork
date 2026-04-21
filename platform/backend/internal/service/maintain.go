package service

import (
	"log"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/opencode"
	"github.com/a3c/platform/internal/repo"
)

// DashboardSessionCallback is called when a maintain agent session is ready for dashboard multi-round dialogue
var DashboardSessionCallback func(projectID, ocSessionID, agentSessionID, model string)

func TriggerMaintainAgent(projectID string, trigger string, inputContent string) error {
	direction, _ := repo.GetContentBlock(projectID, "direction")
	milestone, _ := repo.GetCurrentMilestone(projectID)
	version, _ := repo.GetContentBlock(projectID, "version")
	tasks, _ := repo.GetTasksByProject(projectID)
	locks, _ := repo.GetLocksByProject(projectID)

	directionContent := ""
	if direction != nil {
		directionContent = direction.Content
	}
	milestoneContent := ""
	if milestone != nil {
		milestoneContent = milestone.Name + "\n" + milestone.Description
	}
	versionContent := "v1.0"
	if version != nil {
		versionContent = version.Content
	}

	taskList := ""
	for i, t := range tasks {
		assignee := "unassigned"
		if t.AssigneeID != nil {
			var a model.Agent
			if model.DB.Where("id = ?", *t.AssigneeID).First(&a).Error == nil {
				assignee = a.Name
			}
		}
		taskList += "- " + t.Name + " [" + t.Status + "] (priority: " + t.Priority + ", assignee: " + assignee + ")"
		if t.Description != "" {
			taskList += " - " + t.Description
		}
		if i < len(tasks)-1 {
			taskList += "\n"
		}
	}

	lockList := ""
	for _, l := range locks {
		agentName := l.AgentID
		var a model.Agent
		if model.DB.Where("id = ?", l.AgentID).First(&a).Error == nil {
			agentName = a.Name
		}
		lockList += "- " + agentName + " locked files for: " + l.Reason + "\n"
	}

	ctx := &agent.SessionContext{
		DirectionBlock: directionContent,
		MilestoneBlock: milestoneContent,
		TaskList:      taskList,
		Version:       versionContent,
		InputContent:  inputContent,
		TriggerReason: trigger,
		LockList:      lockList,
	}

	session := agent.DefaultManager.CreateSession(agent.RoleMaintain, projectID, ctx, trigger)
	log.Printf("[Maintain] Created session %s for project %s, trigger=%s", session.ID, projectID, trigger)

	agent.DispatchSession(session)

	// Register the serve session for dashboard multi-round dialogue
	// The OpenCodeSessionID will be set by the scheduler after the serve session is created
	go func() {
		scheduler := opencode.DefaultScheduler
		for i := 0; i < 30; i++ {
			updated := agent.DefaultManager.GetSession(session.ID)
			if updated != nil && updated.OpenCodeSessionID != "" {
				modelStr := "minimax-coding-plan/MiniMax-M2.7"
				if scheduler != nil {
					modelStr = scheduler.GetModelString()
				}
				if DashboardSessionCallback != nil {
					DashboardSessionCallback(projectID, updated.OpenCodeSessionID, session.ID, modelStr)
				}
				// Also register in agentServeSessionMap for poll injection
				// Find the maintain agent for this project by name pattern
				var maintainAgent model.Agent
				if model.DB.Where("current_project_id = ? AND status != 'offline' AND name LIKE ?", projectID, "maintain%").
					Order("last_heartbeat DESC").First(&maintainAgent).Error == nil {
					opencode.RegisterAgentServeSession(maintainAgent.ID, updated.OpenCodeSessionID)
					log.Printf("[Maintain] Registered agent %s (%s) serve session %s for poll injection", maintainAgent.ID, maintainAgent.Name, updated.OpenCodeSessionID)
				}
				log.Printf("[Maintain] Registered dashboard session for project %s: ocSession=%s", projectID, updated.OpenCodeSessionID)
				return
			}
			// If session completed without OpenCodeSessionID (legacy fallback), skip registration
			if updated != nil && (updated.Status == "completed" || updated.Status == "failed") {
				return
			}
			time.Sleep(time.Second)
		}
		log.Printf("[Maintain] Timeout waiting for OpenCodeSessionID for project %s", projectID)
	}()

	return nil
}

func TriggerConsultAgent(projectID string, query string) (*agent.Session, error) {
	direction, _ := repo.GetContentBlock(projectID, "direction")
	milestone, _ := repo.GetCurrentMilestone(projectID)
	version, _ := repo.GetContentBlock(projectID, "version")
	tasks, _ := repo.GetTasksByProject(projectID)
	locks, _ := repo.GetLocksByProject(projectID)

	directionContent := ""
	if direction != nil {
		directionContent = direction.Content
	}
	milestoneContent := ""
	if milestone != nil {
		milestoneContent = milestone.Name + "\n" + milestone.Description
	}
	versionContent := "v1.0"
	if version != nil {
		versionContent = version.Content
	}

	taskList := ""
	for i, t := range tasks {
		assignee := "unassigned"
		if t.AssigneeID != nil {
			var a model.Agent
			if model.DB.Where("id = ?", *t.AssigneeID).First(&a).Error == nil {
				assignee = a.Name
			}
		}
		taskList += "- " + t.Name + " [" + t.Status + "] (priority: " + t.Priority + ", assignee: " + assignee + ")"
		if t.Description != "" {
			taskList += " - " + t.Description
		}
		if i < len(tasks)-1 {
			taskList += "\n"
		}
	}

	lockList := ""
	for _, l := range locks {
		agentName := l.AgentID
		var a model.Agent
		if model.DB.Where("id = ?", l.AgentID).First(&a).Error == nil {
			agentName = a.Name
		}
		lockList += "- " + agentName + " locked files for: " + l.Reason + "\n"
	}

	repoPath := DataPath + "/" + projectID + "/repo"

	ctx := &agent.SessionContext{
		DirectionBlock: directionContent,
		MilestoneBlock: milestoneContent,
		TaskList:      taskList,
		Version:       versionContent,
		InputContent:   query,
		TriggerReason: "project_info",
		LockList:      lockList,
		ProjectPath:   repoPath,
	}

	session := agent.DefaultManager.CreateSession(agent.RoleConsult, projectID, ctx, "project_info")
	log.Printf("[Consult] Created session %s for project %s", session.ID, projectID)

	agent.DispatchSession(session)

	return session, nil
}

func TriggerAssessAgent(projectID string, projectPath string) (*agent.Session, error) {
	ctx := &agent.SessionContext{
		ProjectPath: projectPath,
	}

session := agent.DefaultManager.CreateSession(agent.RoleAssess, projectID, ctx, "project_import")
	log.Printf("[Assess] Created session %s for project %s", session.ID, projectPath)

	agent.DispatchSession(session)

	return session, nil
}