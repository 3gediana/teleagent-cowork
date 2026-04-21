package service

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
)

// getBranchRepoPath returns the git worktree path for a branch
func getBranchRepoPath(projectID, branchID string) string {
	return filepath.Join(DataPath, projectID, "branches", branchID, "repo")
}

// getMainBranchName detects the main branch name (main or master)
func getMainBranchName(repoPath string) string {
	// Try "main" first
	out, err := runGitInDir(repoPath, "rev-parse", "--verify", "main")
	if err == nil && strings.TrimSpace(out) != "" {
		return "main"
	}
	// Fall back to "master"
	out, err = runGitInDir(repoPath, "rev-parse", "--verify", "master")
	if err == nil && strings.TrimSpace(out) != "" {
		return "master"
	}
	// Default to HEAD (current branch)
	return "HEAD"
}

// runGitInDir runs a git command in the specified directory
func runGitInDir(dir string, args ...string) (string, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", fmt.Errorf("directory not found: %s", dir)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %v failed: %w, output: %s", args, err, string(out))
	}
	return string(out), nil
}

// CreateBranch creates a new feature branch with a git worktree
func CreateBranch(projectID, branchName, creatorID string) (*model.Branch, error) {
	// Check active branch limit
	var activeCount int64
	model.DB.Model(&model.Branch{}).Where("project_id = ? AND status = 'active'", projectID).Count(&activeCount)
	if activeCount >= 3 {
		return nil, fmt.Errorf("project already has %d active branches (max 3)", activeCount)
	}

	// Get current main version
	versionBlock, _ := repo.GetContentBlock(projectID, "version")
	currentVersion := "v1.0"
	if versionBlock != nil {
		currentVersion = versionBlock.Content
	}

	// Get current main HEAD commit
	mainRepoPath := getRepoPath(projectID)
	headOutput, err := runGitInDir(mainRepoPath, "rev-parse", "--short", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to get main HEAD: %w", err)
	}
	baseCommit := strings.TrimSpace(headOutput)

	// Create branch record
	branchID := model.GenerateID("br")
	branch := &model.Branch{
		ID:          branchID,
		ProjectID:   projectID,
		Name:        branchName,
		BaseCommit:  baseCommit,
		BaseVersion: currentVersion,
		Status:      "active",
		CreatorID:   creatorID,
	}
	if err := model.DB.Create(branch).Error; err != nil {
		return nil, fmt.Errorf("failed to create branch record: %w", err)
	}

	// Create git worktree for the branch
	branchRepoPath := getBranchRepoPath(projectID, branchID)
	os.MkdirAll(filepath.Dir(branchRepoPath), 0755)

	// git worktree add <path> -b <branch-name> HEAD
	_, err = runGitInDir(mainRepoPath, "worktree", "add", branchRepoPath, "-b", branchName, "HEAD")
	if err != nil {
		// Cleanup: remove branch record if worktree fails
		model.DB.Delete(branch)
		return nil, fmt.Errorf("failed to create worktree: %w", err)
	}

	// Configure git user in worktree
	runGitInDir(branchRepoPath, "config", "user.email", "a3c@platform.local")
	runGitInDir(branchRepoPath, "config", "user.name", "A3C Platform")

	log.Printf("[Branch] Created branch %s (%s) for project %s, worktree at %s", branchName, branchID, projectID, branchRepoPath)
	return branch, nil
}

// EnterBranch assigns an agent to a branch (marks as occupied)
func EnterBranch(branchID, agentID string) error {
	var branch model.Branch
	if err := model.DB.Where("id = ?", branchID).First(&branch).Error; err != nil {
		return fmt.Errorf("branch not found")
	}
	if branch.Status != "active" {
		return fmt.Errorf("branch is %s, not active", branch.Status)
	}
	if branch.OccupantID != nil {
		// Check if occupant is still online
		var occupant model.Agent
		if model.DB.Where("id = ?", *branch.OccupantID).First(&occupant).Error == nil {
			if occupant.Status == "online" {
				return fmt.Errorf("branch is occupied by %s", occupant.Name)
			}
		}
		// Occupant is offline, allow takeover
		log.Printf("[Branch] Taking over branch %s from offline agent %s", branchID, *branch.OccupantID)
	}

	now := time.Now()
	branch.OccupantID = &agentID
	branch.LastActiveAt = &now
	model.DB.Save(&branch)

	// Update agent's current branch
	model.DB.Model(&model.Agent{}).Where("id = ?", agentID).Update("current_branch_id", branchID)

	log.Printf("[Branch] Agent %s entered branch %s (%s)", agentID, branch.Name, branchID)
	return nil
}

