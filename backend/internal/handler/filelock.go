package handler

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"
)

type FileLockHandler struct{}

func NewFileLockHandler() *FileLockHandler {
	return &FileLockHandler{}
}

type AcquireLockRequest struct {
	TaskID string   `json:"task_id" binding:"required"`
	Files  []string `json:"files" binding:"required"`
	Reason string   `json:"reason" binding:"required"`
}

func (h *FileLockHandler) Acquire(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	projectID := c.Query("project_id")

	var req AcquireLockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	var existing []model.FileLock
	model.DB.Where("project_id = ? AND released_at IS NULL AND expires_at > ?", projectID, time.Now()).Find(&existing)

	for _, lock := range existing {
		if lock.AgentID == agentID.(string) && lock.TaskID == req.TaskID {
			var lockFiles []string
			json.Unmarshal([]byte(lock.Files), &lockFiles)
			conflict := false
			for _, lf := range lockFiles {
				for _, rf := range req.Files {
					if lf == rf {
						conflict = true
						break
					}
				}
				if conflict {
					break
				}
			}
			if !conflict {
				merged := append(lockFiles, req.Files...)
				mergedJSON, _ := json.Marshal(merged)
				lock.Files = string(mergedJSON)
				lock.ExpiresAt = time.Now().Add(5 * time.Minute)
				model.DB.Save(&lock)
				c.JSON(200, gin.H{
					"success": true,
					"data": gin.H{
						"locked_files": merged,
						"expires_at":   lock.ExpiresAt.Format(time.RFC3339),
					},
				})
				return
			}
			continue
		}

		if lock.AgentID != agentID.(string) {
			var lockFiles []string
			json.Unmarshal([]byte(lock.Files), &lockFiles)
			for _, lf := range lockFiles {
				for _, rf := range req.Files {
					if lf == rf {
						c.JSON(409, gin.H{
							"success": false,
							"error": gin.H{
								"code":    "LOCK_CONFLICT",
								"message": "Some files are already locked",
								"conflict_files": []gin.H{{
									"file":       lf,
									"locked_by":  lockGetAgentName(lock.AgentID),
									"task_id":    lock.TaskID,
									"expires_at": lock.ExpiresAt.Format(time.RFC3339),
								}},
							},
						})
						return
					}
				}
			}
		}
	}

	var task model.Task
	if err := model.DB.Where("id = ? AND status = 'claimed' AND assignee_id = ?", req.TaskID, agentID.(string)).First(&task).Error; err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "TASK_NOT_CLAIMED_BY_YOU", "message": "Task not claimed by you"}})
		return
	}

	now := time.Now()
	expiresAt := now.Add(5 * time.Minute)
	filesJSON, _ := json.Marshal(req.Files)

	lock := model.FileLock{
		ID:         model.GenerateID("lock"),
		ProjectID: projectID,
		TaskID:     req.TaskID,
		AgentID:    agentID.(string),
		Files:      string(filesJSON),
		Reason:     req.Reason,
		AcquiredAt: now,
		ExpiresAt:  expiresAt,
	}

	if err := model.DB.Create(&lock).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to create lock"}})
		return
	}

	service.BroadcastEvent(projectID, "LOCK_ACQUIRED", gin.H{
		"lock_id":      lock.ID,
		"task_id":      req.TaskID,
		"agent_id":     agentID.(string),
		"locked_files": req.Files,
		"reason":       req.Reason,
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"locked_files": req.Files,
			"expires_at":   expiresAt.Format(time.RFC3339),
		},
	})
}

func (h *FileLockHandler) Release(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	projectID := c.Query("project_id")

	var req struct {
		Files []string `json:"files"`
	}
	c.ShouldBindJSON(&req)

	now := time.Now()
	var locks []model.FileLock
	model.DB.Where("agent_id = ? AND project_id = ? AND released_at IS NULL", agentID.(string), projectID).Find(&locks)

	released := make([]string, 0)
	if len(req.Files) > 0 {
		for i := range locks {
			var lockFiles []string
			json.Unmarshal([]byte(locks[i].Files), &lockFiles)
			remaining := make([]string, 0)
			releasedInLock := false
			for _, lf := range lockFiles {
				found := false
				for _, rf := range req.Files {
					if lf == rf {
						released = append(released, lf)
						found = true
						releasedInLock = true
						break
					}
				}
				if !found {
					remaining = append(remaining, lf)
				}
			}
			if releasedInLock {
				if len(remaining) == 0 {
					locks[i].ReleasedAt = &now
					model.DB.Save(&locks[i])
				} else {
					remainingJSON, _ := json.Marshal(remaining)
					locks[i].Files = string(remainingJSON)
					model.DB.Save(&locks[i])
				}
			}
		}
	} else {
		for i := range locks {
			locks[i].ReleasedAt = &now
			model.DB.Save(&locks[i])
			var lockFiles []string
			json.Unmarshal([]byte(locks[i].Files), &lockFiles)
			released = append(released, lockFiles...)
		}
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"released_files": released,
		},
	})
}

func (h *FileLockHandler) Renew(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	projectID := c.Query("project_id")

	var locks []model.FileLock
	model.DB.Where("agent_id = ? AND project_id = ? AND released_at IS NULL AND expires_at > ?", agentID.(string), projectID, time.Now()).Find(&locks)

	now := time.Now()
	expiresAt := now.Add(5 * time.Minute)

	renewed := make([]gin.H, 0)
	for i := range locks {
		locks[i].ExpiresAt = expiresAt
		model.DB.Save(&locks[i])
		var files []string
		json.Unmarshal([]byte(locks[i].Files), &files)
		renewed = append(renewed, gin.H{
			"lock_id":   locks[i].ID,
			"files":     files,
			"expires_at": expiresAt.Format(time.RFC3339),
		})
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"lock_ttl":  300,
			"renewed":   renewed,
			"expires_at": expiresAt.Format(time.RFC3339),
		},
	})
}

func lockGetAgentName(agentID string) string {
	var agent model.Agent
	if err := model.DB.Where("id = ?", agentID).First(&agent).Error; err != nil {
		return agentID
	}
	return agent.Name
}