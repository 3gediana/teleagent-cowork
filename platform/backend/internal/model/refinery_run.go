package model

import "time"

// RefineryRun records one execution of the Refinery pipeline so we can
// attribute artifacts back to a specific run, measure pass throughput,
// and later correlate "policies applied after run X" with outcomes.
type RefineryRun struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	ProjectID string    `gorm:"size:64;index" json:"project_id"`
	Trigger   string    `gorm:"size:32" json:"trigger"` // manual / timer / event
	StartedAt time.Time  `gorm:"index" json:"started_at"`
	// EndedAt is a pointer so the initial "running" row can leave it
	// NULL. MySQL 8's default sql_mode rejects the zero literal
	// '0000-00-00 00:00:00' that a non-pointer time.Time serialises to.
	EndedAt    *time.Time `json:"ended_at"`
	DurationMs int        `json:"duration_ms"`

	// PassStats is a JSON map: {pass_name: {"episodes_seen":N,"artifacts":M,"skipped":K,"error":"..."}}
	PassStats string `gorm:"type:json" json:"pass_stats"`
	Status    string `gorm:"size:16;default:'running'" json:"status"` // running/ok/partial/failed
	Error     string `gorm:"type:text" json:"error"`
}

func (RefineryRun) TableName() string { return "refinery_run" }
