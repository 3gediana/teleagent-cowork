package handler

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"
)

// allowedSubmitOutputTools enumerates the tool names /internal/agent/session/:id/output
// accepts. The service layer (service.HandleToolCallResult) silently logs and drops
// unknown names, which made the endpoint a quiet sink for typo'd or attacker-probe
// payloads. Keep this list in sync with the switch in tool_handler.go —
// "project_status" is handled inline in SubmitOutput itself.
var allowedSubmitOutputTools = map[string]struct{}{
	"project_status":       {},
	"audit_output":         {},
	"fix_output":           {},
	"audit2_output":        {},
	"create_task":          {},
	"delete_task":          {},
	"update_milestone":     {},
	"propose_direction":    {},
	"write_milestone":      {},
	"assess_output":        {},
	"approve_pr":           {},
	"reject_pr":            {},
	"switch_milestone":     {},
	"create_policy":        {},
	"delegate_to_maintain": {},
	"chief_output":         {},
	"evaluate_output":      {},
	"merge_output":         {},
	"biz_review_output":    {},
	"analyze_output":       {},
}

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
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "SESSION_NOT_FOUND", "message": "Session not found"}})
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
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "SESSION_NOT_FOUND", "message": "Session not found"}})
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
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "SESSION_NOT_FOUND", "message": "Session not found"}})
		return
	}

	// Session-owner check. Before this, any authenticated caller could POST
	// output for any session ID they could guess — driving the Fix Agent,
	// merging PRs, etc. on behalf of another agent. The link from session
	// back to its owning agent lives on Agent.SessionID (set when the session
	// is dispatched). If the caller's agent row does not currently hold this
	// session, reject.
	//
	// Exception: platform-internal sessions (native runner, Chief auto-loop)
	// are not bound to any Agent row — their SessionID mapping sits in
	// Agent.SessionID="" — but those sessions are also never submitted via
	// HTTP because the native runner completes them in-process. If a future
	// sidecar needs this path, it should register as an agent and claim the
	// session the normal way.
	callerID, _ := c.Get("agent_id")
	callerAgentID, _ := callerID.(string)
	if callerAgentID == "" {
		c.JSON(401, gin.H{"success": false, "error": gin.H{"code": "UNAUTHORIZED", "message": "missing agent context"}})
		return
	}
	var owner model.Agent
	if err := model.DB.Where("id = ? AND session_id = ?", callerAgentID, sessionID).First(&owner).Error; err != nil {
		c.JSON(403, gin.H{"success": false, "error": gin.H{
			"code":    "SESSION_NOT_YOURS",
			"message": "this session is not owned by the calling agent",
		}})
		return
	}

	var output map[string]interface{}
	if err := c.ShouldBindJSON(&output); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}

	// Tool whitelist. The server-side switch in service.HandleToolCallResult
	// silently drops unknown tool names (log only), which made the endpoint
	// a quiet sink for typos and probes. Reject up front with a machine-
	// readable code so clients notice their mistake instead of seeing a
	// success response with no effect. An empty tool name is allowed — it
	// represents a plain text completion with no tool dispatch.
	if toolName, _ := output["tool"].(string); toolName != "" {
		if _, ok := allowedSubmitOutputTools[toolName]; !ok {
			c.JSON(400, gin.H{"success": false, "error": gin.H{
				"code":    "UNKNOWN_TOOL",
				"message": "tool not recognised by platform",
				"tool":    toolName,
			}})
			return
		}
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