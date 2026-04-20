package handler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
)

type FileSyncHandler struct{}

func NewFileSyncHandler() *FileSyncHandler {
	return &FileSyncHandler{}
}

type FileSyncRequest struct {
	Version string `json:"version"`
}

func (h *FileSyncHandler) Sync(c *gin.Context) {
	agentID, _ := c.Get("agent_id")
	agent, err := repo.GetAgentByID(agentID.(string))
	if err != nil || agent == nil || agent.CurrentProjectID == nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "No project selected"}})
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

	stagingBase := filepath.Join("data", "projects", projectID, "staging")
	fullStaging := filepath.Join(stagingBase, "full")
	os.MkdirAll(fullStaging, 0755)

	type FileInfo struct {
		Path    string `json:"path"`
		Content string `json:"content,omitempty"`
	}

	noChange := []string{}
	unlockedModify := []FileInfo{}
	lockedModify := []FileInfo{}

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

	repoPath := filepath.Join("data", "projects", projectID, "repo")
	if _, err := os.Stat(repoPath); err == nil {
		filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			relPath, _ := filepath.Rel(repoPath, path)
			relPath = strings.ReplaceAll(relPath, "\\", "/")
			if strings.HasPrefix(relPath, ".git") || strings.HasPrefix(relPath, ".a3c") {
				return nil
			}

			stagingPath := filepath.Join(fullStaging, relPath)
			os.MkdirAll(filepath.Dir(stagingPath), 0755)
			srcData, _ := os.ReadFile(path)
			stagingData, stagingErr := os.ReadFile(stagingPath)

			if stagingErr != nil || string(srcData) != string(stagingData) {
				fileInfo := FileInfo{Path: relPath, Content: string(srcData)}
				if lockedFiles[relPath] {
					lockedModify = append(lockedModify, fileInfo)
				} else {
					unlockedModify = append(unlockedModify, fileInfo)
				}
				os.WriteFile(stagingPath, srcData, 0644)
			} else {
				noChange = append(noChange, relPath)
			}
			return nil
		})
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"version":      currentVersion,
			"staging_path": ".a3c_staging/full/",
			"files": gin.H{
				"no_change":        noChange,
				"unlocked_modify":  unlockedModify,
				"locked_modify":    lockedModify,
			},
			"message": "Files downloaded to staging area. AI decides whether to apply.",
		},
	})
}