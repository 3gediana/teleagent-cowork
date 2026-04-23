package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"text/template"
	"time"

	"github.com/a3c/platform/internal/model"
)

type Session struct {
	ID                string
	Role              Role
	ProjectID         string
	ChangeID          string
	PRID              string // PullRequest ID for evaluate/merge agents
	TriggerReason     string
	Context           *SessionContext
	Status            string // pending, running, completed, failed
	Output            string
	OpenCodeSessionID string // opencode serve session ID
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
	TriggerReason  string
	LockList       string

	// PR evaluation fields
	PRTitle           string
	PRDescription     string
	SubmitterName     string
	BranchName        string
	BaseVersion       string
	SelfReview        string
	DiffStat          string
	DiffFull          string
	MergeCheckResult  string
	MergeCostRating   string
	ConflictFiles     string
	TechReviewSummary string

	// Chief Agent fields
	GlobalState string // 平台全局状态快照
	AutoMode    bool   // 项目 AutoMode 开关

	// Refinery feedback: IDs of KnowledgeArtifacts injected into this prompt.
	// Persisted on AgentSession so session-completion hooks can bump
	// success/failure counts.
	InjectedArtifactIDs []string
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
	mu       sync.RWMutex
	sessions map[string]*Session
}

var DefaultManager = &AgentManager{
	sessions: make(map[string]*Session),
}

type SessionDispatcher func(session *Session) error

// SessionFailureHook is invoked whenever DispatchSession's dispatcher
// returns an error. Lives here (rather than in the service package
// that owns SSE) so the agent package stays import-cycle-free; the
// service layer registers a concrete broadcast implementation at
// startup via RegisterFailureHook. Runs on the dispatch goroutine
// after the session has already been flipped to status=failed in
// memory + DB, so hooks only need to worry about observer-side
// effects (SSE event, dialogue message append, metrics).
type SessionFailureHook func(session *Session, err error)

var (
	dispatcher  SessionDispatcher
	failureHook SessionFailureHook
)

func RegisterDispatcher(d SessionDispatcher) {
	dispatcher = d
}

// RegisterFailureHook wires in the observer side-effects (SSE broadcast
// etc.) invoked when the dispatcher returns an error. Calling with nil
// clears the hook, which is only useful in tests. Safe to call before
// any session is dispatched; the hook is read on each dispatch.
func RegisterFailureHook(h SessionFailureHook) {
	failureHook = h
}

func DispatchSession(session *Session) {
	if dispatcher == nil {
		log.Printf("[Agent] No dispatcher registered, session %s stays pending", session.ID)
		return
	}
	go func() {
		err := dispatcher(session)
		if err == nil {
			return
		}
		// Previously dispatch errors were logged and swallowed, which
		// made e.g. "no LLM endpoints registered" invisible to anyone
		// chatting with the Chief — the session would stay pending
		// forever and the client saw a 200 OK from the handler. Now
		// we flip the in-memory session + DB row to failed and stash
		// the error message on Output so operators can see it in the
		// session browser, then let the failure hook broadcast.
		log.Printf("[Agent] Failed to dispatch session %s: %v", session.ID, err)
		session.Status = "failed"
		session.Output = err.Error()
		model.DB.Model(&model.AgentSession{}).
			Where("id = ?", session.ID).
			Updates(map[string]interface{}{
				"status": "failed",
				"output": err.Error(),
			})
		if failureHook != nil {
			failureHook(session, err)
		}
	}()
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
	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()

	// Persist to DB
	dbSession := &model.AgentSession{
		ID:            sessionID,
		Role:          string(role),
		ProjectID:     projectID,
		TriggerReason: trigger,
		Status:        "pending",
		CreatedAt:     time.Now(),
	}
	if ctx != nil && ctx.ChangeInfo != nil {
		dbSession.ChangeID = ctx.ChangeInfo.ChangeID
	}
	if ctx != nil && len(ctx.InjectedArtifactIDs) > 0 {
		if b, err := json.Marshal(ctx.InjectedArtifactIDs); err == nil {
			dbSession.InjectedArtifacts = string(b)
		}
	}
	if err := model.DB.Create(dbSession).Error; err != nil {
		log.Printf("[Agent] Failed to persist session %s to DB: %v", sessionID, err)
	}

	return session
}

