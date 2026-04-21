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
	"github.com/a3c/platform/internal/model"

	"gorm.io/gorm"
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
	initSchedulerClient(fmt.Sprintf("http://127.0.0.1:%d", port))
	log.Printf("[OpenCode] Pure serve started on port %d", port)
}

// DefaultClient returns an OpenCode API client using the scheduler's serve URL
var DefaultClient *Client

func initSchedulerClient(serveURL string) {
	if serveURL != "" {
		DefaultClient = NewClient(serveURL)
	}
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
	roleConfig := agent.GetRoleConfigWithOverride(session.Role)
	if roleConfig == nil {
		return fmt.Errorf("unknown role: %s", session.Role)
	}

	// M23: Apply matching policies before building prompt
	if session.ProjectID != "" && session.Context != nil {
		s.applyPoliciesToSession(session)
	}

	prompt, err := agent.BuildPrompt(session.Role, session.Context)
	if err != nil {
		return fmt.Errorf("failed to build prompt: %w", err)
	}

	fullMessage := s.buildFullMessage(session, prompt)

	agentName := string(session.Role)

	// Use role-level model override, fall back to global default
	modelProvider := roleConfig.ModelProvider
	modelID := roleConfig.ModelID
	if modelProvider == "" {
		modelProvider = s.cfg.DefaultModelProvider
	}
	if modelID == "" {
		modelID = s.cfg.DefaultModelID
	}
	modelStr := fmt.Sprintf("%s/%s", modelProvider, modelID)
	if modelProvider == "" {
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
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Tool      string          `json:"tool"`
	CallID    string          `json:"callID"`
	SessionID string          `json:"sessionID"`
	MessageID string          `json:"messageID"`
	ID        string          `json:"id"`
	State     *serveToolState `json:"state,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
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
	startTime := time.Now()
	defer func() {
		if session.Status == "running" {
			session.Status = "failed"
		}
		// Record duration and update DB
		durationMs := int(time.Since(startTime).Milliseconds())
		now := time.Now()
		model.DB.Model(&model.AgentSession{}).Where("id = ?", session.ID).Updates(map[string]interface{}{
			"status":       session.Status,
			"duration_ms":  durationMs,
			"completed_at": now,
			"model_id":     modelStr,
		})

		// Retry logic: if session failed and role supports retry, re-dispatch
		if session.Status == "failed" {
			s.maybeRetry(session, message, agentName, modelStr)
		}
	}()

	session.Status = "running"
	// Update DB status to running
	model.DB.Model(&model.AgentSession{}).Where("id = ?", session.ID).Update("status", "running")

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

	// 2. Send message to the serve session (async — serve returns empty body)
	err = s.sendServeMessageAsync(ocSessionID, message, agentName, modelStr, "")
	if err != nil {
		log.Printf("[OpenCode] Failed to send message to serve session %s: %v", ocSessionID, err)
		session.Status = "failed"
		return
	}

	// 3. Poll for assistant response (serve API is async)
	msgResp, err := s.pollServeResponse(ocSessionID, 120) // max 120s wait
	if err != nil {
		log.Printf("[OpenCode] Failed to poll response from serve session %s: %v", ocSessionID, err)
		session.Status = "failed"
		return
	}

	// 4. Process the response parts — only extract text; tool calls are handled by processServeToolCalls
	var textParts []string
	for _, part := range msgResp.Parts {
		if part.Type == "text" {
			textParts = append(textParts, part.Text)
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

	// Send message (async API returns empty body)
	err := s.sendServeMessageAsync(ocSessionID, message, agentName, model, nr)
	if err != nil {
		return nil, err
	}

	// If noReply, don't wait for response
	if nr == "true" {
		return &serveMessageResponse{}, nil
	}

	// Poll for assistant response
	return s.pollServeResponse(ocSessionID, 120)
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

// sendServeMessageAsync sends a message to the serve session without expecting a synchronous response.
// OpenCode serve's message API is async — it returns 200 with empty body.
func (s *Scheduler) sendServeMessageAsync(ocSessionID string, message string, agentName string, model string, noReplyFlag string) error {
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
		parts := strings.SplitN(model, "/", 2)
		if len(parts) == 2 {
			body["model"] = map[string]string{
				"providerID": parts[0],
				"modelID":    parts[1],
			}
		} else {
			body["model"] = model
		}
	}
	if noReplyFlag == "true" {
		body["noReply"] = true
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal message body: %w", err)
	}

	resp, err := s.httpClient.Post(url, "application/json", bytes.NewReader(bodyJSON))
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("serve returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Drain body (serve returns empty, but we must read it)
	io.ReadAll(resp.Body)
	return nil
}

// pollServeResponse polls the serve session's messages endpoint until an assistant response appears.
func (s *Scheduler) pollServeResponse(ocSessionID string, maxWaitSec int) (*serveMessageResponse, error) {
	url := fmt.Sprintf("%s/session/%s/message?limit=20", s.pureServeURL, ocSessionID)

	// Initial wait before first poll — give serve time to process the message
	time.Sleep(3 * time.Second)

	deadline := time.Now().Add(time.Duration(maxWaitSec) * time.Second)
	for time.Now().Before(deadline) {
		resp, err := s.httpClient.Get(url)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		var messages []struct {
			Info struct {
				Role string `json:"role"`
			} `json:"info"`
			Parts []servePart `json:"parts"`
		}
		if err := json.Unmarshal(body, &messages); err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		// Look for assistant message with parts
		for i := len(messages) - 1; i >= 0; i-- {
			msg := messages[i]
			if msg.Info.Role == "assistant" && len(msg.Parts) > 0 {
				result := &serveMessageResponse{Parts: msg.Parts}
				log.Printf("[OpenCode] pollServeResponse: found assistant response with %d parts", len(msg.Parts))
				return result, nil
			}
		}

		// No assistant response yet, wait and retry
		time.Sleep(3 * time.Second)
	}

	return nil, fmt.Errorf("timed out waiting for assistant response after %ds", maxWaitSec)
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
		// Parse "providerID/modelID" format into object that OpenCode serve expects
		parts := strings.SplitN(model, "/", 2)
		if len(parts) == 2 {
			body["model"] = map[string]string{
				"providerID": parts[0],
				"modelID":    parts[1],
			}
		} else {
			body["model"] = model
		}
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

	log.Printf("[OpenCode] processServeToolCalls: fetched %d messages for session %s", len(messages), ocSessionID)

	// Look for tool-invocation parts in assistant messages
	for _, msg := range messages {
		if msg.Info.Role != "assistant" {
			continue
		}
		for _, part := range msg.Parts {
			// Debug: dump raw part JSON to see actual structure
			partJSON, _ := json.Marshal(part)
			log.Printf("[OpenCode] Part raw JSON: %s", string(partJSON))

			if (part.Type == "tool-invocation" || part.Type == "tool") && part.Tool != "" {
				var inputRaw json.RawMessage

				// Try 1: Get input from state.input
				if part.State != nil && len(part.State.Input) > 0 {
					inputRaw = part.State.Input
					log.Printf("[OpenCode] Tool %s input from state.input: %s", part.Tool, string(inputRaw))
				} else if part.State != nil {
					// Try 2: State exists but input is empty - dump full state for debugging
					stateJSON, _ := json.Marshal(part.State)
					log.Printf("[OpenCode] Tool %s state exists but input empty, full state: %s", part.Tool, string(stateJSON))
				}

				// Try 3: Check metadata for input parameters
				if len(inputRaw) == 0 && len(part.Metadata) > 0 {
					log.Printf("[OpenCode] Tool %s checking metadata for input: %s", part.Tool, string(part.Metadata))
					var meta map[string]interface{}
					if err := json.Unmarshal(part.Metadata, &meta); err == nil {
						if argsVal, ok := meta["args"]; ok {
							if argsJSON, err := json.Marshal(argsVal); err == nil {
								inputRaw = argsJSON
								log.Printf("[OpenCode] Tool %s input from metadata.args: %s", part.Tool, string(inputRaw))
							}
						} else if inputVal, ok := meta["input"]; ok {
							if inputJSON, err := json.Marshal(inputVal); err == nil {
								inputRaw = inputJSON
								log.Printf("[OpenCode] Tool %s input from metadata.input: %s", part.Tool, string(inputRaw))
							}
						}
					}
				}

				// Try 4: If we have callID, fetch tool details from serve API
				if len(inputRaw) == 0 && part.CallID != "" {
					log.Printf("[OpenCode] Tool %s has callID=%s, attempting to fetch tool details", part.Tool, part.CallID)
					if fetched, err := s.fetchToolCallInput(ocSessionID, part.CallID); err == nil && len(fetched) > 0 {
						inputRaw = fetched
						log.Printf("[OpenCode] Tool %s input from callID fetch: %s", part.Tool, string(inputRaw))
					}
				}

				if len(inputRaw) == 0 {
					inputRaw = json.RawMessage("{}")
					log.Printf("[OpenCode] Tool %s all input sources exhausted, using empty", part.Tool)
				}

				s.processToolCall(session, part.Tool, inputRaw, "")
			}
		}
	}
}

// fetchToolCallInput attempts to fetch tool call input from the serve API using callID
func (s *Scheduler) fetchToolCallInput(ocSessionID string, callID string) (json.RawMessage, error) {
	// Try the tool endpoint: GET /session/{sessionID}/tool/{callID}
	url := fmt.Sprintf("%s/session/%s/tool/%s", s.pureServeURL, ocSessionID, callID)
	resp, err := s.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tool call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tool call API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read tool call response: %w", err)
	}

	log.Printf("[OpenCode] fetchToolCallInput raw response: %s", string(body))

	// Parse the response - it should contain the tool part with state.input
	var toolPart servePart
	if err := json.Unmarshal(body, &toolPart); err != nil {
		// Try as a wrapper object
		var wrapper struct {
			Part servePart `json:"part"`
		}
		if err2 := json.Unmarshal(body, &wrapper); err2 != nil {
			return nil, fmt.Errorf("failed to parse tool call response: %w (raw: %s)", err, truncate(string(body), 200))
		}
		toolPart = wrapper.Part
	}

	if toolPart.State != nil && len(toolPart.State.Input) > 0 {
		return toolPart.State.Input, nil
	}

	return nil, fmt.Errorf("tool part has no state.input")
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
		"evaluate_output":  true,
		"merge_output":     true,
		"biz_review_output": true,
		"approve_pr":       true,
		"reject_pr":        true,
		"switch_milestone": true,
		"create_policy":    true,
		"chief_output":     true,
		"analyze_output":  true,
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

	// Record ToolCallTrace
	argsJSON, _ := json.Marshal(args)
	if len(argsJSON) > 2000 {
		argsJSON = argsJSON[:2000] // truncate large args
	}
	trace := &model.ToolCallTrace{
		ID:        model.GenerateID("tct"),
		SessionID: session.ID,
		ProjectID: session.ProjectID,
		ToolName:  toolName,
		Args:      string(argsJSON),
		Success:   true, // if handler didn't error, assume success
		CreatedAt: time.Now(),
	}
	go func() {
		if err := model.DB.Create(trace).Error; err != nil {
			log.Printf("[Trace] Failed to record ToolCallTrace for %s: %v", toolName, err)
		}
	}()

	// Broadcast tool call to frontend in real-time
	if BroadcastHandler != nil {
		BroadcastHandler(session.ProjectID, "TOOL_CALL", map[string]interface{}{
			"session_id": session.ID,
			"tool":       toolName,
			"args":       args,
		})
	}
}

// retryPolicy defines max retries and backoff per role
type retryPolicy struct {
	MaxRetries int
	BackoffSec int
}

var retryPolicies = map[agent.Role]retryPolicy{
	agent.RoleEvaluate: {MaxRetries: 2, BackoffSec: 30},
	agent.RoleMerge:   {MaxRetries: 1, BackoffSec: 15},
	agent.RoleAudit1:  {MaxRetries: 1, BackoffSec: 20},
	agent.RoleFix:     {MaxRetries: 1, BackoffSec: 20},
	agent.RoleChief:   {MaxRetries: 1, BackoffSec: 10},
	agent.RoleAnalyze: {MaxRetries: 1, BackoffSec: 30},
	// Maintain: no retry, next periodic trigger will re-run
	// Consult/Assess/Audit2: no retry
}

// maybeRetry checks if a failed session should be retried and re-dispatches if eligible.
func (s *Scheduler) maybeRetry(session *agent.Session, message string, agentName string, modelStr string) {
	policy, ok := retryPolicies[session.Role]
	if !ok {
		return
	}

	// Read current retry count from DB
	var dbSession model.AgentSession
	if model.DB.Where("id = ?", session.ID).First(&dbSession).Error != nil {
		return
	}

	if dbSession.RetryCount >= policy.MaxRetries {
		log.Printf("[Retry] Session %s (role=%s) max retries reached (%d/%d)", session.ID, session.Role, dbSession.RetryCount, policy.MaxRetries)
		return
	}

	newRetryCount := dbSession.RetryCount + 1
	log.Printf("[Retry] Session %s (role=%s) failed, retrying %d/%d in %ds", session.ID, session.Role, newRetryCount, policy.MaxRetries, policy.BackoffSec)

	// Update retry count and last error in DB
	model.DB.Model(&model.AgentSession{}).Where("id = ?", session.ID).Updates(map[string]interface{}{
		"retry_count": newRetryCount,
		"last_error":  "session failed, retry " + fmt.Sprintf("%d/%d", newRetryCount, policy.MaxRetries),
	})

	go func() {
		time.Sleep(time.Duration(policy.BackoffSec) * time.Second)

		// Create a new session for the retry (fresh state)
		retrySession := &agent.Session{
			ID:            session.ID, // reuse same ID to keep DB record linked
			Role:          session.Role,
			ProjectID:     session.ProjectID,
			ChangeID:      session.ChangeID,
			PRID:          session.PRID,
			TriggerReason: session.TriggerReason,
			Context:       session.Context,
			Status:        "pending",
		}
		agent.DefaultManager.RegisterSession(retrySession)

		// Update DB status back to pending
		model.DB.Model(&model.AgentSession{}).Where("id = ?", session.ID).Updates(map[string]interface{}{
			"status":       "pending",
			"completed_at": nil,
		})

		s.runAgentViaServe(retrySession, message, agentName, modelStr)
	}()
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

// applyPoliciesToSession matches and applies active policies to a session before dispatch.
func (s *Scheduler) applyPoliciesToSession(session *agent.Session) {
	var policies []model.Policy
	model.DB.Where("status = ?", "active").Order("priority DESC").Find(&policies)

	for i := range policies {
		p := &policies[i]

		var mc map[string]interface{}
		if err := json.Unmarshal([]byte(p.MatchCondition), &mc); err != nil {
			continue
		}

		// Check role match
		if reqRole, ok := mc["role"].(string); ok && reqRole != "" && string(session.Role) != reqRole {
			continue
		}

		// Check tag match
		if reqTags, ok := mc["tags"].([]interface{}); ok && len(reqTags) > 0 {
			var taskTags []model.TaskTag
			model.DB.Where("task_id = ?", session.ChangeID).Find(&taskTags)
			tagSet := make(map[string]bool)
			for _, t := range taskTags {
				tagSet[t.Tag] = true
			}
			matched := false
			for _, rt := range reqTags {
				if s, ok := rt.(string); ok && tagSet[s] {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		// Policy matches — apply actions
		var actions map[string]interface{}
		if err := json.Unmarshal([]byte(p.Actions), &actions); err != nil {
			continue
		}

		if gp, ok := actions["guard_prompt"].(string); ok && gp != "" {
			session.Context.InputContent += "\n\n[Policy Guard]: " + gp
		}
		if reqCtx, ok := actions["require_context"].(string); ok && reqCtx != "" {
			session.Context.InputContent += "\n\n[Required Context]: You must read the following files first: " + reqCtx
		}
		if maxFiles, ok := actions["max_file_changes"].(float64); ok {
			session.Context.InputContent += fmt.Sprintf("\n\n[Policy Constraint]: Maximum file changes allowed: %d", int(maxFiles))
		}

		// Increment hit count atomically
		model.DB.Model(&model.Policy{}).Where("id = ?", p.ID).Update("hit_count", gorm.Expr("hit_count + 1"))

		log.Printf("[PolicyEngine] Applied policy %s to session %s", p.Name, session.ID)
	}
}
