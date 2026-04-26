package service

// Observer hook for failed session dispatches. Before this lived on
// the service side, dispatch errors (no LLM endpoints registered,
// provider rejects API key, role override points at a missing
// endpoint, ...) were logged and swallowed: the user would chat with
// the Chief, see a 200 OK, and then ... nothing. This module is
// wired into agent.DispatchSession via agent.RegisterFailureHook at
// startup so every failure pushes an AGENT_ERROR over SSE and —
// for chat-style roles — appends a system message to the dialogue
// history so the transcript shows what went wrong.

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/a3c/platform/internal/agent"
)

// HandleDispatchFailure is registered as agent.SessionFailureHook.
// It runs on the dispatcher goroutine after the agent package has
// already marked the session failed in memory + DB, so here we only
// deal with observer concerns: SSE broadcast + dialogue continuity.
//
// Errors inside this hook are deliberately swallowed after logging;
// we never want an observer hiccup to mask the original dispatch
// error from the operator (the agent package already logged it).
func HandleDispatchFailure(session *agent.Session, dispatchErr error) {
	if session == nil {
		return
	}
	msg := "unknown error"
	if dispatchErr != nil {
		msg = dispatchErr.Error()
	}

	// AGENT_ERROR mirrors what the native runner emits when an
	// LLM/tool call fails mid-session — the dashboard already
	// renders it as a red system line in the chat pane (see
	// platform/frontend/src/hooks/useSSE.ts case 'AGENT_ERROR'). We piggy-
	// back on that plumbing so dispatch-time failures look the
	// same as run-time failures.
	BroadcastEvent(session.ProjectID, "AGENT_ERROR", gin.H{
		"session_id": session.ID,
		"role":       string(session.Role),
		"error":      msg,
	})

	// For chat-style roles we also persist a system-role turn in
	// dialogue history, so the transcript remains faithful even
	// if the operator later hits "Clear broadcast buffer" or
	// reloads the page. Non-chat roles (audit / fix / evaluate
	// / merge / analyze / assess) have no dialogue channel — the
	// SSE event alone is enough.
	channel := DialogueChannelForRole(string(session.Role))
	if channel == "" {
		return
	}
	// Use a distinct "system" role so the frontend renders it in
	// the error style rather than as assistant output. Reuses the
	// same AppendDialogueMessage path so future history rebuilds
	// keep the failure visible.
	AppendDialogueMessage(session.ProjectID, channel, session.ID, "system", "Session failed to start: "+msg)

	log.Printf("[DispatchFailure] Session %s (role=%s project=%s) reported to dashboard: %s",
		session.ID, session.Role, session.ProjectID, msg)
}
