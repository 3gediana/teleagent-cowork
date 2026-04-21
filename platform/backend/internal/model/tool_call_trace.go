package model

import "time"

// ToolCallTrace records each platform tool invocation for observability.
type ToolCallTrace struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	SessionID     string    `gorm:"size:64;index" json:"session_id"`
	ProjectID     string    `gorm:"size:64;index" json:"project_id"`
	ToolName      string    `gorm:"size:32;index" json:"tool_name"`
	Args          string    `gorm:"type:json" json:"args"`
	ResultSummary string    `gorm:"type:text" json:"result_summary"` // max ~500 chars
	Success       bool      `gorm:"index" json:"success"`
	CreatedAt     time.Time `gorm:"index" json:"created_at"`
}

func (ToolCallTrace) TableName() string { return "tool_call_trace" }
