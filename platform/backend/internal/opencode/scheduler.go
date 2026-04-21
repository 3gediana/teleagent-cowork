package opencode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/config"
)

type Scheduler struct {
	cfg          config.OpenCodeConfig
	pureServeURL string
	pureCmd      *exec.Cmd
	httpClient   *http.Client
}

var DefaultScheduler *Scheduler

// ToolCallHandler is called when a platform tool is invoked by the agent
var ToolCallHandler func(sessionID, changeID, projectID, toolName string, args map[string]interface{})

// BroadcastHandler is called to push real-time events (e.g. agent text, tool results) to the frontend via SSE
var BroadcastHandler func(projectID, eventType string, payload map[string]interface{})

// agentServeSessionMap tracks agentID -> ocSessionID for injecting poll messages into serve sessions
var (
	agentServeSessionMap   = make(map[string]string)
	agentServeSessionMutex sync.RWMutex
)

// RegisterAgentServeSession maps an agentID to its opencode serve session ID
func RegisterAgentServeSession(agentID, ocSessionID string) {
	agentServeSessionMutex.Lock()
	agentServeSessionMap[agentID] = ocSessionID
	agentServeSessionMutex.Unlock()
}

// UnregisterAgentServeSession removes the mapping
func UnregisterAgentServeSession(agentID string) {
	agentServeSessionMutex.Lock()
	delete(agentServeSessionMap, agentID)
	agentServeSessionMutex.Unlock()
}

// GetAgentServeSession returns the ocSessionID for an agent
func GetAgentServeSession(agentID string) string {
	agentServeSessionMutex.RLock()
	defer agentServeSessionMutex.RUnlock()
	return agentServeSessionMap[agentID]
}

