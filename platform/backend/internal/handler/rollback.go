package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

type RollbackHandler struct{}

func NewRollbackHandler() *RollbackHandler {
	return &RollbackHandler{}
}

type RollbackRequest struct {
	Version string `json:"version" binding:"required"`
	Reason  string `json:"reason"`
}

func (h *RollbackHandler) Rollback(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	var req RollbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	project, _ := repo.GetProjectByID(projectID)
	if project == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PROJECT_NOT_FOUND", "message": "Project not found"}})
		return
	}

	versionBlock, _ := repo.GetContentBlock(projectID, "version")
	targetVersion := req.Version
	if !strings.HasPrefix(targetVersion, "v") {
		targetVersion = "v" + targetVersion
	}

	prevVersion := "v1.0"
	if versionBlock != nil {
		prevVersion = versionBlock.Content
	}

	if err := service.GitRevertToVersion(projectID, targetVersion); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Rollback failed: " + err.Error()}})
		return
	}

	if versionBlock != nil {
		versionBlock.Content = targetVersion
		versionBlock.Version++
		model.DB.Save(versionBlock)
	}

	service.BroadcastEvent(projectID, "VERSION_ROLLBACK", gin.H{
		"block_type":    "version",
		"content":       targetVersion,
		"prev_version":  prevVersion,
		"reason":        req.Reason,
	})

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"version":       targetVersion,
			"prev_version":  prevVersion,
		},
	})
}

func (h *RollbackHandler) ListVersions(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	versions, err := service.GitListVersions(projectID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"versions": versions}})
}