package model

import "time"

// Policy stores decision rules for the Chief Agent.
// Human directives, auto-generated rules, and later RAG knowledge all live here.
type Policy struct {
	ID             string    `gorm:"primaryKey;size:64" json:"id"`
	Name           string    `gorm:"size:128;not null" json:"name"`
	MatchCondition string    `gorm:"type:json;not null" json:"match_condition"` // e.g. {"scope":"pr_review","file_count_gt":5}
	Actions        string    `gorm:"type:json;not null" json:"actions"`        // e.g. {"require_human":true,"warn":"大改动需人类确认"}
	Priority       int       `gorm:"default:0" json:"priority"`
	Status         string    `gorm:"size:20;default:'active'" json:"status"` // active/deprecated
	Source         string    `gorm:"size:20;default:'human'" json:"source"` // human/chief/analyze
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (Policy) TableName() string { return "policy" }
