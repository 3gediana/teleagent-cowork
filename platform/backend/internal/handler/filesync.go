package handler

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
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
		Status  string `json:"status,omitempty"` // "added" / "modified" — only populated in incremental mode
	}

	var req FileSyncRequest
	_ = c.ShouldBindJSON(&req) // body optional

	repoPath := service.GetProjectRepoPath(projectID)
	ignorePatterns := loadGitignore(repoPath)

	// Incremental path: if client sent their last-synced version and it maps to
	// a git tag, return only the delta via `git diff --name-status`.
	if req.Version != "" && req.Version != currentVersion {
		if changed, deleted, ok := diffFilesSinceVersion(repoPath, req.Version); ok {
			files := make([]FileInfo, 0, len(changed))
			for _, ch := range changed {
				if shouldIgnore(ch.path, ignorePatterns) {
					continue
				}
				data, _ := os.ReadFile(filepath.Join(repoPath, ch.path))

				files = append(files, FileInfo{
					Path:    ch.path,
					Content: string(data),
					Locked:  lockedFiles[ch.path],
					Status:  ch.status,
				})
			}
			filteredDeleted := make([]string, 0, len(deleted))
			for _, d := range deleted {
				if shouldIgnore(d, ignorePatterns) {
					continue
				}
				filteredDeleted = append(filteredDeleted, d)
			}
			c.JSON(200, gin.H{
				"success": true,
				"data": gin.H{
					"version":      currentVersion,
					"from_version": req.Version,
					"incremental":  true,
					"project_id":   projectID,
					"project_name": project.Name,
					"file_count":   len(files),
					"files":        files,
					"deleted":      filteredDeleted,
					"message":      "Incremental sync: apply writes and delete listed files.",
				},
			})
			return
		}
		// fall through to full sync if the tag doesn't exist or git diff fails
	}

	// Full sync (fallback / first sync)
	allFiles := make([]FileInfo, 0)
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
			"incremental":  false,
			"project_id":   projectID,
			"project_name": project.Name,
			"file_count":   len(allFiles),
			"files":        allFiles,
			"deleted":      []string{},
			"message":      "Full project files synced. Write to .a3c_staging/{project_id}/full/ in your working directory.",
		},
	})
}

// changedFile represents one entry from `git diff --name-status`.
type changedFile struct {
	status string // "added" or "modified"
	path   string
}

// diffFilesSinceVersion uses git to compute the set of files changed between
// an older version tag and the current HEAD. Returns (changed, deleted, ok).
// ok=false means caller should fall back to a full snapshot (e.g. unknown tag).
func diffFilesSinceVersion(repoPath, fromVersion string) ([]changedFile, []string, bool) {
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return nil, nil, false
	}
	// Verify the tag exists; otherwise fall back.
	if err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "refs/tags/"+fromVersion).Run(); err != nil {
		return nil, nil, false
	}
	out, err := exec.Command("git", "-C", repoPath, "diff", "--name-status", fromVersion+"..HEAD").Output()
	if err != nil {
		return nil, nil, false
	}
	var changed []changedFile
	var deleted []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "<STATUS>\t<path>" or "R<score>\t<old>\t<new>"
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		code := parts[0]
		switch {
		case strings.HasPrefix(code, "A"):
			changed = append(changed, changedFile{status: "added", path: strings.ReplaceAll(parts[1], "\\", "/")})
		case strings.HasPrefix(code, "M"):
			changed = append(changed, changedFile{status: "modified", path: strings.ReplaceAll(parts[1], "\\", "/")})
		case strings.HasPrefix(code, "D"):
			deleted = append(deleted, strings.ReplaceAll(parts[1], "\\", "/"))
		case strings.HasPrefix(code, "R") && len(parts) >= 3:
			// Rename: treat as delete(old) + add(new)
			deleted = append(deleted, strings.ReplaceAll(parts[1], "\\", "/"))
			changed = append(changed, changedFile{status: "added", path: strings.ReplaceAll(parts[2], "\\", "/")})
		}
	}
	return changed, deleted, true
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