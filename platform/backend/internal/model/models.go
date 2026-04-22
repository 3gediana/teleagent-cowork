package model

import (
	"crypto/rand"
	"fmt"
	"time"
)

func GenerateID(prefix string) string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%s_%x", prefix, b)
}

func GenerateKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

type Project struct {
	ID          string     `gorm:"primaryKey;size:64" json:"id"`
	Name        string     `gorm:"size:256;not null" json:"name"`
	Description string     `gorm:"type:text" json:"description"`
	GithubRepo  string     `gorm:"size:512" json:"github_repo"`
	Status      string     `gorm:"size:20;default:'initializing'" json:"status"` // initializing/ready/idle
	AutoMode    bool       `gorm:"default:true" json:"auto_mode"`                // true=auto (blocks for audit), false=manual (requires human confirm before audit)
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (Project) TableName() string { return "project" }

type Agent struct {
	ID               string     `gorm:"primaryKey;size:64" json:"id"`
	Name             string     `gorm:"size:128;not null;uniqueIndex" json:"name"`
	AccessKey        string     `gorm:"size:256;not null" json:"-"`
	SessionID        string     `gorm:"size:128" json:"session_id"`
	Status           string     `gorm:"size:20;default:'offline'" json:"status"` // online/offline
	IsHuman          bool       `gorm:"default:false" json:"is_human"`           // true = dashboard user; allowed to direct Maintain Agent
	CurrentProjectID *string    `gorm:"size:64;index" json:"current_project_id"`
	CurrentBranchID  *string    `gorm:"size:64;index" json:"current_branch_id"` // nil=on main
	LastHeartbeat    *time.Time `json:"last_heartbeat"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`

	// IsPlatformHosted marks agents spawned by the platform itself —
	// opencode subprocesses the platform started, auto-injected with
	// skills and MCP wiring, then treats like any other client agent
	// claiming tasks. The pool manager sets this; humans registering
	// via /agent/register always get false. Set at spawn and never
	// mutated afterwards, so the UI can safely show the "platform
	// agent" chip based purely on this field.
	IsPlatformHosted bool   `gorm:"default:false;index" json:"is_platform_hosted"`
	// PoolInstanceID is the opaque id used by the pool manager to
	// track subprocess lifecycle (PID, ports, working dir, etc.).
	// Empty for human-registered / external-client agents.
	PoolInstanceID   string `gorm:"size:64;index" json:"pool_instance_id"`
}

func (Agent) TableName() string { return "agent" }

type ContentBlock struct {
	ID         string    `gorm:"primaryKey;size:64" json:"id"`
	ProjectID  string    `gorm:"size:64;not null;uniqueIndex:idx_block_project_type" json:"project_id"`
	BlockType  string    `gorm:"size:20;not null;uniqueIndex:idx_block_project_type" json:"block_type"` // direction/milestone/version
	Content    string    `gorm:"type:text;not null" json:"content"`
	Version    int       `gorm:"default:1" json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (ContentBlock) TableName() string { return "content_block" }

type Milestone struct {
	ID          string     `gorm:"primaryKey;size:64" json:"id"`
	ProjectID   string     `gorm:"size:64;not null;index:idx_milestone_project" json:"project_id"`
	Name        string     `gorm:"size:256;not null" json:"name"`
	Description string     `gorm:"type:text" json:"description"`
	Status      string     `gorm:"size:20;default:'in_progress'" json:"status"` // in_progress/completed
	CreatedBy   string     `gorm:"size:64;not null" json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

func (Milestone) TableName() string { return "milestone" }

type MilestoneArchive struct {
	ID               string    `gorm:"primaryKey;size:64" json:"id"`
	ProjectID        string    `gorm:"size:64;not null;index:idx_archive_project" json:"project_id"`
	MilestoneID      string    `gorm:"size:64;not null" json:"milestone_id"`
	Name             string    `gorm:"size:256;not null" json:"name"`
	Description      string    `gorm:"type:text" json:"description"`
	DirectionSnapshot string  `gorm:"type:text;not null" json:"direction_snapshot"`
	Tasks            string    `gorm:"type:json;not null" json:"tasks"` // JSON
	VersionStart     string    `gorm:"size:8" json:"version_start"`
	VersionEnd       string    `gorm:"size:8" json:"version_end"`
	CreatedAt        time.Time `json:"created_at"`
}

func (MilestoneArchive) TableName() string { return "milestone_archive" }

type Task struct {
	ID          string     `gorm:"primaryKey;size:64" json:"id"`
	ProjectID   string     `gorm:"size:64;not null;index:idx_task_project" json:"project_id"`
	MilestoneID *string    `gorm:"size:64" json:"milestone_id"`
	Name        string     `gorm:"size:256;not null" json:"name"`
	Description string     `gorm:"type:text" json:"description"`
	Priority    string     `gorm:"size:10;default:'medium'" json:"priority"` // high/medium/low
	Status      string     `gorm:"size:20;default:'pending'" json:"status"` // pending/claimed/completed/deleted
	AssigneeID  *string    `gorm:"size:64;index:idx_task_assignee" json:"assignee_id"`
	CreatedBy   string     `gorm:"size:64;not null" json:"created_by"`

	// Semantic embedding of (Name + "\n" + Description), produced by the
	// bge-base-zh-v1.5 sidecar at task-create time. Used by the injection
	// selector to find the most semantically relevant past artifacts for
	// this task without relying solely on tags. Nil when the embedder was
	// unreachable; a background reconciler will backfill it later.
	DescriptionEmbedding    []byte     `gorm:"type:longblob" json:"-"`
	DescriptionEmbeddingDim int        `gorm:"default:0" json:"description_embedding_dim"`
	DescriptionEmbeddedAt   *time.Time `json:"description_embedded_at"`

	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at"`
	DeletedAt   *time.Time `json:"deleted_at"`
}

func (Task) TableName() string { return "task" }

type FileLock struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	ProjectID   string    `gorm:"size:64;not null;index:idx_lock_project" json:"project_id"`
	BranchID    *string   `gorm:"size:64;index" json:"branch_id"` // nil=main lock
	TaskID      string    `gorm:"size:64;not null;index:idx_lock_task" json:"task_id"`
	AgentID     string    `gorm:"size:64;not null;index:idx_lock_agent" json:"agent_id"`
	Files       string    `gorm:"type:json;not null" json:"files"` // JSON array
	Reason      string    `gorm:"type:text;not null" json:"reason"`
	BaseVersion string    `gorm:"size:8" json:"base_version"`
	AcquiredAt  time.Time `json:"acquired_at"`
	ReleasedAt  *time.Time `json:"released_at"`
	ExpiresAt   time.Time `gorm:"not null;index:idx_lock_expires" json:"expires_at"`
}

func (FileLock) TableName() string { return "file_lock" }

type ChangeFileEntry struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}

