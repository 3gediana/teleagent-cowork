package handler

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/service"
)

type AgentHandler struct{}

func NewAgentHandler() *AgentHandler {
	return &AgentHandler{}
}

type AuditOutputRequest struct {
	ChangeID     string        `json:"change_id"`
	Level        string        `json:"level"`
	Issues       []AuditIssue  `json:"issues,omitempty"`
	RejectReason string        `json:"reject_reason,omitempty"`
}

type AuditIssue struct {
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
	Type   string `json:"type"`
	Detail string `json:"detail"`
	Status string `json:"status"`
}

func (h *AgentHandler) AuditOutput(c *gin.Context) {
	var req AuditOutputRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if req.Level != "L0" && req.Level != "L1" && req.Level != "L2" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "level must be L0, L1, or L2"}})
		return
	}

	result := &service.AuditResult{
		Level:        req.Level,
		Issues:       make([]service.AuditIssue, 0, len(req.Issues)),
		RejectReason: req.RejectReason,
	}
	for _, issue := range req.Issues {
		result.Issues = append(result.Issues, service.AuditIssue{
			File:   issue.File,
			Line:   issue.Line,
			Type:   issue.Type,
			Detail: issue.Detail,
			Status: issue.Status,
		})
	}

	if err := service.ProcessAuditOutput(req.ChangeID, result); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"change_id": req.ChangeID, "level": req.Level}})
}

type FixOutputRequest struct {
	ChangeID     string `json:"change_id"`
	Action       string `json:"action"`
	Fixed        bool   `json:"fixed"`
	DelegateTo   string `json:"delegate_to,omitempty"`
	RejectReason string `json:"reject_reason,omitempty"`
}

func (h *AgentHandler) FixOutput(c *gin.Context) {
	var req FixOutputRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if req.Action != "fix" && req.Action != "delegate" && req.Action != "reject" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "action must be fix, delegate, or reject"}})
		return
	}

	result := &service.FixResult{
		Action:       req.Action,
		Fixed:        req.Fixed,
		DelegateTo:   req.DelegateTo,
		RejectReason: req.RejectReason,
	}

	if err := service.ProcessFixOutput(req.ChangeID, result); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"change_id": req.ChangeID, "action": req.Action}})
}

type Audit2OutputRequest struct {
	ChangeID     string `json:"change_id"`
	Result       string `json:"result"`
	RejectReason string `json:"reject_reason,omitempty"`
}

func (h *AgentHandler) Audit2Output(c *gin.Context) {
	var req Audit2OutputRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	if req.Result != "merge" && req.Result != "reject" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "result must be merge or reject"}})
		return
	}

	result := &service.Audit2Result{
		Result:       req.Result,
		RejectReason: req.RejectReason,
	}

	if err := service.ProcessAudit2Output(req.ChangeID, result); err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"change_id": req.ChangeID, "result": req.Result}})
}

func (h *AgentHandler) GetSession(c *gin.Context) {
	sessionID := c.Param("session_id")
	session := agent.DefaultManager.GetSession(sessionID)
	if session == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Session not found"}})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":             session.ID,
			"role":           session.Role,
			"project_id":     session.ProjectID,
			"change_id":      session.ChangeID,
			"trigger_reason": session.TriggerReason,
			"status":         session.Status,
			"output":         session.Output,
		},
	})
}

func (h *AgentHandler) GetPrompt(c *gin.Context) {
	sessionID := c.Param("session_id")
	session := agent.DefaultManager.GetSession(sessionID)
	if session == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Session not found"}})
		return
	}

	prompt, err := agent.BuildPrompt(session.Role, session.Context)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": err.Error()}})
		return
	}

	roleConfig := agent.GetRoleConfig(session.Role)
	tools := agent.GetToolsForRole(session.Role)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"session_id":      session.ID,
			"role":            session.Role,
			"prompt":          prompt,
			"platform_tools":  tools,
			"opencode_tools":  roleConfig.OpenCodeTools,
		},
	})
}

func (h *AgentHandler) SubmitOutput(c *gin.Context) {
	sessionID := c.Param("session_id")
	session := agent.DefaultManager.GetSession(sessionID)
	if session == nil {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "SYSTEM_ERROR", "message": "Session not found"}})
		return
	}

	var output map[string]interface{}
	if err := c.ShouldBindJSON(&output); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	// Handle project_status tool: return project snapshot instead of just storing output
	if toolName, _ := output["tool"].(string); toolName == "project_status" {
		projectID := session.ProjectID
		direction, _ := repoGetContentBlock(projectID, "direction")
		milestone, _ := repoGetCurrentMilestone(projectID)
		version, _ := repoGetContentBlock(projectID, "version")
		tasks, _ := repoGetTasksByProject(projectID)
		locks, _ := repoGetLocksByProject(projectID)

		taskList := make([]gin.H, 0)
		for _, t := range tasks {
			taskList = append(taskList, gin.H{
				"id": t.ID, "name": t.Name, "status": t.Status, "priority": t.Priority,
			})
		}
		lockList := make([]gin.H, 0)
		for _, l := range locks {
			var files []string
			json.Unmarshal([]byte(l.Files), &files)
			lockList = append(lockList, gin.H{"agent_name": syncGetAgentName(l.AgentID), "files": files})
		}

		data := gin.H{"version": "unknown", "tasks": taskList, "locks": lockList}
		if direction != nil { data["direction"] = direction.Content }
		if milestone != nil { data["milestone"] = milestone.Name }
		if version != nil { data["version"] = version.Content }

		c.JSON(200, gin.H{"success": true, "data": data})
		return
	}

	outputJSON, _ := json.Marshal(output)
	agent.DefaultManager.UpdateSessionOutput(sessionID, string(outputJSON))

	// If this is a tool output, process it through the tool handler
	if toolName, _ := output["tool"].(string); toolName != "" && toolName != "project_status" {
		go service.HandleToolCallResult(sessionID, session.ChangeID, session.ProjectID, toolName, output)
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"session_id": sessionID,
			"status":     "completed",
		},
	})
}

func (h *AgentHandler) ListSessions(c *gin.Context) {
	projectID := c.Query("project_id")

	sessions := make([]gin.H, 0)
	for id, session := range agent.DefaultManager.Sessions() {
		if projectID != "" && session.ProjectID != projectID {
			continue
		}
		sessions = append(sessions, gin.H{
			"id":             id,
			"role":           session.Role,
			"project_id":     session.ProjectID,
			"change_id":      session.ChangeID,
			"trigger_reason": session.TriggerReason,
			"status":         session.Status,
		})
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"sessions": sessions}})
}