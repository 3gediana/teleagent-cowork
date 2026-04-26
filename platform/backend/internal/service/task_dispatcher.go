package service

// Task dispatcher — matches pending tasks to idle pool agents and
// fires TASK_ASSIGN broadcasts. Without this the pool agents sit
// forever at "ready" because no production code path ever writes to
// the a3c:directed:<agent_id> queue that their broadcast consumer
// drains. See internal/agentpool/broadcast_consumer.go for the
// receiving half of this contract.
//
// Scope deliberately narrow:
//   - One task per agent per tick (no batching, no priority queues).
//   - Cooldown per task so the same task isn't re-broadcast every
//     tick while the agent is still waking up or deciding whether to
//     claim it.
//   - No dependency graph — the dispatcher is content-agnostic.
//     Task ordering within a priority bucket is FIFO on created_at.
//
// Failure modes we accept:
//   - Agent ignores the broadcast → next tick we'll re-broadcast
//     (after cooldown) or give the task to a different idle agent.
//   - Agent claims and crashes → task auto-releases via the
//     heartbeat timeout path and becomes pending again, dispatcher
//     picks it up next round.

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"github.com/gin-gonic/gin"
)

var (
	dispatcherRunning bool
	dispatcherLastAssigned = make(map[string]time.Time)
	dispatcherMu           sync.Mutex
)

// DispatcherInterval is the tick cadence. 15s is a compromise between
// "feels responsive to a human watching the dashboard" and "doesn't
// hammer the DB on idle clusters".
const DispatcherInterval = 15 * time.Second

// DispatcherCooldown is how long we wait before re-broadcasting the
// same task to another (or the same) agent. Needs to be long enough
// that an agent picking up the message has had a chance to call
// task.claim; 90s matches the typical wake → opencode prompt → first
// tool call latency we see on MiniMax-M2.7.
const DispatcherCooldown = 90 * time.Second

// StartTaskDispatcher kicks off the background matcher. Safe to call
// multiple times — a guard prevents double-start. Wire from
// cmd/server/main.go alongside the other Start* schedulers.
func StartTaskDispatcher() {
	dispatcherMu.Lock()
	if dispatcherRunning {
		dispatcherMu.Unlock()
		return
	}
	dispatcherRunning = true
	dispatcherMu.Unlock()

	go func() {
		ticker := time.NewTicker(DispatcherInterval)
		defer ticker.Stop()
		log.Printf("[Dispatcher] Task dispatcher started (interval=%s, cooldown=%s)", DispatcherInterval, DispatcherCooldown)
		for range ticker.C {
			runDispatcherOnce()
		}
	}()
}

// runDispatcherOnce is the single tick body. Pulled out for
// testability — callers can exercise the logic without spinning a
// timer.
func runDispatcherOnce() {
	// 1. All platform-hosted agents currently online, grouped by
	//    project. Non-pool agents (humans, external clients) are
	//    outside this dispatcher's responsibility — they pick their
	//    own tasks via the UI or their own MCP loop.
	var agents []model.Agent
	if err := model.DB.Where(
		"is_platform_hosted = ? AND status = ? AND current_project_id IS NOT NULL",
		true, "online",
	).Find(&agents).Error; err != nil {
		log.Printf("[Dispatcher] Failed to load pool agents: %v", err)
		return
	}
	if len(agents) == 0 {
		return
	}

	byProject := make(map[string][]model.Agent)
	for _, a := range agents {
		if a.CurrentProjectID != nil {
			byProject[*a.CurrentProjectID] = append(byProject[*a.CurrentProjectID], a)
		}
	}

	for projectID, projectAgents := range byProject {
		dispatchForProject(projectID, projectAgents)
	}
}

