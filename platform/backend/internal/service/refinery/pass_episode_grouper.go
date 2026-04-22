package refinery

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/a3c/platform/internal/model"
)

// EpisodeGrouper converts finished AgentSessions into Episode rows so that
// downstream passes can do cheap n-gram analysis without re-joining
// ToolCallTrace each time. This pass is purely deterministic.
type EpisodeGrouper struct{}

func (EpisodeGrouper) Name() string      { return "episode_grouper/v1" }
func (EpisodeGrouper) Produces() []string { return []string{"episode"} }
func (EpisodeGrouper) Requires() []string { return nil }

func (EpisodeGrouper) Run(ctx *Context) (Stats, error) {
	// Sessions in a terminal state with no existing Episode yet.
	var sessions []model.AgentSession
	q := model.DB.Where("status IN ?", []string{"completed", "failed", "rejected", "pending_fix"})
	if ctx.ProjectID != "" {
		q = q.Where("project_id = ?", ctx.ProjectID)
	}
	if ctx.LookbackHours > 0 {
		q = q.Where("created_at >= ?", ctx.Now.Add(-time.Duration(ctx.LookbackHours)*time.Hour))
	}
	if err := q.Find(&sessions).Error; err != nil {
		return nil, err
	}

	var created, skipped, updated int
	for _, s := range sessions {
		// Look up the linked Change once; we use it for outcome inference,
		// audit_level, task_id, and incremental updates.
		var ch *model.Change
		if s.ChangeID != "" {
			var chRow model.Change
			if model.DB.Where("id = ?", s.ChangeID).First(&chRow).Error == nil {
				ch = &chRow
			}
		}

		outcome := inferOutcomeFromSessionAndChange(s, ch)
		auditLevel := inferAuditLevelFromSessionAndChange(s, ch)

		// D: Incremental update — if Episode exists but audit_level/outcome changed
		// (e.g. Change was reviewed after initial Episode creation), update it.
		var existing model.Episode
		err := model.DB.Where("session_id = ?", s.ID).First(&existing).Error
		if err == nil {
			if existing.Outcome != outcome || existing.AuditLevel != auditLevel {
				model.DB.Model(&model.Episode{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
					"outcome":     outcome,
					"audit_level": auditLevel,
					"status":      "new", // re-mark so downstream passes re-analyse
				})
				updated++
			} else {
				skipped++
			}
			continue
		}

		var traces []model.ToolCallTrace
		model.DB.Where("session_id = ?", s.ID).Order("created_at ASC").Find(&traces)

		toolNames := make([]string, 0, len(traces))
		filesTouched := map[string]struct{}{}
		for _, t := range traces {
			toolNames = append(toolNames, t.ToolName)
			for _, f := range extractFiles(t.Args) {
				filesTouched[f] = struct{}{}
			}
		}
		filesList := make([]string, 0, len(filesTouched))
		for f := range filesTouched {
			filesList = append(filesList, f)
		}
		filesJSON, _ := json.Marshal(filesList)

		ep := model.Episode{
			ID:            model.GenerateID("ep"),
			ProjectID:     s.ProjectID,
			SessionID:     s.ID,
			Role:          s.Role,
			ChangeID:      s.ChangeID,
			PRID:          s.PRID,
			Outcome:       outcome,
			AuditLevel:    auditLevel,
			ToolSequence:  strings.Join(toolNames, " "),
			ToolCallCount: len(toolNames),
			FilesTouched:  string(filesJSON),
			DurationMs:    s.DurationMs,
			Status:        "new",
			CreatedAt:     s.CreatedAt,
		}
		if ch != nil && ch.TaskID != nil {
			ep.TaskID = *ch.TaskID
		}
		if err := model.DB.Create(&ep).Error; err != nil {
			// Unique index on session_id — duplicate inserts are benign.
			skipped++
			continue
		}
		created++
	}

	return Stats{
		"sessions_seen":    len(sessions),
		"episodes_created": created,
		"episodes_updated": updated,
		"episodes_skipped": skipped,
	}, nil
}

// inferOutcomeFromSessionAndChange is the v2 outcome inference that
// cross-references the linked Change's audit_level. This lets us
// distinguish "session completed but auditor rejected it" from
// "session completed and auditor signed off".
func inferOutcomeFromSessionAndChange(s model.AgentSession, ch *model.Change) string {
	// Session-level terminal failures always win.
	switch s.Status {
	case "failed":
		return "failure"
	case "rejected":
		return "failure"
	case "pending_fix":
		return "partial"
	}

	// For completed sessions, look at audit verdict if present.
	if s.Status == "completed" && ch != nil && ch.AuditLevel != nil {
		switch *ch.AuditLevel {
		case "L0":
			return "success"
		case "L1":
			return "partial"
		case "L2":
			return "failure"
		}
	}
	if s.Status == "completed" {
		return "success"
	}
	return "unknown"
}

// inferAuditLevelFromSessionAndChange prefers the Change row's recorded
// audit_level over session-status heuristics.
func inferAuditLevelFromSessionAndChange(s model.AgentSession, ch *model.Change) string {
	if ch != nil && ch.AuditLevel != nil && *ch.AuditLevel != "" {
		return *ch.AuditLevel
	}
	switch s.Status {
	case "pending_fix":
		return "L1"
	case "rejected":
		return "L2"
	}
	if s.Role == "audit" && s.Status == "completed" {
		return "L0"
	}
	return "none"
}

// extractFiles best-effort extracts file paths from a ToolCallTrace.Args
// JSON blob. We look for common keys: "path", "file", "files", "writes".
// Returns an empty slice if nothing looks like a path.
func extractFiles(argsJSON string) []string {
	if argsJSON == "" || argsJSON == "null" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return nil
	}
	out := map[string]struct{}{}
	for _, k := range []string{"path", "file"} {
		if s, ok := m[k].(string); ok && s != "" {
			out[s] = struct{}{}
		}
	}
	for _, k := range []string{"files", "paths"} {
		if arr, ok := m[k].([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok && s != "" {
					out[s] = struct{}{}
				}
			}
		}
	}
	if arr, ok := m["writes"].([]any); ok {
		for _, v := range arr {
			if mm, ok := v.(map[string]any); ok {
				if s, ok := mm["path"].(string); ok && s != "" {
					out[s] = struct{}{}
				}
			}
		}
	}
	result := make([]string, 0, len(out))
	for k := range out {
		result = append(result, k)
	}
	return result
}
