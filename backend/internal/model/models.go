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
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (Project) TableName() string { return "project" }

type Agent struct {
	ID              string     `gorm:"primaryKey;size:64" json:"id"`
	Name            string     `gorm:"size:128;not null;uniqueIndex" json:"name"`
	AccessKey       string     `gorm:"size:256;not null" json:"-"`
	SessionID       string     `gorm:"size:128" json:"session_id"`
	Status          string     `gorm:"size:20;default:'offline'" json:"status"` // online/offline
	CurrentProjectID *string   `gorm:"size:64;index" json:"current_project_id"`
	LastHeartbeat   *time.Time `json:"last_heartbeat"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
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
	AgentID       string     `gorm:"size:64;not null;index:idx_change_agent" json:"agent_id"`
	TaskID        *string    `gorm:"size:64" json:"task_id"`
	Version       string     `gorm:"size:8;not null" json:"version"`
	ModifiedFiles string     `gorm:"type:json" json:"modified_files"`
	NewFiles      string     `gorm:"type:json" json:"new_files"`
	DeletedFiles  string     `gorm:"type:json" json:"deleted_files"`
	Diff          string     `gorm:"type:json" json:"diff"`
	Description   string     `gorm:"type:text" json:"description"`
	Status        string     `gorm:"size:20;default:'pending'" json:"status"` // pending/approved/rejected
	AuditLevel    *string    `gorm:"size:2" json:"audit_level"`               // L0/L1/L2
	AuditReason   string     `gorm:"type:text" json:"audit_reason"`
	ReviewedAt    *time.Time `json:"reviewed_at"`
	CreatedAt     time.Time  `gorm:"index:idx_change_created" json:"created_at"`
}

func (Change) TableName() string { return "change" }