// LeaveBranch removes an agent from a branch
func LeaveBranch(agentID string) error {
	var agent model.Agent
	if err := model.DB.Where("id = ?", agentID).First(&agent).Error; err != nil {
		return fmt.Errorf("agent not found")
	}
	if agent.CurrentBranchID == nil {
		return fmt.Errorf("agent is not on any branch")
	}

	branchID := *agent.CurrentBranchID

	// Clear branch occupant
	model.DB.Model(&model.Branch{}).Where("id = ?", branchID).Update("occupant_id", nil)
	// Clear agent's current branch
	model.DB.Model(&model.Agent{}).Where("id = ?", agentID).Update("current_branch_id", nil)

	log.Printf("[Branch] Agent %s left branch %s", agentID, branchID)
	return nil
}

// ListBranches returns all branches for a project
func ListBranches(projectID string) ([]model.Branch, error) {
	var branches []model.Branch
	if err := model.DB.Where("project_id = ?", projectID).Order("created_at DESC").Find(&branches).Error; err != nil {
		return nil, err
	}
	return branches, nil
}

// CloseBranch closes a branch and removes its worktree
func CloseBranch(branchID string) error {
	var branch model.Branch
	if err := model.DB.Where("id = ?", branchID).First(&branch).Error; err != nil {
		return fmt.Errorf("branch not found")
	}
	if branch.Status != "active" {
		return fmt.Errorf("branch is already %s", branch.Status)
	}

	// Release all file locks on this branch
	model.DB.Model(&model.FileLock{}).Where("branch_id = ? AND released_at IS NULL", branchID).
		Update("released_at", time.Now())

	// If someone is on this branch, kick them out
	if branch.OccupantID != nil {
		model.DB.Model(&model.Agent{}).Where("id = ?", *branch.OccupantID).Update("current_branch_id", nil)
	}

	// Remove git worktree
	mainRepoPath := getRepoPath(branch.ProjectID)
	branchRepoPath := getBranchRepoPath(branch.ProjectID, branchID)
	if _, err := os.Stat(branchRepoPath); err == nil {
		runGitInDir(mainRepoPath, "worktree", "remove", branchRepoPath, "--force")
		runGitInDir(mainRepoPath, "branch", "-D", branch.Name)
	}

	now := time.Now()
	branch.Status = "closed"
	branch.ClosedAt = &now
	branch.OccupantID = nil
	model.DB.Save(&branch)

	log.Printf("[Branch] Closed branch %s (%s)", branch.Name, branchID)
	return nil
}

// SyncMain merges main into the branch to keep it up-to-date
func SyncMain(branchID string) ([]string, error) {
	var branch model.Branch
	if err := model.DB.Where("id = ?", branchID).First(&branch).Error; err != nil {
		return nil, fmt.Errorf("branch not found")
	}

	branchRepoPath := getBranchRepoPath(branch.ProjectID, branchID)
	if _, err := os.Stat(branchRepoPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("branch worktree not found")
	}

	// git merge main/master in the branch worktree
	mainBranch := getMainBranchName(getRepoPath(branch.ProjectID))
	out, err := runGitInDir(branchRepoPath, "merge", mainBranch)
	if err != nil {
		// Check for conflicts
		if strings.Contains(out, "CONFLICT") {
			conflictFiles := parseConflictFiles(out)
			// Abort the merge to leave branch in clean state
			runGitInDir(branchRepoPath, "merge", "--abort")
			return conflictFiles, fmt.Errorf("merge conflicts detected")
		}
		return nil, fmt.Errorf("merge failed: %w", err)
	}

	// Update base version
	versionBlock, _ := repo.GetContentBlock(branch.ProjectID, "version")
	if versionBlock != nil {
		branch.BaseVersion = versionBlock.Content
		model.DB.Save(&branch)
	}

	now := time.Now()
	branch.LastActiveAt = &now
	model.DB.Save(&branch)

	log.Printf("[Branch] Synced main into branch %s (%s)", branch.Name, branchID)
	return nil, nil
}

