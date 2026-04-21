package model

import "time"

// Experience stores structured feedback and observations from agent executions.
// Raw experiences are distilled by the Analyze Agent into skills and policies.
type Experience struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	ProjectID   string    `gorm:"size:64;not null;index:idx_exp_project" json:"project_id"`
	SourceType  string    `gorm:"size:32;not null;index:idx_exp_source" json:"source_type"` // agent_feedback / audit_observation / fix_strategy / eval_pattern / maintain_rationale
	SourceID    string    `gorm:"size:64" json:"source_id"`                                 // session ID or task ID
	AgentRole   string    `gorm:"size:32;not null;index:idx_exp_role" json:"agent_role"`
	TaskID      string    `gorm:"size:64;index:idx_exp_task" json:"task_id"`
	Outcome     string    `gorm:"size:20;index:idx_exp_outcome" json:"outcome"` // success / partial / failed

	// Core experience content
	Approach       string `gorm:"type:text" json:"approach"`
	Pitfalls       string `gorm:"type:text" json:"pitfalls"`
	KeyInsight     string `gorm:"type:text" json:"key_insight"`
	MissingContext string `gorm:"type:text" json:"missing_context"`
	DoDifferently  string `gorm:"type:text" json:"do_differently"`

	// Structured supplements
	PatternObserved string `gorm:"type:text" json:"pattern_observed"`
	FixStrategy     string `gorm:"type:text" json:"fix_strategy"`
	QualityPatterns string `gorm:"type:json" json:"quality_patterns"`
	FalsePositive   bool   `gorm:"default:false" json:"false_positive"`

	// Context
	Tags          string `gorm:"type:json" json:"tags"`
	FilesInvolved string `gorm:"type:json" json:"files_involved"`

	Status    string    `gorm:"size:20;default:'raw';index:idx_exp_status" json:"status"` // raw / distilled / skill / deprecated
	CreatedAt time.Time `gorm:"index:idx_exp_created" json:"created_at"`
}

func (Experience) TableName() string { return "experience" }