func InitScheduler(cfg config.OpenCodeConfig) {
	// Find a free port for pure serve
	port := findFreePort(15000)
	
	// Inline config that disables ALL MCP servers and only allows platform tools
	pureConfigContent := `{"$schema":"https://opencode.ai/config.json","permission":"allow","tools":{"a3c_*":false,"tavily_*":false,"context7_*":false}}`
	
	args := []string{"serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)}
	cmd := exec.Command("opencode", args...)
	cmd.Dir = cfg.ProjectPath
	if cmd.Dir == "" {
		cmd.Dir = "."
	}
	// Override config to disable MCP
	cmd.Env = append(os.Environ(), "OPENCODE_CONFIG_CONTENT="+pureConfigContent)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &bytes.Buffer{}

	err := cmd.Start()
	if err != nil {
		log.Printf("[OpenCode] Failed to start pure serve: %v", err)
		DefaultScheduler = &Scheduler{cfg: cfg}
		return
	}

	// Wait for serve to be ready
	time.Sleep(3 * time.Second)
	
	DefaultScheduler = &Scheduler{
		cfg:          cfg,
		pureServeURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		pureCmd:      cmd,
		httpClient:   &http.Client{Timeout: 180 * time.Second},
	}
	log.Printf("[OpenCode] Pure serve started on port %d", port)
}

func findFreePort(start int) int {
	for p := start; p < start+100; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	return 0
}

// GetModelString returns the configured model string in provider/model format
func (s *Scheduler) GetModelString() string {
	model := fmt.Sprintf("%s/%s", s.cfg.DefaultModelProvider, s.cfg.DefaultModelID)
	if s.cfg.DefaultModelProvider == "" {
		model = "minimax-coding-plan/MiniMax-M2.7"
	}
	return model
}

func (s *Scheduler) Dispatch(session *agent.Session) error {
	roleConfig := agent.GetRoleConfig(session.Role)
	if roleConfig == nil {
		return fmt.Errorf("unknown role: %s", session.Role)
	}

	prompt, err := agent.BuildPrompt(session.Role, session.Context)
	if err != nil {
		return fmt.Errorf("failed to build prompt: %w", err)
	}

	fullMessage := s.buildFullMessage(session, prompt)

	agentName := string(session.Role)
	modelStr := fmt.Sprintf("%s/%s", s.cfg.DefaultModelProvider, s.cfg.DefaultModelID)
	if s.cfg.DefaultModelProvider == "" {
		modelStr = "minimax-coding-plan/MiniMax-M2.7"
	}

	log.Printf("[OpenCode] Dispatching session %s: agent=%s, model=%s", session.ID, agentName, modelStr)

	go s.runAgentViaServe(session, fullMessage, agentName, modelStr)

	return nil
}

func (s *Scheduler) buildFullMessage(session *agent.Session, prompt string) string {
	var contextParts []string
	ctx := session.Context
	if ctx == nil {
		return prompt
	}

	if ctx.DirectionBlock != "" {
		contextParts = append(contextParts, "## Current Direction\n"+ctx.DirectionBlock)
	}
	if ctx.MilestoneBlock != "" {
		contextParts = append(contextParts, "## Current Milestone\n"+ctx.MilestoneBlock)
	}
	if ctx.TaskList != "" {
		contextParts = append(contextParts, "## Task List\n"+ctx.TaskList)
	}
	if ctx.Version != "" {
		contextParts = append(contextParts, "## Current Version: "+ctx.Version)
	}
	if ctx.ProjectPath != "" {
		contextParts = append(contextParts, "## Project Path\n"+ctx.ProjectPath)
	}
	if ctx.ChangeInfo != nil {
		ci := ctx.ChangeInfo
		var items []string
		if ci.TaskName != "" {
			items = append(items, fmt.Sprintf("Task: %s - %s", ci.TaskName, ci.TaskDesc))
		}
		if ci.AgentName != "" {
			items = append(items, fmt.Sprintf("Submitter: %s", ci.AgentName))
		}
		if len(ci.ModifiedFiles) > 0 {
			items = append(items, fmt.Sprintf("Modified files: %s", strings.Join(ci.ModifiedFiles, ", ")))
		}
		if len(ci.NewFiles) > 0 {
			items = append(items, fmt.Sprintf("New files: %s", strings.Join(ci.NewFiles, ", ")))
		}
		if len(ci.DeletedFiles) > 0 {
			items = append(items, fmt.Sprintf("Deleted files: %s", strings.Join(ci.DeletedFiles, ", ")))
		}
		if ci.Diff != "" {
			items = append(items, "## Diff\n"+ci.Diff)
		}
		if ci.AuditIssues != "" {
			items = append(items, "## Flagged Issues\n"+ci.AuditIssues)
		}
		if len(items) > 0 {
			contextParts = append(contextParts, "## Change Information\n"+strings.Join(items, "\n"))
		}
	}

	var fullMessage string
	if ctx.InputContent != "" {
		fullMessage += "## TASK TO COMPLETE\n" + ctx.InputContent + "\n\n"
	}

	if len(contextParts) > 0 {
		fullMessage += strings.Join(contextParts, "\n\n") + "\n\n"
	}

	fullMessage += prompt
	return fullMessage
}

// serve API response types

type serveSessionResponse struct {
	ID string `json:"id"`
}

type serveMessageResponse struct {
	Info  serveMessageInfo `json:"info"`
	Parts []servePart      `json:"parts"`
}

type serveMessageInfo struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Role      string `json:"role"`
	Agent     string `json:"agent"`
	ModelID   string `json:"modelID"`
	Finish    string `json:"finish"`
}

type servePart struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Tool    string          `json:"tool"`
	SessionID string        `json:"sessionID"`
	MessageID string        `json:"messageID"`
	ID      string          `json:"id"`
	State   *serveToolState `json:"state,omitempty"`
}

type serveToolState struct {
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Status string          `json:"status"`
}

// legacy jsonEvent types (kept for backward compat)

type jsonEvent struct {
	Type      string    `json:"type"`
	SessionID string    `json:"sessionID"`
	Part      jsonPart  `json:"part"`
}

type jsonPart struct {
	Type    string          `json:"type"`
	Tool    string          `json:"tool"`
	State   jsonToolState   `json:"state"`
	Text    string          `json:"text"`
	Reason  string          `json:"reason"`
}

type jsonToolState struct {
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Status string          `json:"status"`
}

