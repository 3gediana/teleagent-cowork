package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
)

type TaskHandler struct{}

func NewTaskHandler() *TaskHandler {
	return &TaskHandler{}
}

func (h *TaskHandler) List(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	tasks, err := repo.GetTasksByProject(projectID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Internal error"}})
		return
	}

	result := make([]gin.H, 0, len(tasks))
	for _, t := range tasks {
		assigneeName := ""
		if t.AssigneeID != nil {
			agent, _ := repo.GetAgentByID(*t.AssigneeID)
			if agent != nil {
				assigneeName = agent.Name
			}
		}
		result = append(result, gin.H{
			"id":            t.ID,
			"name":          t.Name,
			"description":   t.Description,
			"status":        t.Status,
			"assignee_id":   t.AssigneeID,
			"assignee_name": assigneeName,
			"milestone_id":  t.MilestoneID,
			"priority":      t.Priority,
		})
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"tasks": result}})
}

func (h *TaskHandler) Claim(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	aid := agentID.(string)

	var req struct {
		TaskID string `json:"task_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	var claimedTask model.Task
	if err := model.DB.Where("assignee_id = ? AND status = 'claimed'", aid).First(&claimedTask).Error; err == nil {
		c.JSON(409, gin.H{
			"success": false,
			"error": gin.H{
				"code":    "AGENT_HAS_TASK",
				"message": "You already have a claimed task",
			},
			"data": gin.H{
				"claimed_task_id":   claimedTask.ID,
				"claimed_task_name": claimedTask.Name,
			},
		})
		return
	}

	result := model.DB.Model(&model.Task{}).
		Where("id = ? AND status = 'pending'", req.TaskID).
		Updates(map[string]interface{}{
			"status":      "claimed",
			"assignee_id": aid,
		})

	if result.Error != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to claim task"}})
		return
	}

	if result.RowsAffected == 0 {
		var task model.Task
		if err := model.DB.Where("id = ?", req.TaskID).First(&task).Error; err != nil {
			c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "TASK_NOT_FOUND", "message": "Task not found"}})
			return
		}
		if task.Status == "claimed" {
			c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "TASK_CLAIMED", "message": "Task already claimed"}})
			return
		}
		if task.Status == "completed" {
			c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "TASK_COMPLETED", "message": "Task already completed"}})
			return
		}
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "TASK_UNCLAIMABLE", "message": "Task cannot be claimed"}})
		return
	}

	var task model.Task
	model.DB.Where("id = ?", req.TaskID).First(&task)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":           task.ID,
			"name":         task.Name,
			"description":  task.Description,
			"milestone_id": task.MilestoneID,
			"priority":     task.Priority,
		},
	})
}

func (h *TaskHandler) Complete(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	var req struct {
		TaskID string `json:"task_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	var task model.Task
	if err := model.DB.Where("id = ?", req.TaskID).First(&task).Error; err != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "TASK_NOT_FOUND", "message": "Task not found"}})
		return
	}

	if task.AssigneeID == nil || *task.AssigneeID != agentID.(string) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "TASK_NOT_CLAIMED_BY_YOU", "message": "Task not claimed by you"}})
		return
	}

	now := time.Now()
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"status":       "completed",
			"completed_at": now,
		}).Error; err != nil {
			return err
		}

		if err := tx.Model(&model.FileLock{}).
			Where("task_id = ? AND released_at IS NULL", task.ID).
			Update("released_at", now).Error; err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to complete task"}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":     task.ID,
			"name":   task.Name,
			"status": "completed",
		},
	})
}

func (h *TaskHandler) Create(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	projectID := c.Query("project_id")

	var req struct {
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
		MilestoneID string `json:"milestone_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if req.Priority == "" {
		req.Priority = "medium"
	}

	milestoneID := req.MilestoneID
	if milestoneID == "" {
		milestone, _ := repo.GetCurrentMilestone(projectID)
		if milestone != nil {
			milestoneID = milestone.ID
		}
	}

	priority := req.Priority
	if priority != "high" && priority != "medium" && priority != "low" {
		priority = "medium"
	}

	task := model.Task{
		ID:          model.GenerateID("task"),
		ProjectID:   projectID,
		MilestoneID: nil,
		Name:        req.Name,
		Description: req.Description,
		Priority:    priority,
		Status:      "pending",
		CreatedBy:   agentID.(string),
	}
	if milestoneID != "" {
		task.MilestoneID = &milestoneID
	}

	if err := model.DB.Create(&task).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to create task"}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":          task.ID,
			"name":        task.Name,
			"description": task.Description,
			"status":      task.Status,
			"priority":    task.Priority,
			"milestone_id": task.MilestoneID,
		},
	})
}

func (h *TaskHandler) Delete(c *gin.Context) {
	taskID := c.Param("task_id")

	var task model.Task
	if err := model.DB.Where("id = ?", taskID).First(&task).Error; err != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "TASK_NOT_FOUND", "message": "Task not found"}})
		return
	}

	if task.Status == "claimed" {
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "TASK_ALREADY_CLAIMED", "message": "Cannot delete claimed task"}})
		return
	}

	now := time.Now()
	task.Status = "deleted"
	task.DeletedAt = &now
	model.DB.Save(&task)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":     task.ID,
			"name":   task.Name,
			"status": "deleted",
		},
	})
}