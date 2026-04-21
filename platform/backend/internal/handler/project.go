package handler

import (
	"log"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/repo"
	"github.com/a3c/platform/internal/service"
)

type ProjectHandler struct{}

func NewProjectHandler() *ProjectHandler {
	return &ProjectHandler{}
}

type CreateProjectRequest struct {
	Name           string `json:"name" binding:"required"`
	Description    string `json:"description"`
	GithubRepo     string `json:"github_repo"`
	ImportExisting bool   `json:"import_existing"`
}

func (h *ProjectHandler) Create(c *gin.Context) {
	var req CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	project := model.Project{
		ID:          model.GenerateID("proj"),
		Name:        req.Name,
		Description: req.Description,
		GithubRepo:  req.GithubRepo,
		Status:      "initializing",
	}

	if err := model.DB.Create(&project).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to create project"}})
		return
	}

	directionCB := model.ContentBlock{
		ID:        model.GenerateID("cb"),
		ProjectID: project.ID,
		BlockType: "direction",
		Content:   "",
		Version:   1,
	}
	model.DB.Create(&directionCB)

	versionCB := model.ContentBlock{
		ID:        model.GenerateID("cb"),
		ProjectID: project.ID,
		BlockType: "version",
		Content:   "v1.0",
		Version:   1,
	}
	model.DB.Create(&versionCB)

	milestone := model.Milestone{
		ID:        model.GenerateID("ms"),
		ProjectID: project.ID,
		Name:      "Milestone 1",
		Status:    "in_progress",
		CreatedBy: "system",
	}
	model.DB.Create(&milestone)

	repoPath := filepath.Join("data", "projects", project.ID, "repo")
	os.MkdirAll(repoPath, 0755)

	if req.ImportExisting && req.GithubRepo != "" {
		go func() {
			projectPath := filepath.Join("data", "projects", project.ID, "repo")
			log.Printf("[Project] Starting assessment for imported project %s", project.ID)
			_, err := service.TriggerAssessAgent(project.ID, projectPath)
			if err != nil {
				log.Printf("[Project] Failed to trigger assess agent: %v", err)
			}
		}()
	} else {
		project.Status = "ready"
		model.DB.Save(&project)
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":          project.ID,
			"name":        project.Name,
			"status":      project.Status,
			"milestone_id": milestone.ID,
		},
	})
}

func (h *ProjectHandler) Get(c *gin.Context) {
	id := c.Param("id")
	project, err := repo.GetProjectByID(id)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Internal error"}})
		return
	}
	if project == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PROJECT_NOT_FOUND", "message": "Project not found"}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": project})
}

func (h *ProjectHandler) List(c *gin.Context) {
	var projects []model.Project
	model.DB.Find(&projects)
	c.JSON(200, gin.H{"success": true, "data": projects})
}

func (h *ProjectHandler) SetAutoMode(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}

	var req struct {
		AutoMode bool `json:"auto_mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	var project model.Project
	if err := model.DB.Where("id = ?", projectID).First(&project).Error; err != nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PROJECT_NOT_FOUND", "message": "Project not found"}})
		return
	}

	project.AutoMode = req.AutoMode
	model.DB.Save(&project)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"project_id": projectID,
			"auto_mode":  project.AutoMode,
		},
	})
}

func (h *ProjectHandler) ImportAssess(c *gin.Context) {
	projectID := c.Param("id")
	project, err := repo.GetProjectByID(projectID)
	if err != nil || project == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "PROJECT_NOT_FOUND", "message": "Project not found"}})
		return
	}

	projectPath := filepath.Join("data", "projects", project.ID, "repo")
	session, err := service.TriggerAssessAgent(project.ID, projectPath)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Failed to start assessment"}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"assess_id": session.ID,
			"status":    "running",
		},
	})
}