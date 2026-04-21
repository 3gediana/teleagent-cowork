package handler

import (
	"encoding/json"
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

type PRHandler struct{}

func NewPRHandler() *PRHandler {
	return &PRHandler{}
}

// Submit creates a Pull Request from the agent's current branch
func (h *PRHandler) Submit(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	var agent model.Agent
	if model.DB.Where("id = ?", agentID).First(&agent).Error != nil || agent.CurrentBranchID == nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "NOT_ON_BRANCH", "message": "Must be on a branch to submit PR"}})
		return
	}

	var req struct {
		Title       string `json:"title" binding:"required"`
		Description string `json:"description"`
		SelfReview  string `json:"self_review" binding:"required"` // JSON string with self-review
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	// Validate self_review is valid JSON
	var selfReview interface{}
	if err := json.Unmarshal([]byte(req.SelfReview), &selfReview); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_SELF_REVIEW", "message": "self_review must be valid JSON"}})
		return
	}

	branchID := *agent.CurrentBranchID

	// Check if branch already has an open PR
	var existingPR model.PullRequest
	if model.DB.Where("branch_id = ? AND status IN ?", branchID,
		[]string{"pending_human_review", "evaluating", "evaluated", "pending_human_merge"}).First(&existingPR).Error == nil {
		c.JSON(409, gin.H{"success": false, "error": gin.H{
			"code":    "PR_ALREADY_EXISTS",
			"message": "Branch already has an open PR",
			"pr_id":   existingPR.ID,
		}})
		return
	}

	// Generate diff
	diffStat, diffFull, err := service.GenerateBranchDiff(branchID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "DIFF_FAILED", "message": err.Error()}})
		return
	}

	prID := model.GenerateID("pr")
	pr := &model.PullRequest{
		ID:          prID,
		ProjectID:   *agent.CurrentProjectID,
		BranchID:    branchID,
		Title:       req.Title,
		Description: req.Description,
		SelfReview:  req.SelfReview,
		DiffStat:    diffStat,
		DiffFull:    diffFull,
		Status:      "pending_human_review",
		SubmitterID: agentID.(string),
	}

	if err := model.DB.Create(pr).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	// Check AutoMode: if true, trigger Chief Agent for risk assessment instead of waiting for human
	var project model.Project
	autoMode := false
	if model.DB.Where("id = ?", *agent.CurrentProjectID).First(&project).Error == nil {
		autoMode = project.AutoMode
	}

	if autoMode {
		// Chief Agent will evaluate risk and decide whether to approve_review
		go service.TriggerChiefDecision(*agent.CurrentProjectID, "pr_review", pr.ID)

		service.BroadcastEvent(*agent.CurrentProjectID, "PR_SUBMITTED", map[string]interface{}{
			"pr_id":        prID,
			"title":        req.Title,
			"branch_id":    branchID,
			"submitter_id": agentID.(string),
			"status":       "pending_human_review",
			"auto_mode":    true,
		})

		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"id":     prID,
				"status": "pending_human_review",
				"title":  req.Title,
				"message": "PR submitted (AutoMode), Chief Agent will assess risk",
			},
		})
		return
	}

	// Broadcast PR submitted event
	service.BroadcastEvent(*agent.CurrentProjectID, "PR_SUBMITTED", map[string]interface{}{
		"pr_id":        prID,
		"title":        req.Title,
		"branch_id":    branchID,
		"submitter_id": agentID.(string),
		"status":       "pending_human_review",
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":     prID,
			"status": "pending_human_review",
			"title":  req.Title,
			"message": "PR submitted, waiting for human review approval",
		},
	})
}

// List returns all PRs for the current project
func (h *PRHandler) List(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	var agent model.Agent
	if model.DB.Where("id = ?", agentID).First(&agent).Error != nil || agent.CurrentProjectID == nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "NO_PROJECT", "message": "No project selected"}})
		return
	}

	var prs []model.PullRequest
	model.DB.Where("project_id = ?", *agent.CurrentProjectID).Order("created_at DESC").Find(&prs)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"pull_requests": prs,
		},
	})
}