// dispatchForProject is the per-project matcher. Broken out for
// readability; callers don't use this directly.
func dispatchForProject(projectID string, projectAgents []model.Agent) {
	// 2. Filter agents to those with no currently-claimed task. An
	//    agent holding a claim is presumed still working on it — we
	//    don't queue a second assignment.
	var idleAgents []model.Agent
	for _, a := range projectAgents {
		var claimedCount int64
		model.DB.Model(&model.Task{}).Where(
			"assignee_id = ? AND status = ?", a.ID, "claimed",
		).Count(&claimedCount)
		if claimedCount == 0 {
			idleAgents = append(idleAgents, a)
		}
	}
	if len(idleAgents) == 0 {
		return
	}

	// 3. Pending, unassigned tasks, ordered by priority then age.
	//    MySQL FIELD() gives us explicit priority order; SQLite
	//    doesn't support it but we target MySQL in production.
	var tasks []model.Task
	if err := model.DB.Where(
		"project_id = ? AND status = ? AND assignee_id IS NULL",
		projectID, "pending",
	).Order("FIELD(priority,'high','medium','low'), created_at ASC").Find(&tasks).Error; err != nil {
		log.Printf("[Dispatcher] project=%s: failed to load pending tasks: %v", projectID, err)
		return
	}
	if len(tasks) == 0 {
		return
	}

	// 4. Cooldown filter — skip tasks recently broadcast.
	now := time.Now()
	dispatcherMu.Lock()
	var freshTasks []model.Task
	for _, t := range tasks {
		last, hasLast := dispatcherLastAssigned[t.ID]
		if !hasLast || now.Sub(last) > DispatcherCooldown {
			freshTasks = append(freshTasks, t)
		}
	}
	dispatcherMu.Unlock()
	if len(freshTasks) == 0 {
		return
	}

	// 5. 1-to-1 match (capped at min(agents, tasks)). Agent ordering
	//    is FIFO by DB row order — good enough for now; a future
	//    pass could incorporate load / skill / affinity.
	n := len(idleAgents)
	if len(freshTasks) < n {
		n = len(freshTasks)
	}

	// Build a shared project context header once per project-tick.
	// Every TASK_ASSIGN broadcast for this project gets the same
	// header prefix so the agent's LLM sees concrete anchors ("this
	// project is <name>, direction <text>, milestone <...>") before
	// the abstract task description. Without this, task names like
	// "Implement task creation API" give the LLM no cue which
	// project they belong to and it guesses (see misc/bench/tasq-
	// experiment/ for the misdiagnosis mode this triggers).
	ctxHeader := buildProjectContextHeader(projectID)

	for i := 0; i < n; i++ {
		agent := idleAgents[i]
		task := freshTasks[i]

		// The `description` field is what the agent's LLM actually
		// reads in the injected user turn — everything else in the
		// payload is structured metadata. We prepend the project
		// context header so the task description is framed with
		// "you are working on project X, direction Y, current
		// milestone Z" before the task-specific text.
		enrichedDescription := ctxHeader + "\n---\n## Task\n\n" +
			"**" + task.Name + "** (id=" + task.ID + ", priority=" + task.Priority + ")\n\n" +
			task.Description + "\n\n---\n" +
			"## What to do right now\n\n" +
			"1. Call `a3c_task action=claim task_id=" + task.ID + "`.\n" +
			"2. Call `a3c_file_sync` to pull project files into your staging dir.\n" +
			"3. Read `.a3c_staging/" + task.ProjectID + "/full/OVERVIEW.md`.\n" +
			"4. Plan the edit, `a3c_filelock` the target files, edit them in staging.\n" +
			"5. `a3c_change_submit` and act on `next_action`.\n" +
			"6. `a3c_feedback` with outcome and one key_insight.\n\n" +
			"See AGENTS.md in your working directory for the hard rules.\n"

		BroadcastDirected(agent.ID, "TASK_ASSIGN", gin.H{
			"task_id":     task.ID,
			"task_name":   task.Name,
			"description": enrichedDescription,
			"priority":    task.Priority,
			"project_id":  task.ProjectID,
		})

		dispatcherMu.Lock()
		dispatcherLastAssigned[task.ID] = now
		dispatcherMu.Unlock()

		log.Printf("[Dispatcher] Assigned task %s (%q) to agent %s (%s) in project %s",
			task.ID, task.Name, agent.ID, agent.Name, projectID)
	}

	// 6. Garbage-collect the cooldown map so it doesn't grow
	//    unbounded across completed tasks. Anything older than 10x
	//    cooldown is definitely not about to be re-broadcast.
	dispatcherMu.Lock()
	cutoff := now.Add(-10 * DispatcherCooldown)
	for tid, ts := range dispatcherLastAssigned {
		if ts.Before(cutoff) {
			delete(dispatcherLastAssigned, tid)
		}
	}
	dispatcherMu.Unlock()
}

// buildProjectContextHeader formats project name + direction + current
// milestone into a markdown header block. Used by the TASK_ASSIGN
// broadcast so the agent's LLM sees project anchors before the task
// description.
//
// Falls back gracefully when individual blocks are missing (fresh
// project with no direction, no active milestone, etc.) — we emit
// a stub "(not set)" note rather than skipping, so the agent
// knows the platform intended to surface that context.
//
// Returns a self-contained markdown string, no leading/trailing
// whitespace.
func buildProjectContextHeader(projectID string) string {
	var sb strings.Builder

	// Project name — cheap, always worth showing.
	var project model.Project
	projectName := "(unknown)"
	if err := model.DB.Where("id = ?", projectID).First(&project).Error; err == nil {
		projectName = project.Name
	}

	sb.WriteString("## Project context\n\n")
	sb.WriteString(fmt.Sprintf("**Project:** %s (`%s`)\n\n", projectName, projectID))

	// Direction — the "what is this project for" block. Usually
	// set early by Chief/Maintain; empty on brand-new projects.
	direction, _ := repo.GetContentBlock(projectID, "direction")
	if direction != nil && strings.TrimSpace(direction.Content) != "" {
		sb.WriteString("**Direction:**\n")
		sb.WriteString(trimForContext(direction.Content, 800))
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("**Direction:** _(not set — ask Chief if unclear)_\n\n")
	}

	// Current milestone — the "what phase are we in" block.
	milestone, _ := repo.GetCurrentMilestone(projectID)
	if milestone != nil {
		sb.WriteString(fmt.Sprintf("**Current milestone:** %s\n", milestone.Name))
		if strings.TrimSpace(milestone.Description) != "" {
			sb.WriteString(trimForContext(milestone.Description, 400))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("**Current milestone:** _(no milestone in progress)_\n\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

// trimForContext truncates a block to roughly maxChars while
// preserving complete lines. Dispatcher broadcasts go straight into
// the agent's LLM prompt, so unbounded direction text would eat the
// model's context window on every TASK_ASSIGN. 800-char direction +
// 400-char milestone is enough to convey intent without being the
// whole document.
func trimForContext(s string, maxChars int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxChars {
		return s
	}
	trimmed := s[:maxChars]
	// Cut at the last newline so we don't end mid-sentence. If no
	// newline within range, fall back to the hard cut — the agent
	// can always call status_sync for the full text.
	if idx := strings.LastIndex(trimmed, "\n"); idx > maxChars/2 {
		trimmed = trimmed[:idx]
	}
	return trimmed + "\n_…(truncated; call a3c_status_sync for the full direction)_"
}
