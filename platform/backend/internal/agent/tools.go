package agent

// Platform tool definitions.
//
// Descriptions follow the Claude Code / Anthropic prompt.ts pattern:
// every field explains not just *what* to pass but *when*, *why*, and
// *what not to do*. Examples give the model a complete, correct
// invocation to pattern-match against. This matters most for smaller
// models (MiniMax-M2, Haiku, 7B-class local models) where a richer
// schema is the difference between a reliable tool call and a 60%
// failure rate.
//
// Schema fields:
//   Description       — 2-4 sentences; what the tool does + the single
//                       most common failure mode.
//   Parameters        — per-field description includes allowed values,
//                       format, and the "common mistake" to avoid.
//   Examples          — full working invocations the LLM can imitate.
//                       Pass through to JSON Schema `examples` so
//                       providers that support it (Anthropic, some
//                       OpenAI-compat gateways) surface them to the
//                       model at inference time.
//   ErrorGuidance     — optional appended block describing what to do
//                       when the tool might error out (wrong args,
//                       missing prerequisites, etc.). Concatenated
//                       into the final Description served to the LLM.

type ToolDefinition struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Parameters    []ToolParam     `json:"parameters"`
	Examples      []map[string]any `json:"examples,omitempty"`
	ErrorGuidance string          `json:"error_guidance,omitempty"`
	RoleAccess    []Role          `json:"role_access"`
}

type ToolParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

