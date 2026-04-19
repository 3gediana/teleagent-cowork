package repo

import (
	"errors"

	"github.com/a3c/platform/internal/model"
	"gorm.io/gorm"
)

func GetProjectByID(id string) (*model.Project, error) {
	var p model.Project
	if err := model.DB.Where("id = ?", id).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func GetAgentByKey(key string) (*model.Agent, error) {
	var a model.Agent
	if err := model.DB.Where("access_key = ?", key).First(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

func GetAgentByID(id string) (*model.Agent, error) {
	var a model.Agent
	if err := model.DB.Where("id = ?", id).First(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

func GetTasksByProject(projectID string) ([]model.Task, error) {
	var tasks []model.Task
	err := model.DB.Where("project_id = ? AND status != 'deleted'", projectID).Find(&tasks).Error
	return tasks, err
}

func GetLocksByProject(projectID string) ([]model.FileLock, error) {
	var locks []model.FileLock
	err := model.DB.Where("project_id = ? AND released_at IS NULL AND expires_at > NOW()", projectID).Find(&locks).Error
	return locks, err
}

func GetCurrentMilestone(projectID string) (*model.Milestone, error) {
	var m model.Milestone
	if err := model.DB.Where("project_id = ? AND status = 'in_progress'", projectID).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

func GetContentBlock(projectID, blockType string) (*model.ContentBlock, error) {
	var cb model.ContentBlock
	if err := model.DB.Where("project_id = ? AND block_type = ?", projectID, blockType).First(&cb).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &cb, nil
}