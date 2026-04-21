package service

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/opencode"
	"github.com/a3c/platform/internal/repo"
)

// TriggerChiefDecision triggers the Chief Agent to make a decision.
// decisionType: "pr_review", "pr_merge", "milestone_switch", etc.
// targetID: the ID of the entity being decided on (PR ID, milestone ID, etc.)
func TriggerChiefDecision(projectID string, decisionType string, targetID string) {
	ctx := buildChiefContext(projectID, decisionType, targetID)
	session := agent.DefaultManager.CreateSession(agent.RoleChief, projectID, ctx, "chief_decision_"+decisionType)
	session.PRID = targetID // reuse PRID field for the target entity

	log.Printf("[Chief] Created decision session %s for project %s, type=%s, target=%s", session.ID, projectID, decisionType, targetID)

	agent.DispatchSession(session)

	// Register serve session for multi-round dialogue when available
	go registerChiefServeSession(session.ID, projectID)
}

// TriggerChiefChat triggers the Chief Agent for a human conversation.
func TriggerChiefChat(projectID string, inputContent string) {
	ctx := buildChiefContext(projectID, "chat", "")
	ctx.InputContent = inputContent

	session := agent.DefaultManager.CreateSession(agent.RoleChief, projectID, ctx, "chief_request")
	log.Printf("[Chief] Created chat session %s for project %s", session.ID, projectID)

	agent.DispatchSession(session)

	// Register serve session for multi-round dialogue when available
	go registerChiefServeSession(session.ID, projectID)
}

// ChiefSessionReady is called when a Chief agent session's OpenCode serve session is available
var ChiefSessionReady func(projectID, ocSessionID, agentSessionID, model string)

func registerChiefServeSession(sessionID, projectID string) {
	scheduler := opencode.DefaultScheduler
	for i := 0; i < 30; i++ {
		updated := agent.DefaultManager.GetSession(sessionID)
		if updated != nil && updated.OpenCodeSessionID != "" {
			modelStr := "minimax-coding-plan/MiniMax-M2.7"
			if scheduler != nil {
				modelStr = scheduler.GetModelString()
			}
			if ChiefSessionReady != nil {
				ChiefSessionReady(projectID, updated.OpenCodeSessionID, sessionID, modelStr)
			}
			log.Printf("[Chief] Registered serve session for project %s: ocSession=%s", projectID, updated.OpenCodeSessionID)
			return
		}
		if updated != nil && (updated.Status == "completed" || updated.Status == "failed") {
			return
		}
		time.Sleep(time.Second)
	}
	log.Printf("[Chief] Timeout waiting for OpenCodeSessionID for project %s", projectID)
}

