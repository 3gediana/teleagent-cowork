package handler

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

type MilestoneHandler struct{}

func NewMilestoneHandler() *MilestoneHandler {
	return &MilestoneHandler{}
}

type MilestoneSwitchRequest struct {
	MilestoneID string `json:"milestone_id"`
}

func (h *MilestoneHandler) Switch(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	var req MilestoneSwitchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	currentMilestone, err := repo.GetCurrentMilestone(projectID)
	if err != nil || currentMilestone == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "MILESTONE_NOT_FOUND", "message": "No active milestone"}})
		return
	}

	tasks, _ := repo.GetTasksByProject(projectID)
	for _, t := range tasks {
		if t.Status != "completed" && t.Status != "deleted" {
			c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "MILESTONE_HAS_ACTIVE_TASKS", "message": "All tasks must be completed before switching milestones"}})
			return
		}
	}

	direction, _ := repo.GetContentBlock(projectID, "direction")
	versionBlock, _ := repo.GetContentBlock(projectID, "version")

	directionContent := ""
	if direction != nil {
		directionContent = direction.Content
	}

	taskArchive := make([]gin.H, 0)
	for _, t := range tasks {
		taskArchive = append(taskArchive, gin.H{
			"id":            t.ID,
			"name":          t.Name,
			"description":   t.Description,
			"status":        t.Status,
			"priority":      t.Priority,
			"assignee_name": dashboardGetAgentNamePtr(t.AssigneeID),
		})
	}
	taskArchiveJSON, _ := json.Marshal(taskArchive)

	currentVersion := "v1.0"
	if versionBlock != nil {
		currentVersion = versionBlock.Content
	}

	now := time.Now()
	archive := model.MilestoneArchive{
		ID:                model.GenerateID("arch"),
		ProjectID:         projectID,
		MilestoneID:       currentMilestone.ID,
		Name:              currentMilestone.Name,
		Description:       currentMilestone.Description,
		DirectionSnapshot: directionContent,
		Tasks:             string(taskArchiveJSON),
		VersionStart:      currentVersion,
		VersionEnd:        currentVersion,
		CreatedAt:         now,
	}
	model.DB.Create(&archive)

	currentMilestone.Status = "completed"
	currentMilestone.CompletedAt = &now
	model.DB.Save(currentMilestone)

	for _, t := range tasks {
		model.DB.Delete(&t)
	}

	var locks []model.FileLock
	model.DB.Where("project_id = ? AND released_at IS NULL", projectID).Find(&locks)
	for i := range locks {
		locks[i].ReleasedAt = &now
		model.DB.Save(&locks[i])
	}

	newVersion, _ := service.SwitchMilestoneVersion(projectID)

	newMilestone := model.Milestone{
		ID:        model.GenerateID("ms"),
		ProjectID: projectID,
		Name:      "New Milestone",
		Status:    "in_progress",
		CreatedBy: "system",
	}
	model.DB.Create(&newMilestone)

	service.BroadcastEvent(projectID, "MILESTONE_SWITCH", gin.H{
		"block_type":       "milestone",
		"old_milestone_id": currentMilestone.ID,
		"new_milestone_id": newMilestone.ID,
		"content":          newMilestone.Name,
		"new_version":      newVersion,
		"reason":           "milestone switch",
	})

	go func() {
		service.TriggerMaintainAgent(projectID, "milestone_switch", "New milestone started: "+newMilestone.Name)
	}()

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"archived_milestone": currentMilestone.ID,
			"new_milestone":      newMilestone.ID,
			"new_version":         newVersion,
		},
	})
}

func (h *MilestoneHandler) Archives(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	var archives []model.MilestoneArchive
	model.DB.Where("project_id = ?", projectID).Order("created_at desc").Find(&archives)

	result := make([]gin.H, 0, len(archives))
	for _, a := range archives {
		result = append(result, gin.H{
			"id":                a.ID,
			"milestone_id":      a.MilestoneID,
			"name":              a.Name,
			"description":       a.Description,
			"direction_snapshot": a.DirectionSnapshot,
			"tasks":             a.Tasks,
			"version_start":     a.VersionStart,
			"version_end":       a.VersionEnd,
			"created_at":        a.CreatedAt,
		})
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"archives": result}})
}