// runAgentViaServe creates a serve session, sends the message via HTTP API, and processes the response.
// This replaces the old run --pure approach, enabling multi-round conversation support.
func (s *Scheduler) runAgentViaServe(session *agent.Session, message string, agentName string, modelStr string) {
	defer func() {
		if session.Status == "running" {
			session.Status = "failed"
		}
		// Note: audit/fix sessions are not in agentServeSessionMap, no cleanup needed
	}()

	session.Status = "running"

	if s.pureServeURL == "" {
		log.Printf("[OpenCode] No serve URL, falling back to run --pure for session %s", session.ID)
		s.runAgentLegacy(session, message, agentName, modelStr)
		return
	}

	// 1. Create a new serve session
	ocSessionID, err := s.createServeSession()
	if err != nil {
		log.Printf("[OpenCode] Failed to create serve session for %s: %v, falling back", session.ID, err)
		s.runAgentLegacy(session, message, agentName, modelStr)
		return
	}

	session.OpenCodeSessionID = ocSessionID

	// Note: we do NOT register audit/fix agent sessions in agentServeSessionMap.
	// Those are internal platform agents that don't poll. Only the maintain agent
	// (registered via maintain.go) needs poll injection.

	log.Printf("[OpenCode] Created serve session %s for agent session %s (agent=%s)", ocSessionID, session.ID, agentName)

	// 2. Send message to the serve session
	msgResp, err := s.sendServeMessage(ocSessionID, message, agentName, modelStr, "")
	if err != nil {
		log.Printf("[OpenCode] Failed to send message to serve session %s: %v", ocSessionID, err)
		session.Status = "failed"
		return
	}

	// 3. Process the response parts
	var textParts []string
	for _, part := range msgResp.Parts {
		switch part.Type {
		case "text":
			textParts = append(textParts, part.Text)
		case "tool-invocation":
			// Tool was already executed by serve; log it
			log.Printf("[OpenCode] Serve session %s: tool invoked: %s", ocSessionID, part.Tool)
		}
	}

	session.Output = strings.Join(textParts, "\n")
	session.Status = "completed"
	log.Printf("[OpenCode] Session %s completed via serve, output length=%d", session.ID, len(session.Output))
	if len(session.Output) > 0 {
		log.Printf("[OpenCode] Session %s OUTPUT:\n%s", session.ID, session.Output)
	}

	// 4. Broadcast agent text response to frontend in real-time
	if len(textParts) > 0 && BroadcastHandler != nil {
		BroadcastHandler(session.ProjectID, "CHAT_UPDATE", map[string]interface{}{
			"role":    "agent",
			"content": session.Output,
		})
	}

	// 5. Process tool calls from the serve session messages
	s.processServeToolCalls(session, ocSessionID)
}

// SendToExistingSession sends a follow-up message to an existing serve session (for multi-round dialogue)
// noReply=true means inject context without expecting the agent to respond
func (s *Scheduler) SendToExistingSession(ocSessionID string, message string, agentName string, model string, noReply ...bool) (*serveMessageResponse, error) {
	nr := ""
	if len(noReply) > 0 && noReply[0] {
		nr = "true"
	}
	return s.sendServeMessage(ocSessionID, message, agentName, model, nr)
}

// DeleteServeSession destroys a serve session (for clearing context)
func (s *Scheduler) DeleteServeSession(ocSessionID string) error {
	url := s.pureServeURL + "/session/" + ocSessionID
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create delete request: %w", err)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	defer resp.Body.Close()
	log.Printf("[OpenCode] Deleted serve session %s", ocSessionID)
	return nil
}

// GetSessionMessages fetches all messages from a serve session (for dialogue history)
func (s *Scheduler) GetSessionMessages(ocSessionID string) ([]interface{}, error) {
	url := fmt.Sprintf("%s/session/%s/message?limit=100", s.pureServeURL, ocSessionID)
	resp, err := s.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read messages response: %w", err)
	}

	var messages []interface{}
	if err := json.Unmarshal(body, &messages); err != nil {
		return nil, fmt.Errorf("failed to parse messages: %w", err)
	}

	return messages, nil
}

func (s *Scheduler) createServeSession() (string, error) {
	url := s.pureServeURL + "/session"
	resp, err := s.httpClient.Post(url, "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result serveSessionResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse session response: %w", err)
	}

	if result.ID == "" {
		return "", fmt.Errorf("empty session ID in response")
	}

	return result.ID, nil
}

