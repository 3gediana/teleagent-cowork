package service

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/opencode"
	"github.com/a3c/platform/internal/repo"
)

// TriggerEvaluateAgent starts the evaluate agent for a PR
func TriggerEvaluateAgent(pr *model.PullRequest) error {
	if opencode.DefaultScheduler == nil {
		return fmt.Errorf("scheduler not initialized")
	}

	// Get project context
	direction, _ := repo.GetContentBlock(pr.ProjectID, "direction")
	milestone, _ := repo.GetCurrentMilestone(pr.ProjectID)

	directionContent := ""
	if direction != nil {
		directionContent = direction.Content
	}
	milestoneContent := ""
	if milestone != nil {
		milestoneContent = milestone.Name + ": " + milestone.Description
	}

	// Get submitter name
	var submitter model.Agent
	submitterName := "unknown"
	if model.DB.Where("id = ?", pr.SubmitterID).First(&submitter).Error == nil {
		submitterName = submitter.Name
	}

	// Get branch info
	var branch model.Branch
	branchName := "unknown"
	baseVersion := ""
	if model.DB.Where("id = ?", pr.BranchID).First(&branch).Error == nil {
		branchName = branch.Name
		baseVersion = branch.BaseVersion
	}

	// Dry-run merge check
	canMerge, conflictFiles, _ := DryRunMerge(pr.BranchID)
	mergeCheckResult := "No conflicts detected, safe to merge"
	if !canMerge {
		conflictJSON, _ := json.Marshal(conflictFiles)
		mergeCheckResult = fmt.Sprintf("Conflicts detected in files: %s", string(conflictJSON))
	}

	// Parse self_review for display
	selfReviewStr := pr.SelfReview
	var selfReview interface{}
	if json.Unmarshal([]byte(selfReviewStr), &selfReview) == nil {
		formatted, _ := json.MarshalIndent(selfReview, "", "  ")
		selfReviewStr = string(formatted)
	}

	ctx := &agent.SessionContext{
		DirectionBlock: directionContent,
		MilestoneBlock: milestoneContent,
		PRTitle:        pr.Title,
		PRDescription:  pr.Description,
		SubmitterName:   submitterName,
		BranchName:      branchName,
		BaseVersion:     baseVersion,
		SelfReview:      selfReviewStr,
		DiffStat:        pr.DiffStat,
		DiffFull:        pr.DiffFull,
		MergeCheckResult: mergeCheckResult,
	}

	sessionID := model.GenerateID("session")
	session := &agent.Session{
		ID:      sessionID,
		ProjectID: pr.ProjectID,
		Role:    agent.RoleEvaluate,
		Status:  "pending",
		Context: ctx,
		PRID:    pr.ID,
	}

	agent.DefaultManager.RegisterSession(session)

	if err := opencode.DefaultScheduler.Dispatch(session); err != nil {
		return fmt.Errorf("failed to dispatch evaluate agent: %w", err)
	}

	log.Printf("[PR] Evaluate agent dispatched for PR %s (session %s)", pr.ID, sessionID)
	return nil
}

// TriggerMergeAgent starts the merge agent for a PR
func TriggerMergeAgent(pr *model.PullRequest) error {
	if opencode.DefaultScheduler == nil {
		return fmt.Errorf("scheduler not initialized")
	}

	// Get branch info
	var branch model.Branch
	branchName := "unknown"
	if model.DB.Where("id = ?", pr.BranchID).First(&branch).Error == nil {
		branchName = branch.Name
	}

	// Get tech review for merge cost rating
	mergeCostRating := "unknown"
	conflictFiles := "none"
	if pr.TechReview != "" {
		var techReview map[string]interface{}
		if json.Unmarshal([]byte(pr.TechReview), &techReview) == nil {
			if rating, ok := techReview["merge_cost_rating"].(string); ok {
				mergeCostRating = rating
			}
			if cf, ok := techReview["conflict_files"].([]interface{}); ok {
				cfJSON, _ := json.Marshal(cf)
				conflictFiles = string(cfJSON)
			}
		}
	}

	ctx := &agent.SessionContext{
		PRTitle:         pr.Title,
		BranchName:      branchName,
		MergeCostRating: mergeCostRating,
		ConflictFiles:   conflictFiles,
	}

	sessionID := model.GenerateID("session")
	session := &agent.Session{
		ID:      sessionID,
		ProjectID: pr.ProjectID,
		Role:    agent.RoleMerge,
		Status:  "pending",
		Context: ctx,
		PRID:    pr.ID,
	}

	agent.DefaultManager.RegisterSession(session)

	if err := opencode.DefaultScheduler.Dispatch(session); err != nil {
		return fmt.Errorf("failed to dispatch merge agent: %w", err)
	}

	log.Printf("[PR] Merge agent dispatched for PR %s (session %s)", pr.ID, sessionID)
	return nil
}

