package handler

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

type FileSyncHandler struct{}

func NewFileSyncHandler() *FileSyncHandler {
	return &FileSyncHandler{}
}

type FileSyncRequest struct {
	Version string `json:"version"`
}

func loadGitignore(repoPath string) map[string]bool {
	ignorePatterns := map[string]bool{
		".git":         true,
		".a3c":         true,
		".a3c_staging": true,
		"node_modules": true,
		".env":         true,
		".DS_Store":    true,
		"Thumbs.db":    true,
	}

	gitignorePath := filepath.Join(repoPath, ".gitignore")
	file, err := os.Open(gitignorePath)
	if err != nil {
		return ignorePatterns
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSuffix(line, "/")
		ignorePatterns[line] = true
	}

	return ignorePatterns
}

func shouldIgnore(relPath string, ignorePatterns map[string]bool) bool {
	parts := strings.Split(relPath, "/")
	for _, part := range parts {
		if ignorePatterns[part] {
			return true
		}
	}
	if ignorePatterns[relPath] {
		return true
	}
	for pattern := range ignorePatterns {
		if strings.HasSuffix(pattern, "/*") {
			prefix := strings.TrimSuffix(pattern, "/*")
			if strings.HasPrefix(relPath, prefix+"/") {
				return true
			}
		}
		if strings.Contains(pattern, "*") {
			matched, _ := filepath.Match(pattern, filepath.Base(relPath))
			if matched {
				return true
			}
		}
	}
	return false
}

func (h *FileSyncHandler) Sync(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	agent, err := repo.GetAgentByID(agentID.(string))
	if err != nil || agent == nil || agent.CurrentProjectID == nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "No project selected"}})
		return
	}

	// Branch auto-routing: if agent is on a branch, return branch files transparently
	if agent.CurrentBranchID != nil {
		h.syncBranch(c, *agent.CurrentBranchID)
		return
	}

	projectID := *agent.CurrentProjectID
	project, _ := repo.GetProjectByID(projectID)
	if project == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PROJECT_NOT_FOUND", "message": "Project not found"}})
		return
	}

	versionBlock, _ := repo.GetContentBlock(projectID, "version")
	currentVersion := "v1.0"
	if versionBlock != nil {
		currentVersion = versionBlock.Content
	}

	var allLocks []model.FileLock
	model.DB.Where("project_id = ? AND released_at IS NULL AND expires_at > ?", projectID, time.Now()).Find(&allLocks)
	lockedFiles := map[string]bool{}
	for _, l := range allLocks {
		var files []string
		json.Unmarshal([]byte(l.Files), &files)
		for _, f := range files {
			lockedFiles[f] = true
		}
	}

	type FileInfo struct {
		Path    string `json:"path"`
		Content string `json:"content,omitempty"`
		Locked  bool   `json:"locked"`
	}

	var allFiles []FileInfo

	repoPath := filepath.Join("data", "projects", projectID, "repo")
	ignorePatterns := loadGitignore(repoPath)

	if _, err := os.Stat(repoPath); err == nil {
		filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			relPath, _ := filepath.Rel(repoPath, path)
			relPath = strings.ReplaceAll(relPath, "\\", "/")

			if shouldIgnore(relPath, ignorePatterns) {
				return nil
			}

			srcData, _ := os.ReadFile(path)
			allFiles = append(allFiles, FileInfo{
				Path:    relPath,
				Content: string(srcData),
				Locked:  lockedFiles[relPath],
			})
			return nil
		})
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"version":      currentVersion,
			"project_id":   projectID,
			"project_name": project.Name,
			"file_count":   len(allFiles),
			"files":        allFiles,
			"message":      "All project files synced. Write to .a3c_staging/{project_id}/full/ in your working directory.",
		},
	})
}

// syncBranch handles file_sync when the agent is on a branch.
// It returns branch worktree files transparently, no locks needed (single-occupant).
func (h *FileSyncHandler) syncBranch(c *gin.Context, branchID string) {
	files, err := service.GetBranchFiles(branchID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"branch_id":  branchID,
			"file_count": len(files),
			"files":      files,
			"message":    "Branch files synced. Write to .a3c_staging/{project_id}/branch/{branch_id}/ in your working directory.",
		},
	})
}