// BranchCommit commits changes in a branch worktree
func BranchCommit(branchID, description string) error {
	branchRepoPath := getRepoPathForBranch(branchID)
	if branchRepoPath == "" {
		return fmt.Errorf("branch worktree not found")
	}

	if _, err := runGitInDir(branchRepoPath, "add", "-A"); err != nil {
		return err
	}

	commitMsg := fmt.Sprintf("[branch] %s", description)
	out, err := runGitInDir(branchRepoPath, "commit", "-m", commitMsg)
	if err != nil {
		if strings.Contains(out, "nothing to commit") {
			return nil
		}
		return err
	}

	// Update last active time
	now := time.Now()
	model.DB.Model(&model.Branch{}).Where("id = ?", branchID).Update("last_active_at", now)

	log.Printf("[Branch] Committed to branch %s: %s", branchID, description)
	return nil
}

// getRepoPathForBranch looks up the branch and returns its worktree path
func getRepoPathForBranch(branchID string) string {
	var branch model.Branch
	if model.DB.Where("id = ?", branchID).First(&branch).Error != nil {
		return ""
	}
	path := getBranchRepoPath(branch.ProjectID, branchID)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return ""
	}
	return path
}

// GenerateBranchDiff generates the diff between main and a branch
func GenerateBranchDiff(branchID string) (stat string, full string, err error) {
	var branch model.Branch
	if dbErr := model.DB.Where("id = ?", branchID).First(&branch).Error; dbErr != nil {
		return "", "", fmt.Errorf("branch not found")
	}

	mainRepoPath := getRepoPath(branch.ProjectID)
	mainBranch := getMainBranchName(mainRepoPath)
	stat, err = runGitInDir(mainRepoPath, "diff", mainBranch+"..."+branch.Name, "--stat")
	if err != nil {
		return "", "", fmt.Errorf("failed to generate diff stat: %w", err)
	}

	full, err = runGitInDir(mainRepoPath, "diff", mainBranch+"..."+branch.Name)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate full diff: %w", err)
	}

	return stat, full, nil
}

// DryRunMerge attempts a dry-run merge to check for conflicts
func DryRunMerge(branchID string) (canMerge bool, conflictFiles []string, err error) {
	var branch model.Branch
	if dbErr := model.DB.Where("id = ?", branchID).First(&branch).Error; dbErr != nil {
		return false, nil, fmt.Errorf("branch not found")
	}

	mainRepoPath := getRepoPath(branch.ProjectID)

	// Try merge with --no-commit
	out, mergeErr := runGitInDir(mainRepoPath, "merge", "--no-commit", "--no-ff", branch.Name)
	if mergeErr != nil {
		if strings.Contains(out, "CONFLICT") {
			conflictFiles = parseConflictFiles(out)
			// Abort to restore clean state
			runGitInDir(mainRepoPath, "merge", "--abort")
			return false, conflictFiles, nil
		}
		// Other error (e.g. branch not found in repo)
		runGitInDir(mainRepoPath, "merge", "--abort")
		return false, nil, fmt.Errorf("merge check failed: %w", mergeErr)
	}

	// No conflicts - abort the test merge (we'll do the real one later)
	runGitInDir(mainRepoPath, "merge", "--abort")
	return true, nil, nil
}

