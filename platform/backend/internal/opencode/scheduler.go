package opencode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/service"
)

type Scheduler struct {
	cfg          config.OpenCodeConfig
	pureServeURL string
	pureCmd      *exec.Cmd
}

var DefaultScheduler *Scheduler

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

func (s *Scheduler) Dispatch(session *agent.Session) error {
	roleConfig := agent.GetRoleConfig(session.Role)
	if roleConfig == nil {
		return fmt.Errorf("unknown role: %s", session.Role)
	}

	prompt, err := agent.BuildPrompt(session.Role, session.Context)
	if err != nil {
		return fmt.Errorf("failed to build prompt: %w", err)
	}

	var contextParts []string
	ctx := session.Context
	if ctx == nil {
		return fmt.Errorf("session context is nil")
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

	agentName := string(session.Role)
	model := fmt.Sprintf("%s/%s", s.cfg.DefaultModelProvider, s.cfg.DefaultModelID)
	if s.cfg.DefaultModelProvider == "" {
		model = "minimax-coding-plan/MiniMax-M2.7"
	}

	projectPath := filepath.Join(s.cfg.ProjectPath, "platform", "data", "projects", session.ProjectID)
	
	var fullMessage string
	fullMessage = fmt.Sprintf("## Project Data Location\nPath: %s\n\nRead these files to understand the project:\n- DIRECTION.md (project direction)\n- MILESTONE.md (current milestone)\n- TASKS.md (task list)\n\n", projectPath)
	
	if ctx.InputContent != "" {
		fullMessage += "## TASK TO COMPLETE\n" + ctx.InputContent + "\n\n"
	}
	
	if len(contextParts) > 0 {
		fullMessage += strings.Join(contextParts, "\n\n") + "\n\n"
	}
	
	fullMessage += prompt

	log.Printf("[OpenCode] Dispatching session %s: agent=%s, model=%s", session.ID, agentName, model)

	go s.runAgent(session, fullMessage, agentName, model)

	return nil
}

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

func (s *Scheduler) runAgent(session *agent.Session, message string, agentName string, model string) {
	defer func() {
		if session.Status == "running" {
			session.Status = "failed"
		}
	}()

	session.Status = "running"

	agentConfigDir := filepath.Join(s.cfg.ProjectPath, "platform", "backend", "agent-config")

	args := []string{
		"run",
		"--pure",
		"--agent", agentName,
		"--model", model,
		"--format", "json",
		"--dangerously-skip-permissions",
	}

	// Use stdin for message to avoid Windows stack overflow with long messages
	cmd := exec.Command("opencode", args...)
	cmd.Dir = agentConfigDir
	cmd.Stdin = bytes.NewReader([]byte(message + "\n"))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[OpenCode] Running: opencode run --pure --agent %s --model %s --format json (stdin message len=%d)", agentName, model, len(message))

	err := cmd.Run()
	
	if err != nil {
		log.Printf("[OpenCode] Session %s error: %v, stderr: %s", session.ID, err, stderr.String())
	}

	session.Output = stdout.String()
	session.Status = "completed"
	log.Printf("[OpenCode] Session %s completed, output length=%d, stderr length=%d", session.ID, len(session.Output), len(stderr.String()))
	if len(session.Output) > 0 {
		log.Printf("[OpenCode] Session %s FULL OUTPUT:\n%s", session.ID, session.Output)
	}

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

	service.HandleToolCallResult(session.ID, session.ChangeID, session.ProjectID, toolName, args)
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
