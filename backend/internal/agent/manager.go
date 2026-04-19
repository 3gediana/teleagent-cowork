package agent

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/a3c/platform/internal/model"
)

type Session struct {
	ID           string
	Role         Role
	ProjectID    string
	ChangeID     string
	TriggerReason string
	Context      *SessionContext
	Status       string // pending, running, completed, failed
	Output       string
}

type SessionContext struct {
	DirectionBlock string
	MilestoneBlock string
	TaskList       string
	Version        string
	AgentName      string
	ChangeInfo     *ChangeContext
	InputContent   string
	ProjectPath    string
}

type ChangeContext struct {
	ChangeID     string
	TaskName     string
	TaskDesc     string
	AgentName    string
	ModifiedFiles []string
	NewFiles      []string
	DeletedFiles  []string
	Diff          string
	AuditIssues   string
}

type AgentManager struct {
	sessions map[string]*Session
}

var DefaultManager = &AgentManager{
	sessions: make(map[string]*Session),
}

func (m *AgentManager) CreateSession(role Role, projectID string, ctx *SessionContext, trigger string) *Session {
	sessionID := model.GenerateID("session")
	session := &Session{
		ID:            sessionID,
		Role:          role,
		ProjectID:    projectID,
		Context:       ctx,
		TriggerReason: trigger,
		Status:        "pending",
	}
	m.sessions[sessionID] = session
	return session
}

func (m *AgentManager) GetSession(id string) *Session {
	return m.sessions[id]
}

func (m *AgentManager) UpdateSessionOutput(id string, output string) {
	if session, ok := m.sessions[id]; ok {
		session.Output = output
		session.Status = "completed"
	}
}

func (m *AgentManager) MarkSessionFailed(id string) {
	if session, ok := m.sessions[id]; ok {
		session.Status = "failed"
	}
}

func (m *AgentManager) Sessions() map[string]*Session {
	return m.sessions
}

func (m *AgentManager) ClearSession(id string) {
	delete(m.sessions, id)
}

func BuildPrompt(role Role, ctx *SessionContext) (string, error) {
	cfg := GetRoleConfig(role)
	if cfg == nil {
		return "", fmt.Errorf("unknown role: %s", role)
	}

	tmplContent, err := getPromptTemplate(cfg.PromptTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to load prompt template %s: %w", cfg.PromptTemplate, err)
	}

	tmpl, err := template.New("prompt").Parse(tmplContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse prompt template: %w", err)
	}

	data := make(map[string]interface{})
	data["DirectionBlock"] = ctx.DirectionBlock
	data["MilestoneBlock"] = ctx.MilestoneBlock
	data["TaskList"] = ctx.TaskList
	data["Version"] = ctx.Version
	data["AgentName"] = ctx.AgentName
	data["InputContent"] = ctx.InputContent
	data["ProjectPath"] = ctx.ProjectPath

	if ctx.ChangeInfo != nil {
		data["ChangeID"] = ctx.ChangeInfo.ChangeID
		data["TaskName"] = ctx.ChangeInfo.TaskName
		data["TaskDesc"] = ctx.ChangeInfo.TaskDesc
		data["ModifiedFiles"] = ctx.ChangeInfo.ModifiedFiles
		data["NewFiles"] = ctx.ChangeInfo.NewFiles
		data["DeletedFiles"] = ctx.ChangeInfo.DeletedFiles
		data["Diff"] = ctx.ChangeInfo.Diff
		data["AuditIssues"] = ctx.ChangeInfo.AuditIssues
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute prompt template: %w", err)
	}

	return buf.String(), nil
}

func GetRoleForTrigger(trigger string) Role {
	switch trigger {
	case "change_submitted":
		return RoleAudit1
	case "fix_needed":
		return RoleFix
	case "re_audit":
		return RoleAudit2
	case "dashboard_input", "milestone_complete", "timer":
		return RoleMaintain
	case "project_info":
		return RoleConsult
	case "project_import":
		return RoleAssess
	default:
		return RoleMaintain
	}
}