// ExecuteMerge performs the actual merge of a branch into main
func ExecuteMerge(branchID string) error {
	var branch model.Branch
	if dbErr := model.DB.Where("id = ?", branchID).First(&branch).Error; dbErr != nil {
		return fmt.Errorf("branch not found")
	}

	mainRepoPath := getRepoPath(branch.ProjectID)

	// Try merge with --no-commit first (safety)
	out, err := runGitInDir(mainRepoPath, "merge", "--no-commit", "--no-ff", branch.Name)
	if err != nil {
		if strings.Contains(out, "CONFLICT") {
			runGitInDir(mainRepoPath, "merge", "--abort")
			return fmt.Errorf("merge conflicts detected, aborting")
		}
		runGitInDir(mainRepoPath, "merge", "--abort")
		return fmt.Errorf("merge failed: %w", err)
	}

	// Simple auto-resolvable conflicts: check if there are unmerged paths
	statusOut, _ := runGitInDir(mainRepoPath, "status", "--porcelain")
	hasUnmerged := false
	for _, line := range strings.Split(statusOut, "\n") {
		if strings.HasPrefix(line, "UU") || strings.HasPrefix(line, "AA") || strings.HasPrefix(line, "DU") {
			hasUnmerged = true
			break
		}
	}

	if hasUnmerged {
		// Complex conflicts - abort
		runGitInDir(mainRepoPath, "merge", "--abort")
		return fmt.Errorf("complex merge conflicts, requires human resolution")
	}

	// No unmerged paths or only auto-resolved - commit the merge
	commitMsg := fmt.Sprintf("Merge branch '%s'", branch.Name)
	if _, err := runGitInDir(mainRepoPath, "commit", "-m", commitMsg); err != nil {
		runGitInDir(mainRepoPath, "merge", "--abort")
		return fmt.Errorf("merge commit failed: %w", err)
	}

	// Update branch status
	now := time.Now()
	branch.Status = "merged"
	branch.MergedAt = &now
	branch.OccupantID = nil
	model.DB.Save(&branch)

	// Remove worktree
	branchRepoPath := getBranchRepoPath(branch.ProjectID, branchID)
	if _, err := os.Stat(branchRepoPath); err == nil {
		runGitInDir(mainRepoPath, "worktree", "remove", branchRepoPath, "--force")
	}

	// Kick agent off branch
	if branch.OccupantID != nil {
		model.DB.Model(&model.Agent{}).Where("current_branch_id = ?", branchID).Update("current_branch_id", nil)
	}

	// Release branch file locks
	model.DB.Model(&model.FileLock{}).Where("branch_id = ? AND released_at IS NULL", branchID).
		Update("released_at", now)

	log.Printf("[Branch] Merged branch %s (%s) into %s", branch.Name, branchID, getMainBranchName(getRepoPath(branch.ProjectID)))
	return nil
}

// parseConflictFiles extracts conflicting file paths from git merge output
func parseConflictFiles(output string) []string {
	var files []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CONFLICT") {
			// Format: CONFLICT (content): Merge conflict in path/to/file
			parts := strings.SplitN(line, "in ", 2)
			if len(parts) == 2 {
				files = append(files, strings.TrimSpace(parts[1]))
			}
		}
	}
	return files
}

// GetBranchFiles returns all files in a branch worktree
func GetBranchFiles(branchID string) ([]map[string]interface{}, error) {
	branchRepoPath := getRepoPathForBranch(branchID)
	if branchRepoPath == "" {
		return nil, fmt.Errorf("branch worktree not found")
	}

	var allFiles []map[string]interface{}
	filepath.Walk(branchRepoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		relPath, _ := filepath.Rel(branchRepoPath, path)
		relPath = strings.ReplaceAll(relPath, "\\", "/")
		if strings.HasPrefix(relPath, ".git") {
			return nil
		}
		srcData, _ := os.ReadFile(path)
		allFiles = append(allFiles, map[string]interface{}{
			"path":    relPath,
			"content": string(srcData),
		})
		return nil
	})
	return allFiles, nil
}

// WriteBranchFiles writes files to a branch worktree
func WriteBranchFiles(branchID string, writes []model.ChangeFileEntry, deletes []string) error {
	branchRepoPath := getRepoPathForBranch(branchID)
	if branchRepoPath == "" {
		return fmt.Errorf("branch worktree not found")
	}

	// Write files
	for _, w := range writes {
		fullPath := filepath.Join(branchRepoPath, w.Path)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		if err := os.WriteFile(fullPath, []byte(w.Content), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", w.Path, err)
		}
	}

	// Delete files
	for _, d := range deletes {
		fullPath := filepath.Join(branchRepoPath, d)
		os.Remove(fullPath)
	}

	return nil
}