type Change struct {
	ID            string     `gorm:"primaryKey;size:64" json:"id"`
	ProjectID     string     `gorm:"size:64;not null;index:idx_change_project" json:"project_id"`
	BranchID      *string    `gorm:"size:64;index" json:"branch_id"` // nil=main change
	AgentID       string     `gorm:"size:64;not null;index:idx_change_agent" json:"agent_id"`
	TaskID        *string    `gorm:"size:64" json:"task_id"`
	Version       string     `gorm:"size:8;not null" json:"version"`
	ModifiedFiles string     `gorm:"type:json" json:"modified_files"`
	NewFiles      string     `gorm:"type:json" json:"new_files"`
	DeletedFiles  string     `gorm:"type:json" json:"deleted_files"`
	Diff          string     `gorm:"type:json" json:"diff"`
	Description   string     `gorm:"type:text" json:"description"`
	Status        string     `gorm:"size:20;default:'pending'" json:"status"` // pending/pending_human_confirm/approved/rejected
	AuditLevel    *string    `gorm:"size:2" json:"audit_level"`               // L0/L1/L2
	AuditReason   string     `gorm:"type:text" json:"audit_reason"`
	FailureMode   string     `gorm:"size:64" json:"failure_mode"`             // wrong_assumption/missing_context/tool_misuse/over_edit/invalid_output/incomplete_fix
	RetryCount    int        `gorm:"default:0" json:"retry_count"`            // how many times this task was resubmitted
	ReviewedAt    *time.Time `json:"reviewed_at"`

	// InjectedArtifacts is the JSON-encoded list of KnowledgeArtifact IDs
	// that were surfaced to the claiming MCP client via task.claim hints
	// and are therefore accountable for this change's outcome. Echoed back
	// by the client on change.submit; consumed by HandleChangeAudit when
	// the audit verdict lands (L0=success, L2=failure) to bump the
	// artifacts' success_count / failure_count.
	InjectedArtifacts string `gorm:"type:json;default:null" json:"injected_artifacts"`

	// FeedbackApplied guards against double-accounting: once an audit
	// verdict has been translated into artifact counter bumps, we set
	// this so retries or re-submissions don't re-fire the feedback.
	FeedbackApplied bool `gorm:"default:false" json:"feedback_applied"`

	CreatedAt     time.Time  `gorm:"index:idx_change_created" json:"created_at"`
}

func (Change) TableName() string { return "change" }

