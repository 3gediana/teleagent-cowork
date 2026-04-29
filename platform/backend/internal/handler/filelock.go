package handler

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"github.com/a3c/platform/internal/model"
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

	// Atomic acquire: wrap conflict check + insert/merge in a single transaction.
	// Previous implementation read then wrote without isolation, allowing two
	// concurrent Acquire calls on the same file to both succeed.
	aid := agentID.(string)
	type conflictInfo struct {
		file       string
		lockedBy   string
		taskID     string
		expiresAt  time.Time
	}
	var conflict *conflictInfo
	var responseFiles []string
	var responseExpiresAt time.Time

	txErr := model.DB.Transaction(func(tx *gorm.DB) error {
		// Verify the caller owns the task inside the transaction to avoid TOCTOU
		var task model.Task
		if err := tx.Where("id = ? AND status = 'claimed' AND assignee_id = ?", req.TaskID, aid).First(&task).Error; err != nil {
			return gorm.ErrRecordNotFound
		}

		var existing []model.FileLock
		// Lock the relevant rows for update to serialize concurrent acquires.
		tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND released_at IS NULL AND expires_at > ?", projectID, time.Now()).
			Find(&existing)

		// First pass: detect conflicts with other agents and idempotent hits
		for _, lock := range existing {
			if lock.AgentID == aid && lock.TaskID == req.TaskID {
				continue // merge handled in second pass
			}
			if lock.AgentID == aid {
				continue // same agent different task — fine
			}
			var lockFiles []string
			_ = json.Unmarshal([]byte(lock.Files), &lockFiles)
			for _, lf := range lockFiles {
				for _, rf := range req.Files {
					if lf == rf {
						conflict = &conflictInfo{file: lf, lockedBy: lockGetAgentName(lock.AgentID), taskID: lock.TaskID, expiresAt: lock.ExpiresAt}
						return nil
					}
				}
			}
		}

		// Second pass: if this agent+task already has a lock, merge (idempotent)
		for i := range existing {
			lock := &existing[i]
			if lock.AgentID != aid || lock.TaskID != req.TaskID {
				continue
			}
			var lockFiles []string
			_ = json.Unmarshal([]byte(lock.Files), &lockFiles)
			fileSet := make(map[string]bool, len(lockFiles))
			for _, f := range lockFiles {
				fileSet[f] = true
			}
			for _, f := range req.Files {
				if !fileSet[f] {
					lockFiles = append(lockFiles, f)
					fileSet[f] = true
				}
			}
			mergedJSON, _ := json.Marshal(lockFiles)
			lock.Files = string(mergedJSON)
			lock.ExpiresAt = time.Now().Add(5 * time.Minute)
			if err := tx.Save(lock).Error; err != nil {
				return err
			}
			responseFiles = lockFiles
			responseExpiresAt = lock.ExpiresAt
			return nil
		}

		// Otherwise create a fresh lock record
		now := time.Now()
		expiresAt := now.Add(5 * time.Minute)
		filesJSON, _ := json.Marshal(req.Files)
		lock := model.FileLock{
			ID:         model.GenerateID("lock"),
			ProjectID:  projectID,
			TaskID:     req.TaskID,
			AgentID:    aid,
			Files:      string(filesJSON),
			Reason:     req.Reason,
			AcquiredAt: now,
			ExpiresAt:  expiresAt,
		}
		if err := tx.Create(&lock).Error; err != nil {
			return err
		}
		responseFiles = req.Files
		responseExpiresAt = expiresAt
		return nil
	})

	if conflict != nil {
		c.JSON(409, gin.H{
			"success": false,
			"error": gin.H{
				"code":    "LOCK_CONFLICT",
				"message": "Some files are already locked",
				"conflict_files": []gin.H{{
					"file":       conflict.file,
					"locked_by":  conflict.lockedBy,
					"task_id":    conflict.taskID,
					"expires_at": conflict.expiresAt.Format(time.RFC3339),
				}},
			},
		})
		return
	}
	if txErr == gorm.ErrRecordNotFound {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "TASK_NOT_CLAIMED_BY_YOU", "message": "Task not claimed by you"}})
		return
	}
	if txErr != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to create lock"}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"locked_files": responseFiles,
			"expires_at":   responseExpiresAt.Format(time.RFC3339),
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
					model.SaveOrLog(&locks[i], "filelock")
				} else {
					remainingJSON, _ := json.Marshal(remaining)
					locks[i].Files = string(remainingJSON)
					model.SaveOrLog(&locks[i], "filelock")
				}
			}
		}
	} else {
		for i := range locks {
			locks[i].ReleasedAt = &now
			model.SaveOrLog(&locks[i], "filelock")
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

func (h *FileLockHandler) Check(c *gin.Context) {
	projectID := c.Query("project_id")

	var req struct {
		Files []string `json:"files" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	var locks []model.FileLock
	model.DB.Where("project_id = ? AND released_at IS NULL AND expires_at > ?", projectID, time.Now()).Find(&locks)

	// Build lock map: file -> lock info
	lockMap := make(map[string]gin.H)
	for _, lock := range locks {
		var files []string
		json.Unmarshal([]byte(lock.Files), &files)
		for _, f := range files {
			lockMap[f] = gin.H{
				"locked_by":   lockGetAgentName(lock.AgentID),
				"task_id":     lock.TaskID,
				"expires_at":  lock.ExpiresAt.Format(time.RFC3339),
				"expires_in":  int(time.Until(lock.ExpiresAt).Seconds()),
			}
		}
	}

	result := make([]gin.H, 0, len(req.Files))
	for _, f := range req.Files {
		if lockInfo, ok := lockMap[f]; ok {
			result = append(result, gin.H{
				"file":    f,
				"locked":  true,
				"details": lockInfo,
			})
		} else {
			result = append(result, gin.H{
				"file":   f,
				"locked": false,
			})
		}
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"files": result,
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
		model.SaveOrLog(&locks[i], "filelock")
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