func (m *AgentManager) RegisterSession(session *Session) {
	m.mu.Lock()
	m.sessions[session.ID] = session
	m.mu.Unlock()

	// Persist to DB (upsert)
	dbSession := &model.AgentSession{
		ID:            session.ID,
		Role:          string(session.Role),
		ProjectID:     session.ProjectID,
		ChangeID:      session.ChangeID,
		PRID:          session.PRID,
		TriggerReason:  session.TriggerReason,
		Status:        session.Status,
		OpenCodeSessionID: session.OpenCodeSessionID,
		CreatedAt:     time.Now(),
	}
	if err := model.DB.Create(dbSession).Error; err != nil {
		// Session may already exist in DB (e.g. from CreateSession), update instead
		model.DB.Model(&model.AgentSession{}).Where("id = ?", session.ID).Updates(map[string]interface{}{
			"status":              session.Status,
			"opencode_session_id": session.OpenCodeSessionID,
		})
	}
}

func (m *AgentManager) GetSession(id string) *Session {
	m.mu.RLock()
	if s, ok := m.sessions[id]; ok {
		m.mu.RUnlock()
		return s
	}
	m.mu.RUnlock()
	// Fallback: load from DB
	var dbSession model.AgentSession
	if err := model.DB.Where("id = ?", id).First(&dbSession).Error; err != nil {
		return nil
	}
	s := &Session{
		ID:                dbSession.ID,
		Role:              Role(dbSession.Role),
		ProjectID:         dbSession.ProjectID,
		ChangeID:          dbSession.ChangeID,
		PRID:              dbSession.PRID,
		TriggerReason:     dbSession.TriggerReason,
		Status:            dbSession.Status,
		Output:            dbSession.Output,
		OpenCodeSessionID: dbSession.OpenCodeSessionID,
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s
}

// UpdateSessionOutput stores the structured output a terminal tool
// (chief_output, audit_output, evaluate_output, merge_output, ...)
// produced for this session. It ONLY touches `output` — the status
// transition to "completed" happens in runner/dispatcher.go:runNative
// AFTER the post-loop feedback hook has fired. Flipping status here
// would race the dispatcher: callers polling for "completed" would
// observe the session as done BEFORE HandleSessionCompletion has
// written the assistant turn to DialogueMessage (because the chief
// agent emits its tool call mid-loop, not as the final message).
//
// Previously this method also set status=completed + completed_at,
// which caused a classic multi-round dialogue bug: the second Chief
// chat turn would see status=completed on the first turn too early,
// send its own user message, and we'd end up with 2 user messages
// queued back-to-back before either assistant reply landed. The
// transcript would then show only one assistant reply for both
// user turns.
func (m *AgentManager) UpdateSessionOutput(id string, output string) {
	m.mu.RLock()
	session, ok := m.sessions[id]
	m.mu.RUnlock()
	if ok {
		session.Output = output
	}
	if err := model.DB.Model(&model.AgentSession{}).Where("id = ?", id).
		Update("output", output).Error; err != nil {
		log.Printf("[Agent] UpdateSessionOutput persist failed for %s: %v", id, err)
	}
}

func (m *AgentManager) MarkSessionFailed(id string) {
	m.mu.RLock()
	session, ok := m.sessions[id]
	m.mu.RUnlock()
	if ok {
		session.Status = "failed"

		now := time.Now()
		model.DB.Model(&model.AgentSession{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":       "failed",
			"completed_at":  now,
		})
	}
}

func (m *AgentManager) Sessions() map[string]*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make(map[string]*Session, len(m.sessions))
	for k, v := range m.sessions {
		cp[k] = v
	}
	return cp
}

func (m *AgentManager) ClearSession(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
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
	data["TriggerReason"] = ctx.TriggerReason
	data["LockList"] = ctx.LockList

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

	// PR evaluation fields
	data["PRTitle"] = ctx.PRTitle
	data["PRDescription"] = ctx.PRDescription
	data["SubmitterName"] = ctx.SubmitterName
	data["BranchName"] = ctx.BranchName
	data["BaseVersion"] = ctx.BaseVersion
	data["SelfReview"] = ctx.SelfReview
	data["DiffStat"] = ctx.DiffStat
	data["DiffFull"] = ctx.DiffFull
	data["MergeCheckResult"] = ctx.MergeCheckResult
	data["MergeCostRating"] = ctx.MergeCostRating
	data["ConflictFiles"] = ctx.ConflictFiles
	data["TechReviewSummary"] = ctx.TechReviewSummary

	// Chief Agent fields
	data["GlobalState"] = ctx.GlobalState
	data["AutoMode"] = ctx.AutoMode

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
	case "pr_evaluate":
		return RoleEvaluate
	case "pr_merge":
		return RoleMerge
	case "pr_biz_review":
		return RoleMaintain
	case "chief_request", "chief_chat":
		return RoleChief
	case "chief_decision_pr_review", "chief_decision_pr_merge", "chief_decision_milestone_switch":
		return RoleChief
	case "analyze_distill":
		return RoleAnalyze
	default:
		return RoleMaintain
	}
}