// Branch represents a feature branch in a project
type Branch struct {
	ID           string     `gorm:"primaryKey;size:64" json:"id"`
	ProjectID    string     `gorm:"size:64;not null;index:idx_branch_project" json:"project_id"`
	Name         string     `gorm:"size:128;not null" json:"name"`                // feature/alice-login-a3f1
	BaseCommit   string     `gorm:"size:64" json:"base_commit"`                   // main commit hash at creation
	BaseVersion  string     `gorm:"size:8" json:"base_version"`                    // main version at creation
	Status       string     `gorm:"size:20;default:'active'" json:"status"`        // active/merged/closed
	CreatorID    string     `gorm:"size:64;not null;index:idx_branch_creator" json:"creator_id"`
	OccupantID   *string    `gorm:"size:64;index" json:"occupant_id"`              // current agent, nil=vacant
	LastActiveAt *time.Time `json:"last_active_at"`
	CreatedAt    time.Time  `json:"created_at"`
	MergedAt     *time.Time `json:"merged_at"`
	ClosedAt     *time.Time `json:"closed_at"`
}

func (Branch) TableName() string { return "branch" }

// PullRequest represents a merge request from branch to main
type PullRequest struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	ProjectID         string     `gorm:"size:64;not null;index:idx_pr_project" json:"project_id"`
	BranchID          string     `gorm:"size:64;not null;index:idx_pr_branch" json:"branch_id"`
	Title             string     `gorm:"size:256;not null" json:"title"`
	Description       string     `gorm:"type:text" json:"description"`
	SelfReview        string     `gorm:"type:text;not null" json:"self_review"`          // agent self-review JSON string
	DiffStat          string     `gorm:"type:text" json:"diff_stat"`                     // git diff --stat
	DiffFull          string     `gorm:"type:text" json:"diff_full"`                    // git diff full
	Status            string     `gorm:"size:20;default:'pending_human_review'" json:"status"` // pending_human_review/evaluating/evaluated/pending_human_merge/merged/rejected/merge_failed
	SubmitterID       string     `gorm:"size:64;not null;index:idx_pr_submitter" json:"submitter_id"`
	TechReview        string     `gorm:"type:text" json:"tech_review"`                   // evaluation agent result JSON string
	BizReview         string     `gorm:"type:text" json:"biz_review"`                    // maintain agent result JSON string
	VersionSuggestion string     `gorm:"size:8" json:"version_suggestion"`               // maintain agent version suggestion
	ConflictFiles     string     `gorm:"type:text" json:"conflict_files"`                // conflict file list JSON string
	CreatedAt         time.Time  `json:"created_at"`
	MergedAt          *time.Time `json:"merged_at"`
}

func (PullRequest) TableName() string { return "pull_request" }

// RoleOverride stores per-role model configuration overrides
type RoleOverride struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	Role          string    `gorm:"size:32;not null" json:"role"`
	ModelProvider string    `gorm:"size:64" json:"model_provider"`
	ModelID       string    `gorm:"size:128" json:"model_id"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (RoleOverride) TableName() string { return "role_override" }

// DialogueMessage stores one turn of a human↔agent multi-round
// conversation on the dashboard. Replaces the opencode serve session
// that previously held dialogue history in-process. Each project has
// per-channel history (channel = "chief" for the Chief chat tab,
// "maintain" for the main dashboard input talking to Maintain).
//
// The native runner is stateless per session, so every new user turn
// spawns a fresh agent session. This table is how we feed prior turns
// back into the new session's prompt — see
// @platform/backend/internal/service/dialogue.go for the loader used
// by TriggerChiefChat / TriggerMaintainAgent.
type DialogueMessage struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	ProjectID string    `gorm:"size:64;not null;index:idx_dialogue_project_channel,priority:1" json:"project_id"`
	// Channel is the conversation lane. "chief" for Chief chat,
	// "maintain" for dashboard input → Maintain agent. Additional
	// channels can be added without migration (column is a string).
	Channel string `gorm:"size:32;not null;index:idx_dialogue_project_channel,priority:2" json:"channel"`
	// SessionID is the AgentSession that produced this message
	// (empty for user-origin rows until a session runs in response).
	SessionID string `gorm:"size:64;index" json:"session_id"`
	// Role is who spoke this turn. "user" means a human typed it;
	// "assistant" means the agent emitted it. We deliberately don't
	// reuse llm.Role here — the values stored are dashboard-facing
	// and we don't want callers coupled to the llm package just to
	// append a message.
	Role      string    `gorm:"size:16;not null" json:"role"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

func (DialogueMessage) TableName() string { return "dialogue_message" }