// ApproveReview allows human to approve starting the evaluation
func (h *PRHandler) ApproveReview(c *gin.Context) {
	var req struct {
		PRID string `json:"pr_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	var pr model.PullRequest
	if model.DB.Where("id = ?", req.PRID).First(&pr).Error != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PR_NOT_FOUND", "message": "PR not found"}})
		return
	}

	if pr.Status != "pending_human_review" {
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "INVALID_STATUS", "message": "PR is not pending human review"}})
		return
	}

	pr.Status = "evaluating"
	model.DB.Save(&pr)

	// Broadcast evaluation started
	service.BroadcastEvent(pr.ProjectID, "PR_EVALUATION_STARTED", map[string]interface{}{
		"pr_id":  pr.ID,
		"title":  pr.Title,
	})

	// Trigger evaluate agent
	go func() {
		if err := service.TriggerEvaluateAgent(&pr); err != nil {
			log.Printf("[PR] Failed to trigger evaluate agent for PR %s: %v", pr.ID, err)
		}
	}()

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"pr_id":  pr.ID,
			"status": "evaluating",
			"message": "Evaluation approved, tech review will begin",
		},
	})
}

// ApproveMerge allows human to confirm merging the PR
func (h *PRHandler) ApproveMerge(c *gin.Context) {
	var req struct {
		PRID    string `json:"pr_id" binding:"required"`
		Version string `json:"version"` // optional override version
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	var pr model.PullRequest
	if model.DB.Where("id = ?", req.PRID).First(&pr).Error != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PR_NOT_FOUND", "message": "PR not found"}})
		return
	}

	if pr.Status != "pending_human_merge" {
		c.JSON(409, gin.H{"success": false, "error": gin.H{"code": "INVALID_STATUS", "message": "PR is not pending human merge"}})
		return
	}

	pr.Status = "merging"
	model.DB.Save(&pr)

	// Execute merge
	if err := service.ExecuteMerge(pr.BranchID); err != nil {
		pr.Status = "merge_failed"
		model.DB.Save(&pr)

		// Broadcast merge failed
		service.BroadcastEvent(pr.ProjectID, "PR_MERGE_FAILED", map[string]interface{}{
			"pr_id":  pr.ID,
			"title":  pr.Title,
			"reason": err.Error(),
		})

		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "MERGE_FAILED", "message": err.Error()}})
		return
	}

	// Merge succeeded
	now := time.Now()
	pr.Status = "merged"
	pr.MergedAt = &now
	model.DB.Save(&pr)

	// Determine version upgrade
	var versionSuggestion string
	if req.Version != "" {
		versionSuggestion = req.Version
	} else if pr.VersionSuggestion != "" {
		versionSuggestion = pr.VersionSuggestion
	}

	var newVersion string
	if versionSuggestion != "" {
		newVersion = versionSuggestion
		// Update version block
		versionBlock, _ := repo.GetContentBlock(pr.ProjectID, "version")
		if versionBlock != nil {
			versionBlock.Content = newVersion
			versionBlock.Version++
			model.DB.Save(versionBlock)
		}
		service.GitTagVersion(pr.ProjectID, newVersion)
	} else {
		// Default: increment minor version
		newVersion, _ = service.IncrementVersion(pr.ProjectID)
	}

	// Broadcast merge success
	service.BroadcastEvent(pr.ProjectID, "PR_MERGED", map[string]interface{}{
		"pr_id":       pr.ID,
		"title":       pr.Title,
		"new_version": newVersion,
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"pr_id":       pr.ID,
			"status":      "merged",
			"new_version": newVersion,
			"message":     "PR merged successfully",
		},
	})
}

// Reject allows human to reject a PR
func (h *PRHandler) Reject(c *gin.Context) {
	var req struct {
		PRID   string `json:"pr_id" binding:"required"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	var pr model.PullRequest
	if model.DB.Where("id = ?", req.PRID).First(&pr).Error != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PR_NOT_FOUND", "message": "PR not found"}})
		return
	}

	pr.Status = "rejected"
	model.DB.Save(&pr)

	// Broadcast PR rejected
	service.BroadcastEvent(pr.ProjectID, "PR_REJECTED", map[string]interface{}{
		"pr_id":  pr.ID,
		"title":  pr.Title,
		"reason": req.Reason,
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"pr_id":  pr.ID,
			"status": "rejected",
			"message": "PR rejected",
		},
	})
}

// GetPR returns a single PR with full details
func (h *PRHandler) GetPR(c *gin.Context) {
	prID := c.Param("pr_id")
	var pr model.PullRequest
	if model.DB.Where("id = ?", prID).First(&pr).Error != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PR_NOT_FOUND", "message": "PR not found"}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": pr})
}
