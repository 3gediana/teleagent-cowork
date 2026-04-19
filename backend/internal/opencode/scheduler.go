package opencode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/service"
)

type Scheduler struct {
	cfg config.OpenCodeConfig
}

var DefaultScheduler *Scheduler

func InitScheduler(cfg config.OpenCodeConfig) {
	DefaultScheduler = &Scheduler{
		cfg: cfg,
	}
	log.Printf("[OpenCode] Scheduler initialized with serve_url=%s, model=%s/%s", cfg.ServeURL, cfg.DefaultModelProvider, cfg.DefaultModelID)
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
	if ctx.InputContent != "" {
		contextParts = append(contextParts, "## Input\n"+ctx.InputContent)
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

	fullMessage := prompt
	if len(contextParts) > 0 {
		fullMessage = strings.Join(contextParts, "\n\n") + "\n\n" + prompt
	}

	agentName := string(session.Role)
	model := fmt.Sprintf("%s/%s", s.cfg.DefaultModelProvider, s.cfg.DefaultModelID)
	if s.cfg.DefaultModelProvider == "" {
		model = "minimax-coding-plan/MiniMax-M2.7"
	}

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

	projectDir := s.cfg.ProjectPath
	if projectDir == "" {
		projectDir = "."
	}

	args := []string{
		"run",
		"--attach", s.cfg.ServeURL,
		"--agent", agentName,
		"--model", model,
		"--format", "json",
		message,
	}

	cmd := exec.Command("opencode", args...)
	cmd.Dir = projectDir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[OpenCode] Running: opencode %s", strings.Join(args[:len(args)-1], " ")+" [message...]")

	err := cmd.Run()
	if err != nil {
		log.Printf("[OpenCode] Session %s stderr: %s", session.ID, stderr.String())
	}

	session.Output = stdout.String()
	session.Status = "completed"
	log.Printf("[OpenCode] Session %s completed, output length=%d", session.ID, len(session.Output))

	s.processJSONOutput(session)
}

func (s *Scheduler) processJSONOutput(session *agent.Session) {
	output := session.Output
	if output == "" {
		return
	}

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
	log.Printf("[OpenCode] Session %s: tool_call=%s input=%s", session.ID, toolName, string(inputRaw))

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
