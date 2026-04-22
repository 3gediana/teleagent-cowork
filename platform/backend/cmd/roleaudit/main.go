// Command roleaudit — exercises every role that currently has
// platform tools against a REAL LLM provider, then prints the full
// transcript for human grading. Designed for the question:
// "do the agents actually use their tools correctly?"
//
// Scenarios (5):
//   A. audit_1 on a clean diff          — expect  L0
//   B. audit_1 on a diff with a nil-deref — expect  L1 (+ issues)
//   C. fix on audit_1's L1 finding      — expect  action="fix"
//   D. audit_2 on a fix delegation       — expect  result="merge" or "reject"
//   E. maintain adds a new task          — expect  create_task call
//
// For each scenario we print:
//   - scenario intent (what we asked)
//   - captured tool calls (name + args)
//   - session.Output (model thinking + prose)
//   - duration + tokens + cost
//
// Reads MiniMax creds from configs/config.yaml (llm.minimax.*).

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/llm"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/runner"
	"github.com/a3c/platform/internal/service"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type scenario struct {
	ID            string       // human label
	Role          agent.Role
	SystemPrompt  string       // override — we don't use BuildPrompt here to keep the diff focused
	UserInput     string       // the task brief the model sees
	ExpectedTool  string       // which platform tool the model should call
	PreHook       func(sessionID, changeID, projectID string) // setup extra DB state
	WantedVerdict string       // a string to look for in tool args, or "" to skip content check
}

