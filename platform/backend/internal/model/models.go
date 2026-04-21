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
	CurrentProjectID *string    `gorm:"size:64;index" json:"current_project_id"`
	CurrentBranchID  *string    `gorm:"size:64;index" json:"current_branch_id"` // nil=on main
	LastHeartbeat    *time.Time `json:"last_heartbeat"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
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
	ReviewedAt    *time.Time `json:"reviewed_at"`
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