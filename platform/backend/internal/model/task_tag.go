package model

import "time"

// TaskTag stores categorization tags for tasks, used for task profiling and policy matching.
type TaskTag struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	TaskID    string    `gorm:"size:64;not null;index:idx_task_tag_task" json:"task_id"`
	Tag       string    `gorm:"size:64;not null;index:idx_task_tag_tag" json:"tag"`
	Source    string    `gorm:"size:20;default:'human'" json:"source"` // human/auto/chief
	CreatedAt time.Time `json:"created_at"`
}

func (TaskTag) TableName() string { return "task_tag" }
