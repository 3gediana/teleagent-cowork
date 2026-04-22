package model

import "time"

// Episode is a grouped view of an AgentSession: its tool-call sequence plus
// outcome metadata, cached in a form cheap for the Refinery to re-read
// across multiple passes without re-joining ToolCallTrace each time.
//
// The Refinery's EpisodeGrouper pass produces these; downstream passes
// (PatternExtractor, AntiPatternDetector, ToolRecipeMiner, …) consume them.
type Episode struct {
	ID        string `gorm:"primaryKey;size:64" json:"id"`
	ProjectID string `gorm:"size:64;not null;index:idx_ep_project" json:"project_id"`
	SessionID string `gorm:"size:64;not null;uniqueIndex:idx_ep_session" json:"session_id"`

	Role       string `gorm:"size:32;index:idx_ep_role" json:"role"`
	TaskID     string `gorm:"size:64;index:idx_ep_task" json:"task_id"`
	ChangeID   string `gorm:"size:64" json:"change_id"`
	PRID       string `gorm:"size:64" json:"pr_id"`

	// Outcome is a coarse success signal derived from session status + audit:
	// success / partial / failure / unknown
	Outcome    string `gorm:"size:16;index:idx_ep_outcome" json:"outcome"`
	// AuditLevel: L0 / L1 / L2 / none (best-effort inference)
	AuditLevel string `gorm:"size:8" json:"audit_level"`

	// ToolSequence is a space-separated list of tool names in chronological
	// order — e.g. "grep read grep edit change_submit". This format makes
	// n-gram extraction trivial in Go.
	ToolSequence  string `gorm:"type:text" json:"tool_sequence"`
	ToolCallCount int    `json:"tool_call_count"`

	// FilesTouched is a JSON array of unique file paths touched during
	// the session (best-effort parsed from tool args).
	FilesTouched string `gorm:"type:json;default:null" json:"files_touched"`

	DurationMs int `json:"duration_ms"`

	// Status: new / analyzed / skipped
	Status    string    `gorm:"size:16;default:'new';index:idx_ep_status" json:"status"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

func (Episode) TableName() string { return "episode" }
