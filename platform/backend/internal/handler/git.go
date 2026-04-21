package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/service"
)

type GitHandler struct{}

func NewGitHandler() *GitHandler {
	return &GitHandler{}
}

func (h *GitHandler) Diff(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	var req struct {
		FromVersion string `json:"from_version" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	diff, err := service.GitDiff(projectID, req.FromVersion)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"diff": diff}})
}

func (h *GitHandler) Commit(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	var req struct {
		TaskName string `json:"task_name" binding:"required"`
		TaskDesc string `json:"task_desc"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if err := service.GitAddAndCommit(projectID, req.TaskName, req.TaskDesc); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"committed": true}})
}

func (h *GitHandler) Revert(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	var req struct {
		Version string `json:"version" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	err := service.GitRevertToVersion(projectID, req.Version)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"version": req.Version}})
}

func (h *GitHandler) Push(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	var req struct {
		Remote string `json:"remote"`
		Branch string `json:"branch"`
	}
	c.ShouldBindJSON(&req)

	if err := service.GitPush(projectID, req.Remote, req.Branch); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "GIT_PUSH_FAILED", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"pushed": true}})
}

func (h *GitHandler) AddRemote(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	var req struct {
		Name string `json:"name" binding:"required"`
		URL  string `json:"url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if err := service.GitAddRemote(projectID, req.Name, req.URL); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "GIT_ADD_REMOTE_FAILED", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"remote": req.Name, "url": req.URL}})
}