func main() {
	log.SetFlags(0)

	// ---- bootstrap ----------------------------------------------------
	cfg := config.Load("")
	if cfg.LLM.MiniMax.APIKey == "" {
		log.Fatalf("llm.minimax.api_key is empty; paste into configs/config.yaml and retry")
	}

	db, err := gorm.Open(sqlite.Open("file:roleaudit?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Project{}, &model.Agent{}, &model.Task{}, &model.Milestone{},
		&model.AgentSession{}, &model.ToolCallTrace{},
		&model.Change{}, &model.RoleOverride{},
		&model.LLMEndpoint{},
	); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	model.DB = db
	// Set data dir so Chief+Milestone writes don't explode on missing
	// directories (maintain tools touch the filesystem).
	service.InitDataPath(os.TempDir())

	projectID := "proj_roleaudit"
	humanID := "agent_human_ra"
	db.Create(&model.Project{ID: projectID, Name: "RoleAudit", Status: "ready"})
	accessKey := "hk_ra"
	db.Create(&model.Agent{ID: humanID, Name: "human", Status: "online",
		CurrentProjectID: &projectID, AccessKey: accessKey, IsHuman: true})
	// Seed one existing milestone + task so maintain has something to mutate.
	db.Create(&model.Milestone{
		ID: "ms_seed", ProjectID: projectID, Name: "v0.1 baseline",
		Description: "## Milestone: v0.1\n- [x] bootstrap\n- [ ] ship auth",
		Status:      "in_progress",
		CreatedBy:   humanID,
		CreatedAt:   time.Now(),
	})

	// ---- register real MiniMax endpoint + override all roles ----------
	endpointID := model.GenerateID("llm")
	db.Create(&model.LLMEndpoint{
		ID:           endpointID,
		Name:         "minimax-live",
		Format:       "openai",
		BaseURL:      cfg.LLM.MiniMax.BaseURL,
		APIKey:       cfg.LLM.MiniMax.APIKey,
		Models:       fmt.Sprintf(`[{"id":%q}]`, cfg.LLM.MiniMax.Model),
		DefaultModel: cfg.LLM.MiniMax.Model,
		Status:       "active",
		CreatedBy:    humanID,
	})
	if err := llm.LoadEndpoint(endpointID); err != nil {
		log.Fatalf("load endpoint: %v", err)
	}
	for _, r := range []agent.Role{agent.RoleAudit1, agent.RoleAudit2, agent.RoleFix, agent.RoleMaintain, agent.RoleAssess} {
		if err := agent.SetRoleOverride(r, endpointID, cfg.LLM.MiniMax.Model); err != nil {
			log.Fatalf("set role override %s: %v", r, err)
		}
	}

	runner.NativeRegistryBuilder = runner.PlatformRegistryBuilder
	runner.PlatformToolSink = service.HandleToolCallResult
	rec := newRecorder()
	runner.StreamEmitter = rec.emit

	// ---- scenarios ----------------------------------------------------
	scenarios := []scenario{
		scenarioACleanAudit(),
		scenarioBBuggyAudit(),
		scenarioCFix(),
		scenarioDAudit2(),
		scenarioEMaintain(),
	}

	var summary []scenarioResult
	for _, sc := range scenarios {
		res := runScenario(db, projectID, humanID, sc, rec)
		summary = append(summary, res)
	}

	// ---- grade --------------------------------------------------------
	fmt.Println(strings.Repeat("═", 72))
	fmt.Println("  FINAL GRADE")
	fmt.Println(strings.Repeat("═", 72))
	pass := 0
	for _, r := range summary {
		mark := "✗"
		if r.ToolCalledCorrectly && r.ArgsLookValid {
			mark = "✓"
			pass++
		}
		fmt.Printf("  %s %-24s tool=%s args_ok=%v duration=%s  cost=$%.4f\n",
			mark, r.ID, r.ActualTool, r.ArgsLookValid, r.Duration.Truncate(time.Millisecond), r.Cost)
	}
	fmt.Printf("\n  %d / %d scenarios produced correct tool calls.\n", pass, len(summary))
}

// ---- scenario builders ---------------------------------------------

func scenarioACleanAudit() scenario {
	return scenario{
		ID:   "A_audit1_clean",
		Role: agent.RoleAudit1,
		SystemPrompt: `You are the audit_1 agent. Your sole job is to review one code change
and emit EXACTLY ONE audit_output tool call with the verdict (L0/L1/L2).
Do not emit plain text opinions — only the tool call counts.

L0 = clean, safe to merge.  L1 = fixable issues within the changed files.  L2 = systemic issue, reject.`,
		UserInput: `Change under review:
  Task: Add a debug log when the auth session starts.
  Modified files: src/auth/session.go
  Diff:
    @@ -42,6 +42,7 @@ func (s *Service) StartSession(ctx context.Context, user *User) (*Session, error) {
         if user == nil {
             return nil, ErrNilUser
         }
    +    log.Printf("[auth] starting session for user=%s", user.ID)
         sess := &Session{
             ID:     model.GenerateID("sess"),
             UserID: user.ID,

This is a trivial, single-line logging addition. Emit audit_output with level="L0".`,
		ExpectedTool:  "audit_output",
		WantedVerdict: "L0",
	}
}

func scenarioBBuggyAudit() scenario {
	return scenario{
		ID:   "B_audit1_l1bug",
		Role: agent.RoleAudit1,
		SystemPrompt: `You are the audit_1 agent. Review the change carefully and emit
EXACTLY ONE audit_output tool call.

L0 = clean, no problems.
L1 = fixable issues inside the changed files (missing null check, off-by-one, bad naming).
L2 = systemic issue (wrong approach, breaks another subsystem).`,
		UserInput: `Change under review:
  Task: Look up a user by email and log them in.
  Modified files: src/auth/login.go
  Diff:
    @@ -10,6 +10,14 @@ func (s *Service) LoginByEmail(ctx context.Context, email string) (*Session, error) {
    +    user, err := s.users.FindByEmail(ctx, email)
    +    if err != nil {
    +        return nil, err
    +    }
    +    // BUG: no nil check on user — FindByEmail returns (nil, nil) on ErrNoRows
    +    sess := &Session{UserID: user.ID}
    +    return sess, nil
    +}

There is an obvious bug: the code dereferences user.ID without first checking if user is nil,
which will panic when ErrNoRows is returned as (nil, nil) per the repo's convention.
Emit audit_output with level="L1" and an issues array describing the nil-deref.`,
		ExpectedTool:  "audit_output",
		WantedVerdict: "L1",
	}
}

func scenarioCFix() scenario {
	return scenario{
		ID:   "C_fix_addnilcheck",
		Role: agent.RoleFix,
		SystemPrompt: `You are the fix agent. You've been handed L1 issues from audit_1 and must decide
what to do. Three legitimate outcomes: action="fix" (you modified the files to resolve the issues),
action="delegate" (you disagree with audit_1), or action="reject" (infeasible).
Emit EXACTLY ONE fix_output call.`,
		UserInput: `Prior audit verdict: L1
  Issue: src/auth/login.go line 15 — missing nil-check on user before dereferencing user.ID.
  The FindByEmail function returns (nil, nil) on ErrNoRows per repo convention.

Pretend you have already read the file, added "if user == nil { return nil, ErrUserNotFound }"
between the err-check and the Session construction. The fix is a 2-line addition.
Emit fix_output with action="fix", fixed=true, and a short fix_strategy.`,
		ExpectedTool:  "fix_output",
		WantedVerdict: "fix",
	}
}

func scenarioDAudit2() scenario {
	return scenario{
		ID:   "D_audit2_final",
		Role: agent.RoleAudit2,
		SystemPrompt: `You are audit_2, the final reviewer. A fix agent delegated this change to you
because they disagreed with audit_1's verdict. Your decision is final — emit EXACTLY ONE
audit2_output call with result="merge" or result="reject".`,
		UserInput: `History on this change:
  - audit_1 flagged L1: "function name 'doStuff' is not descriptive".
  - fix agent delegated with false_positive=true, arguing:
      "The function is a private helper used only inside this file; the calling site
       makes its purpose obvious. Renaming would churn 5 callers for no clarity gain."

You agree with the fix agent — this is a nit, not a real issue.
Emit audit2_output with result="merge".`,
		ExpectedTool:  "audit2_output",
		WantedVerdict: "merge",
	}
}

func scenarioEMaintain() scenario {
	return scenario{
		ID:   "E_maintain_newtask",
		Role: agent.RoleMaintain,
		SystemPrompt: `You are the maintain agent (Chief's task manager). You can call create_task,
delete_task, update_milestone, write_milestone, or propose_direction. Pick the ONE tool
that best fits the instruction.`,
		UserInput: `The human PM noticed during code review that error messages across the auth package
are inconsistent (some use %v, some use %s). They want this tracked as a medium-priority
task. Use create_task to add it to the current milestone.`,
		ExpectedTool:  "create_task",
		WantedVerdict: "",
	}
}

// ---- per-scenario driver -------------------------------------------

type scenarioResult struct {
	ID                   string
	ExpectedTool         string
	ActualTool           string
	ToolCalledCorrectly  bool
	ArgsLookValid        bool
	ArgsSeen             json.RawMessage
	Duration             time.Duration
	Cost                 float64
	Usage                llm.Usage
	SessionOutput        string
	FailReason           string
}

func runScenario(db *gorm.DB, projectID, humanID string, sc scenario, rec *recorder) scenarioResult {
	fmt.Println(strings.Repeat("═", 72))
	fmt.Printf("  SCENARIO %s  (role=%s, expecting=%s)\n", sc.ID, sc.Role, sc.ExpectedTool)
	fmt.Println(strings.Repeat("═", 72))
	fmt.Println("  INPUT:")
	for _, line := range strings.Split(strings.TrimSpace(sc.UserInput), "\n") {
		fmt.Printf("    %s\n", line)
	}
	fmt.Println()

	rec.reset()

	// Install scenario's system prompt via the injectable builder.
	runner.NativePromptBuilder = func(sess *agent.Session) (string, string, error) {
		return sc.SystemPrompt, sc.UserInput, nil
	}

	// Fresh DB state: a pending change + a session row.
	changeID := model.GenerateID("change")
	db.Create(&model.Change{
		ID: changeID, ProjectID: projectID, AgentID: humanID,
		Status: "pending", CreatedAt: time.Now(),
	})
	sessionID := model.GenerateID("session")
	db.Create(&model.AgentSession{
		ID: sessionID, Role: string(sc.Role),
		ProjectID: projectID, ChangeID: changeID,
		Status: "pending", CreatedAt: time.Now(),
	})
	sess := &agent.Session{
		ID: sessionID, Role: sc.Role,
		ProjectID: projectID, ChangeID: changeID, Status: "pending",
		Context: &agent.SessionContext{
			ProjectPath:  os.TempDir(),
			InputContent: sc.UserInput,
			ChangeInfo:   &agent.ChangeContext{ChangeID: changeID},
		},
	}

	start := time.Now()
	err := runner.Dispatch(sess)
	elapsed := time.Since(start)

	res := scenarioResult{
		ID:            sc.ID,
		ExpectedTool:  sc.ExpectedTool,
		Duration:      elapsed,
		SessionOutput: sess.Output,
	}
	if err != nil {
		res.FailReason = err.Error()
		fmt.Printf("  ✗ dispatch error: %v\n\n", err)
		return res
	}

	// Pull the tool call from ToolCallTrace.
	var traces []model.ToolCallTrace
	db.Where("session_id = ?", sessionID).Find(&traces)
	if len(traces) == 0 {
		res.FailReason = "no tool_call_trace rows"
		fmt.Println("  ✗ no tool calls recorded (model failed to emit a tool call)")
		fmt.Printf("  output (for diagnosis):\n%s\n\n", indent(sess.Output))
		return res
	}
	tr := traces[0]
	res.ActualTool = tr.ToolName
	res.ArgsSeen = json.RawMessage(tr.Args)
	res.ToolCalledCorrectly = tr.ToolName == sc.ExpectedTool

	// Usage (grab from the AgentSession's InjectedArtifacts piggyback? No —
	// we logged it to stdout via the dispatcher). For this smoke we
	// approximate cost from the AGENT_DONE event we recorded.
	if done := rec.last(runner.EventAgentDone); done != nil {
		if c, ok := done["cost_usd"].(float64); ok {
			res.Cost = c
		}
		if it, ok := done["input_tokens"].(int); ok {
			res.Usage.InputTokens = it
		}
		if ot, ok := done["output_tokens"].(int); ok {
			res.Usage.OutputTokens = ot
		}
	}

	fmt.Printf("  TOOL CALL:  %s(%s)\n", tr.ToolName, shorten(tr.Args, 400))
	fmt.Printf("  DURATION:   %s\n", elapsed.Truncate(time.Millisecond))
	fmt.Printf("  USAGE:      %d in / %d out   COST: $%.5f\n",
		res.Usage.InputTokens, res.Usage.OutputTokens, res.Cost)
	fmt.Println("  OUTPUT:")
	fmt.Println(indent(res.SessionOutput))

	// Arg sanity: did the required keys land?
	res.ArgsLookValid = validateArgs(tr.ToolName, tr.Args, sc.WantedVerdict)
	if !res.ToolCalledCorrectly {
		fmt.Printf("  ✗ expected tool %q, model called %q\n", sc.ExpectedTool, tr.ToolName)
	} else if !res.ArgsLookValid {
		fmt.Printf("  ⚠ tool was correct but args look off for scenario (wanted %q)\n", sc.WantedVerdict)
	} else {
		fmt.Println("  ✓ tool + args LOOK correct")
	}
	fmt.Println()
	return res
}

// validateArgs does a shallow sanity check per tool. Enough to tell
// the difference between "model nailed it" and "model emitted the
// right tool with nonsense args". Not a strict JSON Schema validator.
func validateArgs(tool, rawArgs, wantedSubstring string) bool {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return false
	}
	switch tool {
	case "audit_output":
		lvl, _ := args["level"].(string)
		if lvl != "L0" && lvl != "L1" && lvl != "L2" {
			return false
		}
		if wantedSubstring != "" && lvl != wantedSubstring {
			return false
		}
		if lvl == "L1" {
			// Must include an issues array with at least one element.
			issues, _ := args["issues"].([]interface{})
			if len(issues) == 0 {
				return false
			}
		}
		if lvl == "L2" {
			if _, ok := args["reject_reason"].(string); !ok {
				return false
			}
		}
		return true
	case "fix_output":
		action, _ := args["action"].(string)
		if action != "fix" && action != "delegate" && action != "reject" {
			return false
		}
		if wantedSubstring != "" && action != wantedSubstring {
			return false
		}
		return true
	case "audit2_output":
		res, _ := args["result"].(string)
		if res != "merge" && res != "reject" {
			return false
		}
		if wantedSubstring != "" && res != wantedSubstring {
			return false
		}
		return true
	case "create_task":
		name, _ := args["name"].(string)
		return strings.TrimSpace(name) != ""
	}
	return true
}

// ---- recorder + formatting helpers --------------------------------

type recorder struct {
	mu     sync.Mutex
	events []recorderEv
}

type recorderEv struct {
	Type    string
	Payload map[string]interface{}
}

func newRecorder() *recorder { return &recorder{} }

func (r *recorder) emit(_, typ string, payload map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recorderEv{typ, payload})
}

func (r *recorder) last(typ string) map[string]interface{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Type == typ {
			return r.events[i].Payload
		}
	}
	return nil
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

func shorten(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func indent(s string) string {
	if s == "" {
		return "    (empty)"
	}
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
