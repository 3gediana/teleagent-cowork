package service

import (
	"encoding/json"
	"log"

	"github.com/a3c/platform/internal/model"
	"gorm.io/gorm"
)

// HandleSessionCompletion bumps success_count or failure_count on every
// KnowledgeArtifact that was injected into the finished session's prompt.
// This closes the refinery feedback loop — lifecycle rules
// (PromoteAndDeprecateArtifacts) then see real effectiveness data.
//
// Status mapping:
//   - "completed"   → success++
//   - "failed"      → failure++
//   - "rejected"    → failure++    (audit L2)
//   - "pending_fix" → failure++    (audit L1 asked for fix)
func HandleSessionCompletion(sessionID, projectID, role, status string) {
	// Recover so a DB hiccup here can never break agent execution.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ArtifactFeedback] recovered from panic: %v", r)
		}
	}()

	// Capture assistant output for dialogue roles. Must happen on
	// "completed" only — failed/rejected runs would pollute chat
	// history with error stubs the model didn't actually author.
	//
	// Additionally, only chat-flavoured trigger reasons go onto the
	// DialogueMessage table. Chief has two session kinds:
	//   - "chief_request"            → user-facing chat (append)
	//   - "chief_decision_<kind>"   → platform-initiated automation
	//                                  (DO NOT append; it muddies
	//                                  the transcript with internal
	//                                  pr_review / pr_merge replies
	//                                  the human never asked for)
	// Maintain has a similar split: dashboard_* is chat, everything
	// else (timer, milestone_complete, biz_review) is bookkeeping.
	if status == "completed" {
		if channel := DialogueChannelForRole(role); channel != "" {
			var outSess model.AgentSession
			if err := model.DB.Select("output", "trigger_reason").Where("id = ?", sessionID).First(&outSess).Error; err == nil {
				if isChatTrigger(role, outSess.TriggerReason) {
					AppendDialogueMessage(projectID, channel, sessionID, DialogueRoleAssistant, outSess.Output)
				}
			}
		}
	}

	var sess model.AgentSession
	if err := model.DB.Select("injected_artifacts").Where("id = ?", sessionID).First(&sess).Error; err != nil {
		return
	}
	if sess.InjectedArtifacts == "" || sess.InjectedArtifacts == "[]" {
		return
	}
	var ids []string
	if err := json.Unmarshal([]byte(sess.InjectedArtifacts), &ids); err != nil || len(ids) == 0 {
		return
	}

	field := ""
	switch status {
	case "completed":
		field = "success_count"
	case "failed", "rejected", "pending_fix":
		field = "failure_count"
	default:
		return
	}

	if err := model.DB.Model(&model.KnowledgeArtifact{}).Where("id IN ?", ids).
		Update(field, gorm.Expr(field+" + 1")).Error; err != nil {
		log.Printf("[ArtifactFeedback] bump %s failed for session %s: %v", field, sessionID, err)
		return
	}
	log.Printf("[ArtifactFeedback] session %s (%s/%s) bumped %s on %d artifacts",
		sessionID, role, status, field, len(ids))
}