var PlatformTools = map[string]*ToolDefinition{
	"audit_output": {
		Name: "audit_output",
		Description: `Emit the final audit verdict for the code change you were assigned. Call this EXACTLY ONCE at the end of your analysis. Do NOT emit plain-text opinions — the verdict only reaches the platform through this tool call.`,
		Parameters: []ToolParam{
			{Name: "level", Type: "string", Required: true,
				Description: `Audit verdict, exactly one of:
  - "L0" — no issues, change is safe to merge as-is.
  - "L1" — small fixable problems (naming, missing null check, off-by-one). Always provide 'issues'.
  - "L2" — fundamental problems that require rejection (wrong approach, breaks invariants). Provide 'reject_reason'.
Pick the most severe level that fits. Never return lowercase "l0" or numeric 0.`},
			{Name: "issues", Type: "array", Required: false,
				Description: `Required for L1 and L2, omitted for L0. Each element is {file, line, type, detail, status}.
'type' is one of: "bug", "style", "missing_test", "naming", "performance", "security".
'status' is always "open" on first emission — fix agent flips it later.
Use absolute repo-relative paths (e.g. "src/auth.go", not "/abs/src/auth.go").`},
			{Name: "reject_reason", Type: "string", Required: false,
				Description: `Required for L2, omitted otherwise. One sentence explaining why the change cannot be salvaged. Example: "Replaces the entire auth flow without addressing the token-refresh deadlock that caused the bug."`},
			{Name: "pattern_observed", Type: "string", Required: false,
				Description: `Optional: a generalised lesson from this change worth capturing as an Experience (feeds into the skill library). One sentence, imperative mood. Example: "JWT middleware must always forward the X-Request-ID header."`},
			{Name: "suggestion_for_submitter", Type: "string", Required: false,
				Description: `Optional: a specific, actionable follow-up the submitter should consider even if the change itself passed. Kept brief.`},
		},
		Examples: []map[string]any{
			{"level": "L0"},
			{
				"level": "L1",
				"issues": []map[string]any{
					{"file": "src/login.go", "line": 42, "type": "bug",
						"detail": "Missing nil-check on user lookup — will panic if the DB returns ErrNoRows.",
						"status": "open"},
				},
			},
			{
				"level":         "L2",
				"reject_reason": "The patch removes the retry logic that was the whole point of the parent task.",
			},
		},
		ErrorGuidance: `If you cannot reach a verdict (e.g. the diff is empty or unrelated to the task), return level="L2" with reject_reason explaining the mismatch. Never return without calling this tool.`,
		RoleAccess:    []Role{RoleAudit1},
	},

	"fix_output": {
		Name: "fix_output",
		Description: `Emit the fix agent's verdict after attempting to resolve the L1 issues from audit_1. Call EXACTLY ONCE. Three legitimate outcomes: issues fixed in code, delegate back to audit_2 when you disagree with audit_1's verdict, or reject when the fix is infeasible.`,
		Parameters: []ToolParam{
			{Name: "action", Type: "string", Required: true,
				Description: `Exactly one of:
  - "fix"      — you edited files and the issues are resolved.
  - "delegate" — you believe audit_1 was wrong; hand off to audit_2 for a second opinion.
  - "reject"   — the fix cannot be applied (e.g. would require refactoring beyond scope).`},
			{Name: "fixed", Type: "boolean", Required: false,
				Description: `Required when action="fix". True means every L1 issue has been addressed; false is invalid here — if you can't fix them, use action="reject" instead.`},
			{Name: "delegate_to", Type: "string", Required: false,
				Description: `Required when action="delegate". Must be "audit_agent_2" — that's the only valid delegate target today.`},
			{Name: "reject_reason", Type: "string", Required: false,
				Description: `Required when action="reject". One sentence explaining why fixing is infeasible.`},
			{Name: "fix_strategy", Type: "string", Required: false,
				Description: `Optional: how you approached the fix, captured as an Experience for future similar bugs. Imperative mood, one sentence. Example: "Replace naked error returns with wrapped fmt.Errorf when error source is ambiguous."`},
			{Name: "false_positive", Type: "boolean", Required: false,
				Description: `Optional: set true if you believe audit_1 flagged something that wasn't actually a bug. Implies action="delegate".`},
		},
		Examples: []map[string]any{
			{"action": "fix", "fixed": true, "fix_strategy": "Added defer rows.Close() on every database/sql query in the auth package."},
			{"action": "delegate", "delegate_to": "audit_agent_2", "false_positive": true},
			{"action": "reject", "reject_reason": "Fix would require redesigning the entire session store; out of scope for this change."},
		},
		ErrorGuidance: `Do not call this tool before running 'edit' to actually modify files (when action="fix"). The platform cross-checks your file-edit journal against the claim — a "fix" action with zero edits is rejected.`,
		RoleAccess:    []Role{RoleFix},
	},

	"audit2_output": {
		Name: "audit2_output",
		Description: `Emit audit_2's final decision after a fix_output delegation. This is the last word on the change — no further appeals.`,
		Parameters: []ToolParam{
			{Name: "result", Type: "string", Required: true,
				Description: `Exactly one of "merge" or "reject". "merge" approves for immediate integration; "reject" sends the change back to the submitter with the reason.`},
			{Name: "reject_reason", Type: "string", Required: false,
				Description: `Required when result="reject". One sentence explaining the decision.`},
		},
		Examples: []map[string]any{
			{"result": "merge"},
			{"result": "reject", "reject_reason": "Even after fixes, the change still widens the auth scope beyond what the task described."},
		},
		RoleAccess: []Role{RoleAudit2},
	},

	"create_task": {
		Name: "create_task",
		Description: `Add a new task to the current milestone. Use when decomposing a milestone into concrete work items, or when a discovered issue warrants its own task. Do not use for ad-hoc notes — the task list is the authoritative work queue.`,
		Parameters: []ToolParam{
			{Name: "name", Type: "string", Required: true,
				Description: `Short imperative title, <= 60 chars. Example: "Add retry on 429 responses". Avoid filler words ("Fix the bug"), avoid file paths in the title.`},
			{Name: "description", Type: "string", Required: false,
				Description: `2-5 sentences describing context and acceptance criteria. Link to prior change IDs or issue numbers when relevant.`},
			{Name: "priority", Type: "string", Required: false,
				Description: `"high" | "medium" | "low". Default "medium". Reserve "high" for tasks blocking the current milestone.`},
		},
		Examples: []map[string]any{
			{"name": "Cache bge embeddings by task hash",
				"description": "Refinery recomputes embeddings on every pass; we should memoise by SHA256(task.Description) to avoid the 3s/task sidecar hit.",
				"priority":    "medium"},
		},
		RoleAccess: []Role{RoleMaintain},
	},

	"delete_task": {
		Name: "delete_task",
		Description: `Remove a task from the milestone. Only for tasks that are demonstrably obsolete (duplicate, scoped-out, or replaced by a better-formed task). Completed tasks are auto-archived — do not delete them.`,
		Parameters: []ToolParam{
			{Name: "task_id", Type: "string", Required: true,
				Description: `The exact task ID (e.g. "task_a1b2c3d4"), not the task name. Check the task_list you were given in the prompt.`},
		},
		Examples: []map[string]any{
			{"task_id": "task_a1b2c3d4"},
		},
		ErrorGuidance: `Will error if the task is currently claimed by an agent — ask for human confirmation first in those cases.`,
		RoleAccess:    []Role{RoleMaintain},
	},

	"update_milestone": {
		Name: "update_milestone",
		Description: `Update the existing milestone block in place. Use to add progress notes, refine scope, or fold in confirmed direction changes. For a fresh milestone, use write_milestone instead.`,
		Parameters: []ToolParam{
			{Name: "content", Type: "string", Required: true,
				Description: `The full replacement milestone body in Markdown. Must start with the '## Milestone' heading that the template expects. Do not embed front-matter or YAML blocks.`},
		},
		Examples: []map[string]any{
			{"content": "## Milestone: Auth hardening\n\n**Goal**: Reduce unauthorised access by 80%.\n\n**Status**: 3 / 8 tasks complete — MFA + rate-limit shipped, audit logging next."},
		},
		RoleAccess: []Role{RoleMaintain},
	},

	"propose_direction": {
		Name: "propose_direction",
		Description: `Propose a new direction statement for the project. This DOES NOT take effect immediately — the human PM must confirm in the dashboard chat before it's applied. Use when the current direction is visibly stale or the roadmap has changed materially.`,
		Parameters: []ToolParam{
			{Name: "content", Type: "string", Required: true,
				Description: `The proposed direction body (Markdown). Keep it short — 3-6 sentences capturing the north star, not a full plan. The human sees this verbatim with accept / reject buttons.`},
		},
		Examples: []map[string]any{
			{"content": "Focus: make the self-evolution loop the primary product differentiator.\n\nNext quarter we concentrate engineering on the refinery pipeline (skill extraction + ranking) and deprecate the manual-triage workflow."},
		},
		ErrorGuidance: `If the human rejects your proposal, do not re-submit the same text in the next session; propose something materially different or leave the direction alone.`,
		RoleAccess:    []Role{RoleMaintain},
	},

	"write_milestone": {
		Name: "write_milestone",
		Description: `Create a brand-new milestone block, replacing whatever is currently there. Use only when starting a new milestone phase (prior milestone archived, new goals identified). For iterative edits to the existing milestone, use update_milestone.`,
		Parameters: []ToolParam{
			{Name: "content", Type: "string", Required: true,
				Description: `Full milestone body in Markdown, starting with '## Milestone: <title>'. Include Goal, Status, and a checklist of tasks. Omit front-matter.`},
		},
		Examples: []map[string]any{
			{"content": "## Milestone: Self-evolution pipeline v2\n\n**Goal**: Refinery runs reach p95 <10s per session.\n\n**Tasks**:\n- [ ] Cache embeddings by task hash\n- [ ] Parallelise refinery passes\n- [ ] Add lifecycle dashboard"},
		},
		RoleAccess: []Role{RoleMaintain},
	},

	"assess_output": {
		Name: "assess_output",
		Description: `Emit the project-structure assessment document (ASSESS_DOC.md). Runs at project init or on explicit human request — not a per-change tool.`,
		Parameters: []ToolParam{
			{Name: "content", Type: "string", Required: true,
				Description: `Full Markdown document following the ASSESS_DOC template: sections for Architecture, Key Modules, Risks, and Suggested First Milestone. Aim for 500-1500 words.`},
		},
		Examples: []map[string]any{
			{"content": "# Project Assessment\n\n## Architecture\n...\n\n## Key Modules\n...\n\n## Risks\n...\n\n## Suggested First Milestone\n..."},
		},
		RoleAccess: []Role{RoleAssess},
	},
}

func GetToolsForRole(role Role) []*ToolDefinition {
	tools := make([]*ToolDefinition, 0)
	for _, tool := range PlatformTools {
		for _, r := range tool.RoleAccess {
			if r == role {
				tools = append(tools, tool)
				break
			}
		}
	}
	return tools
}