// GetBranchInfo returns branch info with occupant name
func GetBranchInfo(branchID string) (map[string]interface{}, error) {
	var branch model.Branch
	if err := model.DB.Where("id = ?", branchID).First(&branch).Error; err != nil {
		return nil, fmt.Errorf("branch not found")
	}

	result := map[string]interface{}{
		"id":           branch.ID,
		"name":         branch.Name,
		"status":       branch.Status,
		"base_version": branch.BaseVersion,
		"creator_id":   branch.CreatorID,
		"created_at":   branch.CreatedAt,
	}

	if branch.OccupantID != nil {
		var agent model.Agent
		if model.DB.Where("id = ?", *branch.OccupantID).First(&agent).Error == nil {
			result["occupied_by"] = agent.Name
			result["occupant_id"] = agent.ID
		}
	} else {
		result["occupied_by"] = nil
		result["occupant_id"] = nil
	}

	return result, nil
}

// ListBranchesWithOccupants returns branches with occupant info for a project
func ListBranchesWithOccupants(projectID string) ([]map[string]interface{}, error) {
	branches, err := ListBranches(projectID)
	if err != nil {
		return nil, err
	}

	result := make([]map[string]interface{}, 0, len(branches))
	for _, b := range branches {
		info := map[string]interface{}{
			"id":           b.ID,
			"name":         b.Name,
			"status":       b.Status,
			"base_version": b.BaseVersion,
			"created_at":   b.CreatedAt,
		}

		if b.OccupantID != nil {
			var agent model.Agent
			if model.DB.Where("id = ?", *b.OccupantID).First(&agent).Error == nil {
				info["occupied_by"] = agent.Name
			}
		} else {
			info["occupied_by"] = nil
		}

		result = append(result, info)
	}
	return result, nil
}

// GetBranchFileLocks returns active locks for a branch
func GetBranchFileLocks(branchID string) ([]model.FileLock, error) {
	var locks []model.FileLock
	err := model.DB.Where("branch_id = ? AND released_at IS NULL AND expires_at > ?", branchID, time.Now()).
		Find(&locks).Error
	return locks, err
}

// AcquireBranchLock acquires a file lock scoped to a branch
func AcquireBranchLock(branchID, agentID, taskID, reason string, files []string) (*model.FileLock, error) {
	// Check for conflicts within the same branch
	var existing []model.FileLock
	model.DB.Where("branch_id = ? AND released_at IS NULL AND expires_at > ?", branchID, time.Now()).
		Find(&existing)

	existingFiles := map[string]string{}
	for _, l := range existing {
		var lFiles []string
		json.Unmarshal([]byte(l.Files), &lFiles)
		for _, f := range lFiles {
			existingFiles[f] = l.AgentID
		}
	}

	var conflictFiles []map[string]interface{}
	for _, f := range files {
		if owner, ok := existingFiles[f]; ok && owner != agentID {
			conflictFiles = append(conflictFiles, map[string]interface{}{
				"file":       f,
				"locked_by":  owner,
			})
		}
	}

	if len(conflictFiles) > 0 {
		return nil, fmt.Errorf("file lock conflict in branch")
	}

	filesJSON, _ := json.Marshal(files)
	lock := &model.FileLock{
		ID:        model.GenerateID("lk"),
		ProjectID: "", // Will be filled from branch
		BranchID:  &branchID,
		TaskID:    taskID,
		AgentID:   agentID,
		Files:     string(filesJSON),
		Reason:    reason,
		AcquiredAt: time.Now(),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	// Get project ID from branch
	var branch model.Branch
	if model.DB.Where("id = ?", branchID).First(&branch).Error == nil {
		lock.ProjectID = branch.ProjectID
	}

	if err := model.DB.Create(lock).Error; err != nil {
		return nil, err
	}
	return lock, nil
}