// TriggerMaintainBizReview starts the maintain agent for PR business evaluation
func TriggerMaintainBizReview(pr *model.PullRequest) error {
	if opencode.DefaultScheduler == nil {
		return fmt.Errorf("scheduler not initialized")
	}

	// Get project context
	direction, _ := repo.GetContentBlock(pr.ProjectID, "direction")
	milestone, _ := repo.GetCurrentMilestone(pr.ProjectID)

	directionContent := ""
	if direction != nil {
		directionContent = direction.Content
	}
	milestoneContent := ""
	if milestone != nil {
		milestoneContent = milestone.Name + ": " + milestone.Description
	}

	// Get tech review summary
	techReviewSummary := "No tech review available"
	if pr.TechReview != "" {
		techReviewSummary = pr.TechReview
	}

	ctx := &agent.SessionContext{
		DirectionBlock:   directionContent,
		MilestoneBlock:   milestoneContent,
		PRTitle:          pr.Title,
		PRDescription:    pr.Description,
		TechReviewSummary: techReviewSummary,
		SelfReview:       pr.SelfReview,
	}

	sessionID := model.GenerateID("session")
	session := &agent.Session{
		ID:      sessionID,
		ProjectID: pr.ProjectID,
		Role:    agent.RoleMaintain,
		Status:  "pending",
		Context: ctx,
		PRID:    pr.ID,
	}

	if err := opencode.DefaultScheduler.Dispatch(session); err != nil {
		return fmt.Errorf("failed to dispatch maintain agent for biz review: %w", err)
	}

	log.Printf("[PR] Maintain agent (biz review) dispatched for PR %s (session %s)", pr.ID, sessionID)
	return nil
}

// HandleEvaluateOutput processes the evaluate agent's output
func HandleEvaluateOutput(sessionID, projectID string, args map[string]interface{}) error {
	// Find the PR associated with this session
	session := agent.DefaultManager.GetSession(sessionID)
	if session == nil || session.PRID == "" {
		return fmt.Errorf("no PR associated with session %s", sessionID)
	}

	var pr model.PullRequest
	if model.DB.Where("id = ?", session.PRID).First(&pr).Error != nil {
		return fmt.Errorf("PR %s not found", session.PRID)
	}

	// Extract evaluation results
	resultJSON, _ := json.Marshal(args)
	pr.TechReview = string(resultJSON)

	// Determine outcome
	result, _ := args["result"].(string) // approved / needs_work / conflicts / high_risk
	result = strings.ToLower(result)
	mergeCostRating, _ := args["merge_cost_rating"].(string)

	switch result {
	case "approved":
		// Tech review passed → trigger maintain agent for biz review
		pr.Status = "evaluated"
		model.DB.Save(&pr)

		// Trigger maintain agent for business evaluation
		go func() {
			if err := TriggerMaintainBizReview(&pr); err != nil {
				log.Printf("[PR] Failed to trigger biz review for PR %s: %v", pr.ID, err)
			}
		}()

	case "needs_work":
		pr.Status = "evaluated"
		model.DB.Save(&pr)
		BroadcastEvent(projectID, "PR_NEEDS_WORK", map[string]interface{}{
			"pr_id":  pr.ID,
			"title":  pr.Title,
			"reason": args["reason"],
		})

	case "conflicts":
		conflictFilesJSON, _ := json.Marshal(args["conflict_files"])
		pr.ConflictFiles = string(conflictFilesJSON)
		pr.Status = "evaluated"
		model.DB.Save(&pr)
		BroadcastEvent(projectID, "PR_HAS_CONFLICTS", map[string]interface{}{
			"pr_id":          pr.ID,
			"title":          pr.Title,
			"conflict_files": args["conflict_files"],
		})

	case "high_risk":
		pr.Status = "evaluated"
		model.DB.Save(&pr)
		BroadcastEvent(projectID, "PR_HIGH_RISK", map[string]interface{}{
			"pr_id":  pr.ID,
			"title":  pr.Title,
			"reason": args["reason"],
		})

	default:
		pr.Status = "evaluated"
		model.DB.Save(&pr)
	}

	log.Printf("[PR] Evaluate output for PR %s: result=%s, merge_cost=%s", pr.ID, result, mergeCostRating)
	return nil
}

// HandleMergeOutput processes the merge agent's output
func HandleMergeOutput(sessionID, projectID string, args map[string]interface{}) error {
	session := agent.DefaultManager.GetSession(sessionID)
	if session == nil || session.PRID == "" {
		return fmt.Errorf("no PR associated with session %s", sessionID)
	}

	var pr model.PullRequest
	if model.DB.Where("id = ?", session.PRID).First(&pr).Error != nil {
		return fmt.Errorf("PR %s not found", session.PRID)
	}

	result, _ := args["result"].(string) // success / failed
	result = strings.ToLower(result)

	switch result {
	case "success":
		// Merge succeeded - this should already be handled by ExecuteMerge
		// but we update PR status here as well
		now := time.Now()
		pr.Status = "merged"
		pr.MergedAt = &now
		model.DB.Save(&pr)

		BroadcastEvent(projectID, "PR_MERGED", map[string]interface{}{
			"pr_id": pr.ID,
			"title": pr.Title,
		})

	case "failed":
		pr.Status = "merge_failed"
		model.DB.Save(&pr)

		BroadcastEvent(projectID, "PR_MERGE_FAILED", map[string]interface{}{
			"pr_id":  pr.ID,
			"title":  pr.Title,
			"reason": args["reason"],
		})
	}

	log.Printf("[PR] Merge output for PR %s: result=%s", pr.ID, result)
	return nil
}
