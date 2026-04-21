package model

import "time"

// AgentSession persists agent session data to DB for traceability across restarts.
type AgentSession struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	Role              string     `gorm:"size:32;index" json:"role"`
	ProjectID         string     `gorm:"size:64;index" json:"project_id"`
	ChangeID          string     `gorm:"size:64" json:"change_id"`
	PRID              string     `gorm:"size:64" json:"pr_id"`
	TriggerReason     string     `gorm:"size:64" json:"trigger_reason"`
	Status            string     `gorm:"size:20;index" json:"status"` // pending/running/completed/failed
	ModelProvider     string     `gorm:"size:64" json:"model_provider"`
	ModelID           string     `gorm:"size:128" json:"model_id"`
	OpenCodeSessionID string     `gorm:"size:128" json:"opencode_session_id"`
	Output            string     `gorm:"type:text" json:"output"`
	PromptHash        string     `gorm:"size:64" json:"prompt_hash"`
	DurationMs        int        `json:"duration_ms"`
	RetryCount        int        `gorm:"default:0" json:"retry_count"`
	LastError         string     `gorm:"type:text" json:"last_error"`
	CreatedAt         time.Time  `gorm:"index" json:"created_at"`
	CompletedAt       *time.Time `json:"completed_at"`
}

func (AgentSession) TableName() string { return "agent_session" }