func (s *Scheduler) sendServeMessage(ocSessionID string, message string, agentName string, model string, noReplyFlag string) (*serveMessageResponse, error) {
	url := fmt.Sprintf("%s/session/%s/message", s.pureServeURL, ocSessionID)

	parts := []map[string]interface{}{
		{"type": "text", "text": message},
	}

	body := map[string]interface{}{
		"parts": parts,
	}
	if agentName != "" {
		body["agent"] = agentName
	}
	if model != "" {
		body["model"] = model
	}
	if noReplyFlag == "true" {
		body["noReply"] = true
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message body: %w", err)
	}

	resp, err := s.httpClient.Post(url, "application/json", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var msgResp serveMessageResponse
	if err := json.Unmarshal(respBody, &msgResp); err != nil {
		return nil, fmt.Errorf("failed to parse message response: %w", err)
	}

	return &msgResp, nil
}

// processServeToolCalls fetches messages from the serve session and extracts tool call results
// that were executed by the custom tools (which call back to platform APIs)
func (s *Scheduler) processServeToolCalls(session *agent.Session, ocSessionID string) {
	url := fmt.Sprintf("%s/session/%s/message?limit=20", s.pureServeURL, ocSessionID)
	resp, err := s.httpClient.Get(url)
	if err != nil {
		log.Printf("[OpenCode] Failed to fetch messages for tool call processing: %v", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[OpenCode] Failed to read messages response: %v", err)
		return
	}

	// The response is an array of message objects with parts
	var messages []struct {
		Info struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []servePart `json:"parts"`
	}
	if err := json.Unmarshal(body, &messages); err != nil {
		log.Printf("[OpenCode] Failed to parse messages for tool calls: %v", err)
		return
	}

	// Look for tool-invocation parts in assistant messages
	for _, msg := range messages {
		if msg.Info.Role != "assistant" {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type == "tool-invocation" && part.Tool != "" {
				var inputRaw json.RawMessage
				if part.State != nil && len(part.State.Input) > 0 {
					inputRaw = part.State.Input
				} else {
					inputRaw = json.RawMessage("{}")
				}
				s.processToolCall(session, part.Tool, inputRaw, "")
			}
		}
	}
}

// runAgentLegacy is the old opencode run --pure approach, kept as fallback
func (s *Scheduler) runAgentLegacy(session *agent.Session, message string, agentName string, model string) {
	defer func() {
		if session.Status == "running" {
			session.Status = "failed"
		}
	}()

	session.Status = "running"

	args := []string{
		"run",
		"--pure",
		"--agent", agentName,
		"--model", model,
		"--format", "json",
		"--dangerously-skip-permissions",
	}

	cmd := exec.Command("opencode", args...)
	cmd.Dir = "."
	cmd.Stdin = bytes.NewReader([]byte(message + "\n"))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[OpenCode] Running (legacy): opencode run --pure --agent %s --model %s (stdin len=%d)", agentName, model, len(message))

	err := cmd.Run()
	if err != nil {
		log.Printf("[OpenCode] Session %s error: %v, stderr: %s", session.ID, err, stderr.String())
	}

	session.Output = stdout.String()
	session.Status = "completed"
	log.Printf("[OpenCode] Session %s completed (legacy), output length=%d", session.ID, len(session.Output))

	s.processJSONOutput(session)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (s *Scheduler) processJSONOutput(session *agent.Session) {
	output := session.Output
	if output == "" {
		log.Printf("[OpenCode] Session %s: output is empty, skipping", session.ID)
		return
	}

	log.Printf("[OpenCode] Session %s: processing JSON output, %d bytes", session.ID, len(output))

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}

		var event jsonEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		if event.Type == "tool_use" && event.Part.Tool != "" {
			s.processToolCall(session, event.Part.Tool, event.Part.State.Input, event.Part.State.Output)
		}
	}
}

func (s *Scheduler) processToolCall(session *agent.Session, toolName string, inputRaw json.RawMessage, outputRaw string) {
	platformTools := map[string]bool{
		"create_task":      true,
		"delete_task":      true,
		"update_milestone": true,
		"propose_direction": true,
		"write_milestone":  true,
		"audit_output":     true,
		"fix_output":       true,
		"audit2_output":    true,
		"assess_output":    true,
	}

	if !platformTools[toolName] {
		return
	}

	log.Printf("[OpenCode] Session %s: platform tool_call=%s input=%s", session.ID, toolName, string(inputRaw))

	var args map[string]interface{}
	if err := json.Unmarshal(inputRaw, &args); err != nil {
		log.Printf("[OpenCode] Failed to parse tool args for %s: %v", toolName, err)
		return
	}

	if ToolCallHandler != nil {
		ToolCallHandler(session.ID, session.ChangeID, session.ProjectID, toolName, args)
	}

	// Broadcast tool call to frontend in real-time
	if BroadcastHandler != nil {
		BroadcastHandler(session.ProjectID, "TOOL_CALL", map[string]interface{}{
			"session_id": session.ID,
			"tool":       toolName,
			"args":       args,
		})
	}
}

func (s *Scheduler) PollSession(session *agent.Session) error {
	maxPolls := 120
	pollInterval := 5 * time.Second

	for i := 0; i < maxPolls; i++ {
		time.Sleep(pollInterval)

		if session.Status == "completed" || session.Status == "failed" {
			return nil
		}
	}

	session.Status = "failed"
	return fmt.Errorf("session %s timed out", session.ID)
}
