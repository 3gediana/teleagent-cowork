package agentpool

// Session-resume injector — runs whenever we hand a pool agent a
// brand-new opencode session (rotation / dormancy wake). Without
// this the agent wakes up on an empty transcript and forgets that
// it had a task claimed, since the `TASK_ASSIGN` broadcast and any
// intermediate tool calls lived on the old (archived) session.
// The dispatcher won't re-broadcast because the task still sits
// `claimed` in the DB — from its point of view the agent is
// mid-work — so the whole pipeline deadlocks indefinitely.
//
// We observed this during the tasq benchmark run: three pool agents
// each had one `claimed` task that sat untouched for 6+ hours after
// their sessions got rotated out from under them. This file is the
// surgical fix.
//
// Design decisions worth noting:
//   - We use the SAME injector path (`broadcastInjector.InjectMessage`)
//     as normal broadcast delivery so the message lands in opencode's
//     transcript as a user-visible turn, not a sneaky system tweak.
//     That means the LLM sees it in-line with whatever else happens
//     on the new session.
//   - We query model.DB directly (not through the store abstraction)
//     because the Store interface doesn't expose task queries and
//     adding one would bloat it for one callsite. The agentpool
//     package already imports model for the Agent struct.
//   - Failures are logged but never returned: the caller already
//     bound the new session id; a missed resume just means the
//     agent has to manually call `a3c_status_sync` to recover,
//     which is strictly better than the 6h deadlock.

import (
	"context"
	"fmt"
	"log"

	"github.com/a3c/platform/internal/model"
)

// maybeInjectResumePrompt checks whether the agent identified by
// agentID has a task in status='claimed' and, if so, pushes a short
// resume message into the given opencode session. Called right
// after rotateSession / wake bind the new session id on the
// Instance. Safe to call when the agent has no claimed task — it's
// a fast DB read and a no-op.
//
// providerID / modelID are required by the injector (opencode
// refuses to route messages without them; see NewPoolBroadcastInjector
// for the rationale). Pass them from the Instance you just mutated.
//
// reason is stitched into the prompt verbatim ("context_exceeded",
// "dormancy_idle", etc.) so the agent has context for why its
// transcript just got smaller.
func (m *Manager) maybeInjectResumePrompt(ctx context.Context, serveURL, sessionID, agentID, providerID, modelID, reason string) {
	if m.broadcastInjector == nil {
		return
	}
	if sessionID == "" || agentID == "" {
		return
	}
	// Tests run the pool manager without initialising model.DB —
	// the resume path is not what they're exercising and should
	// silently no-op rather than panic inside gorm.getInstance.
	// Production always has model.DB wired by main.go.
	if model.DB == nil {
		return
	}

	var task model.Task
	if err := model.DB.
		Where("assignee_id = ? AND status = ?", agentID, "claimed").
		Order("created_at DESC").
		First(&task).Error; err != nil {
		// No claimed task, or DB hiccup. Either way nothing to
		// resume — a wake-up on an empty session is then simply
		// the agent waiting for the next TASK_ASSIGN broadcast,
		// which is already the normal path.
		return
	}

	// Providing provider/model defensively: if the caller forgot
	// to thread them through, the InjectMessage call below would
	// error out ("empty provider/model (would produce parts=0)").
	// Log that clearly so the operator knows the resume didn't
	// land and the agent will likely stall.
	if providerID == "" || modelID == "" {
		log.Printf("[Pool] resume-prompt skipped agent=%s session=%s: empty provider/model",
			agentID, sessionID)
		return
	}

	text := fmt.Sprintf(
		"[SESSION RESUMED — reason=%s]\n\n"+
			"Your previous session was archived and this is a fresh one. "+
			"Per platform DB, you still hold a claimed task that wasn't "+
			"submitted yet:\n\n"+
			"  task_id: %s\n"+
			"  name:    %s\n"+
			"  priority: %s\n\n"+
			"Description:\n%s\n\n"+
			"Resume the core loop from where you left off:\n"+
			"  1. a3c_status_sync — confirm my_task + project direction/milestone\n"+
			"  2. a3c_file_sync — refresh staging files (a previous agent "+
			"may have updated main while you were out)\n"+
			"  3. a3c_filelock action=acquire on the files you'll write\n"+
			"  4. Write your implementation under the staging_dir returned by file_sync\n"+
			"  5. a3c_change_submit — do NOT resubmit a prior attempt; "+
			"if a3c_status_sync shows this task already completed, skip to a3c_feedback\n\n"+
			"If you genuinely can't make progress on this task, call "+
			"a3c_task action=release with a short reason so the dispatcher "+
			"can hand it to someone else.",
		reason, task.ID, task.Name, task.Priority, task.Description,
	)

	if err := m.broadcastInjector.InjectMessage(ctx, serveURL, sessionID, text, providerID, modelID); err != nil {
		log.Printf("[Pool] resume-prompt inject failed agent=%s session=%s task=%s: %v",
			agentID, sessionID, task.ID, err)
		return
	}
	log.Printf("[Pool] resume-prompt injected agent=%s session=%s task=%s reason=%s",
		agentID, sessionID, task.ID, reason)
}
