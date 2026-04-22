package service

// Dialogue history helpers — replace the opencode serve session that
// previously tracked multi-round conversations for Chief chat and the
// Maintain dashboard input.
//
// The native runner is stateless per session: every new user turn
// spawns a fresh agent.Session. To give the agent continuity across
// turns, we:
//
//   1. Append the user's message to model.DialogueMessage before
//      dispatching (AppendDialogueMessage).
//   2. Hydrate the session's prompt with prior turns so the model
//      sees the conversation so far (BuildDialogueHistoryForPrompt).
//   3. Append the agent's final output when the session reaches a
//      terminal state — wired through service.HandleSessionCompletion
//      which already runs on every role.
//
// Channels partition the history per concern. Today:
//   - "chief"    — Chief agent chat tab (handler/chief.go:Chat)
//   - "maintain" — Main dashboard input → Maintain agent
// New channels can be added without a migration.

import (
	"fmt"
	"log"
	"strings"

	"github.com/a3c/platform/internal/model"
)

// DialogueChannelChief is the channel identifier for Chief chat turns.
const DialogueChannelChief = "chief"

// DialogueChannelMaintain is the channel identifier for dashboard
// input → Maintain agent turns.
const DialogueChannelMaintain = "maintain"

// DialogueRoleUser / DialogueRoleAssistant are the two values stored
// in DialogueMessage.Role. Kept as plain consts (not a type) so
// callers can literal-string them when needed.
const (
	DialogueRoleUser      = "user"
	DialogueRoleAssistant = "assistant"
)

// AppendDialogueMessage writes one turn into the dialogue history.
// Empty content is ignored (we don't want rows polluted by failed
// generations that yielded no output). projectID and channel are
// required; sessionID is optional for user-origin rows that predate
// the session that will answer them.
func AppendDialogueMessage(projectID, channel, sessionID, role, content string) {
	content = strings.TrimSpace(content)
	if content == "" || projectID == "" || channel == "" || role == "" {
		return
	}
	msg := &model.DialogueMessage{
		ID:        model.GenerateID("dlg"),
		ProjectID: projectID,
		Channel:   channel,
		SessionID: sessionID,
		Role:      role,
		Content:   content,
	}
	if err := model.DB.Create(msg).Error; err != nil {
		log.Printf("[Dialogue] Failed to persist %s/%s message: %v", channel, role, err)
	}
}

// LoadRecentDialogue returns the most recent `limit` messages for a
// channel, ordered oldest-first so callers can render them as-is.
// Returns an empty slice (never nil) on miss or error — a fresh
// project legitimately has no history and we don't want call sites
// to special-case nil.
func LoadRecentDialogue(projectID, channel string, limit int) []model.DialogueMessage {
	if limit <= 0 {
		limit = 20
	}
	// Pull descending so LIMIT keeps the tail, then reverse.
	var rows []model.DialogueMessage
	err := model.DB.Where("project_id = ? AND channel = ?", projectID, channel).
		Order("created_at DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		log.Printf("[Dialogue] Failed to load %s history for project %s: %v", channel, projectID, err)
		return []model.DialogueMessage{}
	}
	// Reverse into chronological order.
	out := make([]model.DialogueMessage, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, rows[i])
	}
	return out
}

// ClearDialogue deletes every message on a channel for a project.
// Used by the dashboard's Clear Context button and by the
// auto-clear on direction/milestone confirm. Returns the row count
// deleted so callers can log something meaningful.
func ClearDialogue(projectID, channel string) int64 {
	res := model.DB.Where("project_id = ? AND channel = ?", projectID, channel).
		Delete(&model.DialogueMessage{})
	if res.Error != nil {
		log.Printf("[Dialogue] Failed to clear %s history for project %s: %v", channel, projectID, res.Error)
		return 0
	}
	return res.RowsAffected
}

// BuildDialogueHistoryForPrompt renders recent dialogue history as a
// markdown conversation log suitable to prepend to a new session's
// InputContent. The format is deliberately plain — small-model
// friendly — and includes the current turn's user message last so
// the prompt ends on "now respond".
//
// Returns an empty string when there's no history, so callers can
// concatenate unconditionally.
func BuildDialogueHistoryForPrompt(projectID, channel string) string {
	msgs := LoadRecentDialogue(projectID, channel, 20)
	if len(msgs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Conversation history\n\n")
	for _, m := range msgs {
		label := "Human"
		if m.Role == DialogueRoleAssistant {
			label = "You"
		}
		sb.WriteString(fmt.Sprintf("**%s:** %s\n\n", label, m.Content))
	}
	return sb.String()
}

// DialogueChannelForRole returns the dialogue channel conventionally
// associated with a role, or empty string when the role has no
// dialogue lane of its own (audit/fix/evaluate/merge etc. are
// one-shot work agents — no conversation).
func DialogueChannelForRole(role string) string {
	switch role {
	case "chief":
		return DialogueChannelChief
	case "maintain":
		return DialogueChannelMaintain
	default:
		return ""
	}
}
