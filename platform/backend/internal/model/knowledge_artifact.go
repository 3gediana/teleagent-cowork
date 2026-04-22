package model

import "time"

// KnowledgeArtifact is the unified output type of Refinery passes.
//
// Different `Kind` values have different `Payload` JSON schemas:
//   pattern        {"tool_sequence":["grep","read","edit"], "support":5, "confidence":0.83, "n":3}
//   anti_pattern   {"tool_sequence":["edit","edit"], "support":3, "lift":2.1, "fail_rate":0.9}
//   tool_recipe    {"steps":[{"tool":"grep","hint":"..."}], ...}                (future)
//   model_route    {"task_tag":"bugfix", "suggested_model":"gpt-4o-mini", ...}  (future)
//   failure_class  {"label":"missing_ownership_check", "example_ids":[...]}     (future)
//   temporal_rule  {"trigger_event":"PR_MERGED", "expected_within_ms":...}      (future)
//
// Using one table with a Kind discriminator keeps schema migrations cheap
// while the set of passes is still evolving. Once a Kind stabilises we can
// pull it into a dedicated table if query patterns demand it.
type KnowledgeArtifact struct {
	ID        string `gorm:"primaryKey;size:64" json:"id"`
	ProjectID string `gorm:"size:64;index:idx_ka_project" json:"project_id"` // empty = cross-project

	Kind    string `gorm:"size:32;not null;index:idx_ka_kind" json:"kind"`
	Name    string `gorm:"size:256" json:"name"`
	Summary string `gorm:"type:text" json:"summary"`
	Payload string `gorm:"type:json" json:"payload"`

	// Provenance
	ProducedBy   string `gorm:"size:64;index:idx_ka_producer" json:"produced_by"` // "pattern_extractor/v1"
	SourceEvents string `gorm:"type:json;default:null" json:"source_events"`     // JSON array of ep/exp/tc IDs

	// Producer-declared confidence at extraction time (0..1).
	Confidence float64 `gorm:"default:0" json:"confidence"`

	// Effectiveness tracking (filled by downstream consumers via bumps).
	HitCount     int `gorm:"default:0" json:"hit_count"`      // times matched a situation
	UsageCount   int `gorm:"default:0" json:"usage_count"`    // times injected into a prompt / applied
	SuccessCount int `gorm:"default:0" json:"success_count"`  // positive outcome after application
	FailureCount int `gorm:"default:0" json:"failure_count"`  // negative outcome after application

	Status  string `gorm:"size:16;default:'candidate';index:idx_ka_status" json:"status"` // candidate/active/deprecated/rejected
	Version int    `gorm:"default:1" json:"version"`

	// Semantic embedding of (Name + Summary) produced by the Python
	// bge-base-zh-v1.5 sidecar. Stored as raw little-endian float32 bytes
	// to stay DB-portable (MySQL and SQLite both accept BLOB/VARBINARY).
	// Nil when the sidecar wasn't reachable at creation time; the injection
	// selector falls back to tag/confidence scoring in that case.
	Embedding    []byte     `gorm:"type:longblob" json:"-"`
	EmbeddingDim int        `gorm:"default:0" json:"embedding_dim"`
	EmbeddedAt   *time.Time `json:"embedded_at"`

	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `gorm:"index" json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func (KnowledgeArtifact) TableName() string { return "knowledge_artifact" }
