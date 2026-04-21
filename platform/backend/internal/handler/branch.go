package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"
)

type BranchHandler struct{}

func NewBranchHandler() *BranchHandler {
	return &BranchHandler{}
}

// Create creates a new feature branch
func (h *BranchHandler) Create(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	var agent model.Agent
	if model.DB.Where("id = ?", agentID).First(&agent).Error != nil || agent.CurrentProjectID == nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "NO_PROJECT", "message": "No project selected"}})
		return
	}

	var req struct {
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	// Validate branch name (no spaces, special chars)
	if len(req.Name) == 0 || len(req.Name) > 64 {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_NAME", "message": "Branch name must be 1-64 characters"}})
		return
	}

	// Prefix with agent name for clarity
	branchName := "feature/" + agent.Name + "-" + req.Name

	branch, err := service.CreateBranch(*agent.CurrentProjectID, branchName, agentID.(string))
	if err != nil {
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "BRANCH_CREATE_FAILED", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":           branch.ID,
			"name":         branch.Name,
			"base_version": branch.BaseVersion,
			"status":       branch.Status,
			"created_at":   branch.CreatedAt,
		},
	})
}

// Enter enters a branch (marks agent as occupant)
func (h *BranchHandler) Enter(c *gin.Context) {
	agentID, _ := c.Get("agent_id")

	var req struct {
		BranchID string `json:"branch_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if err := service.EnterBranch(req.BranchID, agentID.(string)); err != nil {
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "BRANCH_ENTER_FAILED", "message": err.Error()}})
		return
	}

	// Return branch context
	branchInfo, _ := service.GetBranchInfo(req.BranchID)
	files, _ := service.GetBranchFiles(req.BranchID)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"branch": branchInfo,
			"files":  files,
		},
	})
}

// Leave leaves the current branch
func (h *BranchHandler) Leave(c *gin.Context) {
	agentID, _ := c.Get("agent_id")

	if err := service.LeaveBranch(agentID.(string)); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "BRANCH_LEAVE_FAILED", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"message": "Left branch, back on main"}})
}

// List returns all branches for the current project
func (h *BranchHandler) List(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	var agent model.Agent
	if model.DB.Where("id = ?", agentID).First(&agent).Error != nil || agent.CurrentProjectID == nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "NO_PROJECT", "message": "No project selected"}})
		return
	}

	branches, err := service.ListBranchesWithOccupants(*agent.CurrentProjectID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"branches": branches}})
}

// Close closes a branch
func (h *BranchHandler) Close(c *gin.Context) {
	var req struct {
		BranchID string `json:"branch_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if err := service.CloseBranch(req.BranchID); err != nil {
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "BRANCH_CLOSE_FAILED", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"message": "Branch closed"}})
}

// BranchChangeSubmit writes files to the current branch worktree (no audit, no version check)
func (h *BranchHandler) BranchChangeSubmit(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	var agent model.Agent
	if model.DB.Where("id = ?", agentID).First(&agent).Error != nil || agent.CurrentBranchID == nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "NOT_ON_BRANCH", "message": "Not on a branch. Use branch/enter first."}})
		return
	}

	var req struct {
		Writes      []model.ChangeFileEntry `json:"writes"`
		Deletes     []string                 `json:"deletes"`
		Description string                   `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	branchID := *agent.CurrentBranchID

	// Write files to branch worktree
	if err := service.WriteBranchFiles(branchID, req.Writes, req.Deletes); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "WRITE_FAILED", "message": err.Error()}})
		return
	}

	// Auto-commit in the branch
	description := req.Description
	if description == "" {
		description = "branch change"
	}
	if err := service.BranchCommit(branchID, description); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "COMMIT_FAILED", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"branch_id": branchID,
			"message":   "Changes written to branch",
		},
	})
}

// BranchFileSync returns all files in the current branch worktree
func (h *BranchHandler) BranchFileSync(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	var agent model.Agent
	if model.DB.Where("id = ?", agentID).First(&agent).Error != nil || agent.CurrentBranchID == nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "NOT_ON_BRANCH", "message": "Not on a branch"}})
		return
	}

	files, err := service.GetBranchFiles(*agent.CurrentBranchID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{
		"success":   true,
		"data": gin.H{
			"branch_id": *agent.CurrentBranchID,
			"files":     files,
		},
	})
}

// SyncMain merges main into the current branch
func (h *BranchHandler) SyncMain(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	var agent model.Agent
	if model.DB.Where("id = ?", agentID).First(&agent).Error != nil || agent.CurrentBranchID == nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "NOT_ON_BRANCH", "message": "Not on a branch"}})
		return
	}

	conflictFiles, err := service.SyncMain(*agent.CurrentBranchID)
	if err != nil {
		c.JSON(409, gin.H{
			"success": false,
			"error": gin.H{
				"code":    "SYNC_CONFLICTS",
				"message": "Merge conflicts detected when syncing main into branch",
			},
			"conflict_files": conflictFiles,
		})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"message": "Main synced into branch successfully"}})
}