// buildChiefContext assembles the full platform state snapshot for the Chief Agent.
func buildChiefContext(projectID string, decisionType string, targetID string) *agent.SessionContext {
	direction, _ := repo.GetContentBlock(projectID, "direction")
	milestone, _ := repo.GetCurrentMilestone(projectID)
	version, _ := repo.GetContentBlock(projectID, "version")
	tasks, _ := repo.GetTasksByProject(projectID)

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

	// Task summary
	taskList := ""
	pendingCount := 0
	inProgressCount := 0
	completedCount := 0
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
		switch t.Status {
		case "pending":
			pendingCount++
		case "claimed":
			inProgressCount++
		case "completed":
			completedCount++
		}
	}

	// Agent status
	var agents []model.Agent
	model.DB.Where("current_project_id = ?", projectID).Find(&agents)
	agentList := ""
	for _, a := range agents {
		currentTask := ""
		if a.Status == "online" {
			var task model.Task
			if model.DB.Where("assignee_id = ? AND status = 'claimed'", a.ID).First(&task).Error == nil {
				currentTask = " (doing: " + task.Name + ")"
			}
		}
		agentList += "- " + a.Name + " [" + a.Status + "]" + currentTask + "\n"
	}

	// Active policies
	var policies []model.Policy
	model.DB.Where("status = 'active'").Order("priority DESC").Find(&policies)
	policyList := ""
	for _, p := range policies {
		policyList += "- [" + p.Source + "] " + p.Name + ": match=" + p.MatchCondition + " actions=" + p.Actions + "\n"
	}

	// PR status for this project
	var prs []model.PullRequest
	model.DB.Where("project_id = ?", projectID).Order("created_at DESC").Limit(10).Find(&prs)
	prList := ""
	for _, pr := range prs {
		prList += "- PR " + pr.ID + ": " + pr.Title + " [" + pr.Status + "]\n"
	}

	// Recent audit results
	var recentChanges []model.Change
	model.DB.Where("project_id = ? AND reviewed_at IS NOT NULL", projectID).
		Order("created_at DESC").Limit(10).Find(&recentChanges)
	auditList := ""
	for _, ch := range recentChanges {
		level := ""
		if ch.AuditLevel != nil {
			level = *ch.AuditLevel
		}
		fm := ""
		if ch.FailureMode != "" {
			fm = " (" + ch.FailureMode + ")"
		}
		auditList += "- Change " + ch.ID + ": " + level + fm + "\n"
	}

	// Pending actions
	pendingActions := ""
	if pendingCount > 0 {
		pendingActions += fmt.Sprintf("- %d tasks pending (no agent claimed)\n", pendingCount)
	}
	// PRs waiting for human action
	for _, pr := range prs {
		if pr.Status == "pending_human_review" {
			pendingActions += "- PR " + pr.ID + " waiting for review approval\n"
		}
		if pr.Status == "pending_human_merge" {
			pendingActions += "- PR " + pr.ID + " waiting for merge approval\n"
		}
	}

	// AutoMode status
	var project model.Project
	autoMode := false
	if model.DB.Where("id = ?", projectID).First(&project).Error == nil {
		autoMode = project.AutoMode
	}

	// Build global state string
	globalState := fmt.Sprintf(
		"## 平台全局状态\n\n### 项目\n- 方向: %s\n- 当前里程碑: %s\n- 版本: %s\n- AutoMode: %v\n\n### 任务概览\n- 待领取: %d 个\n- 进行中: %d 个\n- 已完成: %d 个\n%s\n### Agent 状态\n%s\n### 待处理事项\n%s\n### 最近审核结果\n%s\n### PR 状态\n%s\n### 当前策略\n%s",
		directionContent, milestoneContent, versionContent, autoMode,
		pendingCount, inProgressCount, completedCount,
		taskList, agentList, pendingActions, auditList, prList, policyList,
	)

	// Decision-specific context
	decisionContext := ""
	switch decisionType {
	case "pr_review":
		decisionContext = buildPRReviewContext(targetID)
	case "pr_merge":
		decisionContext = buildPRMergeContext(targetID)
	}

	inputContent := ""
	if decisionType != "chat" {
		inputContent = fmt.Sprintf("你需要做一个决策：%s\n\n%s\n\n请根据平台状态和策略，决定是否批准。如果风险高或策略要求人类确认，请拒绝并说明原因。", decisionType, decisionContext)
	}

	return &agent.SessionContext{
		DirectionBlock: directionContent,
		MilestoneBlock: milestoneContent,
		TaskList:      taskList,
		Version:       versionContent,
		InputContent:  inputContent,
		TriggerReason: "chief_decision_" + decisionType,
		GlobalState:   globalState,
		AutoMode:      autoMode,
	}
}

func buildPRReviewContext(prID string) string {
	var pr model.PullRequest
	if model.DB.Where("id = ?", prID).First(&pr).Error != nil {
		return "PR not found"
	}

	// Count files changed from diff_stat
	fileCount := 0
	if pr.DiffStat != "" {
		var statEntries []map[string]interface{}
		if json.Unmarshal([]byte(pr.DiffStat), &statEntries) == nil {
			fileCount = len(statEntries)
		}
	}

	return fmt.Sprintf(
		"PR 详情:\n- ID: %s\n- Title: %s\n- Branch: %s\n- Submitter: %s\n- Files changed: %d\n- Self Review: %s\n\nDiff Stat:\n%s",
		pr.ID, pr.Title, pr.BranchID, pr.SubmitterID, fileCount, pr.SelfReview, pr.DiffStat,
	)
}

func buildPRMergeContext(prID string) string {
	var pr model.PullRequest
	if model.DB.Where("id = ?", prID).First(&pr).Error != nil {
		return "PR not found"
	}

	return fmt.Sprintf(
		"PR 合并决策:\n- ID: %s\n- Title: %s\n- Tech Review: %s\n- Biz Review: %s\n- Conflict Files: %s",
		pr.ID, pr.Title, pr.TechReview, pr.BizReview, pr.ConflictFiles,
	)
}
