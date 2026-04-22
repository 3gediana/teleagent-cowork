package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"gorm.io/gorm"
)

// buildChiefQueryText builds the semantic query used to retrieve relevant
// artifacts for the Chief Agent. Chief isn't working on one specific task
// so we describe what the project is actively doing: the current milestone
// plus the names of pending / claimed tasks. This gives the bge encoder a
// representative sample of the "topic space" Chief is about to reason about.
//
// Kept as a separate function so tests can assert stable text without
// spinning up the full chief context.
func buildChiefQueryText(milestoneContent string, tasks []model.Task) string {
	parts := []string{}
	if milestoneContent != "" {
		parts = append(parts, milestoneContent)
	}
	// Only include actionable tasks — completed ones represent done work
	// and aren't what Chief needs to think about next.
	active := 0
	for _, t := range tasks {
		if t.Status != "pending" && t.Status != "claimed" {
			continue
		}
		parts = append(parts, t.Name)
		active++
		if active >= 10 {
			break
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// TriggerChiefDecision triggers the Chief Agent to make a decision.
// decisionType: "pr_review", "pr_merge", "milestone_switch", etc.
// targetID: the ID of the entity being decided on (PR ID, milestone ID, etc.)
//
// Decision sessions don't feed DialogueMessage history — they're
// platform-initiated (AutoMode, policy-triggered), not human chat.
// If you want the decision to appear in the Chief chat transcript, the
// caller should also invoke AppendDialogueMessage with a synthetic
// user-role preamble describing the context.
func TriggerChiefDecision(projectID string, decisionType string, targetID string) {
	ctx := buildChiefContext(projectID, decisionType, targetID)
	session := agent.DefaultManager.CreateSession(agent.RoleChief, projectID, ctx, "chief_decision_"+decisionType)
	session.PRID = targetID // reuse PRID field for the target entity

	log.Printf("[Chief] Created decision session %s for project %s, type=%s, target=%s", session.ID, projectID, decisionType, targetID)

	agent.DispatchSession(session)
}

// TriggerChiefChat triggers the Chief Agent for a human conversation.
// The user's input is persisted into DialogueMessage before dispatch;
// the assistant's reply is appended automatically by
// HandleSessionCompletion when the session finishes. Prior history is
// prepended to the prompt so the model sees multi-round context.
func TriggerChiefChat(projectID string, inputContent string) {
	// Persist the user turn first so the history the agent is about
	// to read includes this very message (the agent won't see it in
	// the "Conversation history" prefix — it lands as InputContent —
	// but subsequent turns will see it).
	AppendDialogueMessage(projectID, DialogueChannelChief, "", DialogueRoleUser, inputContent)

	ctx := buildChiefContext(projectID, "chat", "")
	// Prefix the conversation history (last ~20 turns) so the model
	// has continuity. The current turn goes in as-is below.
	history := BuildDialogueHistoryForPrompt(projectID, DialogueChannelChief)
	if history != "" {
		ctx.InputContent = history + "\n---\n" + inputContent
	} else {
		ctx.InputContent = inputContent
	}

	session := agent.DefaultManager.CreateSession(agent.RoleChief, projectID, ctx, "chief_request")
	log.Printf("[Chief] Created chat session %s for project %s (history turns=%d)", session.ID, projectID, countHistoryTurns(history))

	agent.DispatchSession(session)
}

// countHistoryTurns is a tiny helper for the log line above — just
// counts "**Human:**" / "**You:**" markers so operators can eyeball
// whether multi-round context is flowing. Not used elsewhere.
func countHistoryTurns(history string) int {
	return strings.Count(history, "**Human:**") + strings.Count(history, "**You:**")
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

	// Task summary. Cap how many tasks actually land in the prompt to keep
	// the Chief's context window under control on long-running projects.
	const chiefMaxTasksInPrompt = 30
	taskList := ""
	pendingCount := 0
	inProgressCount := 0
	completedCount := 0
	shownTasks := 0
	for _, t := range tasks {
		switch t.Status {
		case "pending":
			pendingCount++
		case "claimed":
			inProgressCount++
		case "completed":
			completedCount++
		}
		// Prefer to show actionable tasks (pending/claimed) over completed ones.
		if shownTasks >= chiefMaxTasksInPrompt {
			continue
		}
		if t.Status == "completed" && shownTasks >= chiefMaxTasksInPrompt/3 {
			continue
		}
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
		taskList += "\n"
		shownTasks++
	}
	if len(tasks) > shownTasks {
		taskList += fmt.Sprintf("...(%d more tasks omitted for brevity)\n", len(tasks)-shownTasks)
	}

	// Agent status (cap to keep prompt bounded)
	var agents []model.Agent
	model.DB.Where("current_project_id = ?", projectID).Limit(20).Find(&agents)
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

	// Active policies (cap)
	var policies []model.Policy
	model.DB.Where("status = 'active'").Order("priority DESC").Limit(30).Find(&policies)
	policyList := ""
	for _, p := range policies {
		policyList += "- [" + p.Source + "] " + p.Name + ": match=" + p.MatchCondition + " actions=" + p.Actions + "\n"
	}

	// Knowledge artifacts from refinery — scored by relevance to what the
	// project is actively doing, not just top-N by confidence. Query text
	// summarises the current work surface (milestone + pending task
	// names) so semantic retrieval pulls artifacts related to the right
	// topics. See SelectArtifactsForInjection for the scoring formula.
	queryText := buildChiefQueryText(milestoneContent, tasks)
	injected := SelectArtifactsForInjection(context.Background(), ArtifactQuery{
		ProjectID: projectID,
		Audience:  AudienceCommander,
		QueryText: queryText,
	})
	artifactList := ""
	injectedIDs := make([]string, 0, len(injected))
	for _, ia := range injected {
		a := ia.Artifact
		successRate := 0.0
		if a.UsageCount > 0 {
			successRate = float64(a.SuccessCount) / float64(a.UsageCount)
		}
		artifactList += fmt.Sprintf("- [%s] %s (score=%.2f via %s, used=%d, success_rate=%.0f%%): %s\n",
			a.Kind, a.Name, ia.Score, ia.Reason, a.UsageCount, successRate*100, a.Summary)
		injectedIDs = append(injectedIDs, a.ID)
	}
	// Bump usage_count for each injected artifact (feedback loop for lifecycle)
	if len(injectedIDs) > 0 {
		model.DB.Model(&model.KnowledgeArtifact{}).Where("id IN ?", injectedIDs).
			Update("usage_count", gorm.Expr("usage_count + 1"))
		model.DB.Model(&model.KnowledgeArtifact{}).Where("id IN ?", injectedIDs).
			Update("last_used_at", time.Now())
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
		"## 平台全局状态\n\n### 项目\n- 方向: %s\n- 当前里程碑: %s\n- 版本: %s\n- AutoMode: %v\n\n### 任务概览\n- 待领取: %d 个\n- 进行中: %d 个\n- 已完成: %d 个\n%s\n### Agent 状态\n%s\n### 待处理事项\n%s\n### 最近审核结果\n%s\n### PR 状态\n%s\n### 当前策略\n%s\n### 知识库 (Refinery)\n%s",
		directionContent, milestoneContent, versionContent, autoMode,
		pendingCount, inProgressCount, completedCount,
		taskList, agentList, pendingActions, auditList, prList, policyList, artifactList,
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
		DirectionBlock:      directionContent,
		MilestoneBlock:      milestoneContent,
		TaskList:            taskList,
		Version:             versionContent,
		InputContent:        inputContent,
		TriggerReason:       "chief_decision_" + decisionType,
		GlobalState:         globalState,
		AutoMode:            autoMode,
		InjectedArtifactIDs: injectedIDs,
		// Chief may want to glance at source or platform meta files
		// when responding to a human. Rooting at the project dir
		// surfaces both without opening the whole filesystem.
		ProjectPath: GetProjectPath(projectID),
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
