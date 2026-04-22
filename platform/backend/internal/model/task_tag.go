package model

import "time"

// TaskTag stores categorization tags for tasks. Each tag is a revisable
// assertion — not ground truth — because its authors (rules, humans,
// Analyze Agent) all have different confidence profiles and can be wrong.
//
// Lifecycle states
// ----------------
//   proposed   — Keyword rules or LLM-derived guesses, low confidence.
//                Injected into injection scoring at a reduced weight.
//   confirmed  — Validated (by human / Analyze on real execution data).
//                Full weight in injection scoring.
//   rejected   — Removed by reviewer; kept for audit, excluded from
//                selection. Stops the same rule from re-proposing the
//                same tag next time.
//   superseded — Replaced by a better (usually more specific) tag. The
//                SupersededBy column points to the newer tag.
//
// Tag rows are never physically deleted — removing means flipping state
// to rejected / superseded. This preserves the signal "rule R7 kept
// proposing 'bugfix' which humans rejected 8 times" so we can evaluate
// and retire the rule itself.
type TaskTag struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	TaskID    string    `gorm:"size:64;not null;index:idx_task_tag_task" json:"task_id"`
	Tag       string    `gorm:"size:64;not null;index:idx_task_tag_tag" json:"tag"`

	// Dimension lets us group tags into axes (category / layer /
	// component / free). Free-form for now; a future tag-dictionary
	// feature will constrain it per project.
	Dimension string `gorm:"size:32;default:'free'" json:"dimension"`

	// Source identifies who produced the tag. Default 'human' matches
	// the pre-lifecycle behaviour where humans and Analyze were the
	// only writers. Rule engine adds 'auto_kw'.
	Source string `gorm:"size:20;default:'human';index:idx_task_tag_source" json:"source"` // human/chief/maintain/auditor/analyze/auto_kw

	// Status is the lifecycle state described in the type doc above.
	Status string `gorm:"size:16;default:'confirmed';index:idx_task_tag_status" json:"status"`

	// Confidence (0..1) expressing the producer's certainty at write
	// time. Rule engine emits low (0.3-0.5); humans emit 1.0.
	Confidence float64 `gorm:"default:1" json:"confidence"`

	// Evidence is a JSON blob explaining WHY the producer believed this
	// tag applied — for rules, the matched keywords; for humans, an
	// optional note. Stored as text to stay DB-portable.
	Evidence string `gorm:"type:text" json:"evidence"`

	// Review audit trail. ReviewedAt is non-nil once a reviewer has
	// explicitly touched the tag (confirm / reject / supersede).
	ReviewedAt   *time.Time `json:"reviewed_at"`
	ReviewedBy   string     `gorm:"size:64" json:"reviewed_by"`

	// SupersededBy is the ID of the tag that replaced this one
	// (non-empty only when Status=superseded).
	SupersededBy string `gorm:"size:64" json:"superseded_by"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (TaskTag) TableName() string { return "task_tag" }
