package model

import "time"

// SkillCandidate stores a distilled skill extracted from experience analysis.
type SkillCandidate struct {
	ID             string    `gorm:"primaryKey;size:64" json:"id"`
	Name           string    `gorm:"size:128;not null" json:"name"`
	Type           string    `gorm:"size:32;not null;index:idx_skill_type" json:"type"` // process / prompt / routing / guard
	ApplicableTags string    `gorm:"type:json" json:"applicable_tags"`
	Precondition   string    `gorm:"type:text" json:"precondition"`
	Action         string    `gorm:"type:text;not null" json:"action"`
	Prohibition    string    `gorm:"type:text" json:"prohibition"`
	SourceCaseIDs  string    `gorm:"type:json" json:"source_case_ids"`
	Evidence       string    `gorm:"type:text" json:"evidence"`
	Status         string    `gorm:"size:20;default:'candidate';index:idx_skill_status" json:"status"` // candidate / approved / active / deprecated / rejected
	Version        int       `gorm:"default:1" json:"version"`
	ApprovedBy     string    `gorm:"size:64" json:"approved_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (SkillCandidate) TableName() string { return "skill_candidate" }
