package service

import (
	"log"
	"strings"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
)

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

	// Dashboard-originated inputs feed into the Maintain dialogue
	// channel so follow-ups see the conversation so far. Non-dashboard
	// triggers (periodic timer, milestone_complete, etc.) skip the
	// history loader — they're one-shot bookkeeping, not chat.
	isDashboardTrigger := strings.HasPrefix(trigger, "dashboard_")
	effectiveInput := inputContent
	if isDashboardTrigger {
		AppendDialogueMessage(projectID, DialogueChannelMaintain, "", DialogueRoleUser, inputContent)
		if history := BuildDialogueHistoryForPrompt(projectID, DialogueChannelMaintain); history != "" {
			effectiveInput = history + "\n---\n" + inputContent
		}
	}

	ctx := &agent.SessionContext{
		DirectionBlock: directionContent,
		MilestoneBlock: milestoneContent,
		TaskList:      taskList,
		Version:       versionContent,
		InputContent:  effectiveInput,
		TriggerReason: trigger,
		LockList:      lockList,
		// Maintain reads MILESTONE.md / DIRECTION.md / source via the
		// same builtin file tools as the other roles. Rooting at the
		// project dir (not /repo) lets it see both meta files and the
		// ./repo source tree.
		ProjectPath: GetProjectPath(projectID),
	}

	session := agent.DefaultManager.CreateSession(agent.RoleMaintain, projectID, ctx, trigger)
	log.Printf("[Maintain] Created session %s for project %s, trigger=%s", session.ID, projectID, trigger)

	agent.DispatchSession(session)
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