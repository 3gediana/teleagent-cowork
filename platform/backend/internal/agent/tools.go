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

	// ------------------------------------------------------------
	// Evaluate agent — PR technical evaluation
	// ------------------------------------------------------------
	"evaluate_output": {
		Name: "evaluate_output",
		Description: `Emit the technical evaluation verdict for the PR you were assigned. Call EXACTLY ONCE. Evaluate is the platform's primary technical decision-maker for PRs — your verdict drives whether the change advances to biz review, gets flagged for conflict resolution, or is bounced back for rework. Do NOT emit plain-text opinions; the platform only consumes the structured result below.`,
		Parameters: []ToolParam{
			{Name: "result", Type: "string", Required: true,
				Description: `Overall technical verdict, exactly one of:
  - "approved"   — diff is technically sound and merges cleanly; advance to biz review.
  - "needs_work" — code quality issues the submitter should fix (bugs, missing tests, style). Provide 'reason'.
  - "conflicts"  — merge-conflict files; auto-resolution not safe. Provide 'conflict_files'.
  - "high_risk"  — architecturally risky change (security, perf, invariants). Provide 'reason'. Chief escalates to human even in AutoMode.
Pick the most severe bucket that fits. Never return lowercase variants with whitespace.`},
			{Name: "merge_cost_rating", Type: "string", Required: true,
				Description: `"low" | "medium" | "high". Rubric:
  - low    : <10 files changed, no conflicts, straightforward merge.
  - medium : 10-30 files, or simple auto-resolvable conflicts.
  - high   : >30 files, or complex conflicts requiring manual resolution.`},
			{Name: "recommended_action", Type: "string", Required: true,
				Description: `What Chief should do with this PR next (consumed by AutoMode). Exactly one of:
  - "auto_advance"      — safe for the platform to auto-proceed (biz review → merge) without human touch.
  - "escalate_to_human" — needs a human call; AutoMode will NOT auto-advance.
  - "request_changes"   — send back to submitter; no further platform action until they re-submit.
Rule of thumb: recommend auto_advance ONLY when result="approved" AND merge_cost_rating != "high" AND you see zero security/architecture flags.`},
			{Name: "reason", Type: "string", Required: false,
				Description: `Required when result != "approved". One paragraph (<=3 sentences) explaining the verdict. Be specific — "this refactors the auth middleware without preserving the X-Request-ID forwarding" beats "concerns about auth".`},
			{Name: "conflict_files", Type: "array", Required: false,
				Description: `Required when result="conflicts". JSON array of repo-relative paths: ["src/auth.go", "internal/session/store.go"].`},
			{Name: "quality_patterns", Type: "string", Required: false,
				Description: `Optional: positive patterns worth capturing as Experience (good code you'd want repeated). One sentence. Example: "Wraps all DB errors with fmt.Errorf and %w to preserve the chain."`},
			{Name: "common_mistakes", Type: "string", Required: false,
				Description: `Optional: anti-patterns observed worth capturing as Experience. One sentence. Example: "Goroutines spawned without context propagation — they leak on request cancellation."`},
		},
		Examples: []map[string]any{
			{
				"result":             "approved",
				"merge_cost_rating":  "low",
				"recommended_action": "auto_advance",
				"quality_patterns":   "Table-driven tests with explicit subtests for every public API method.",
			},
			{
				"result":             "needs_work",
				"merge_cost_rating":  "low",
				"recommended_action": "request_changes",
				"reason":             "New HTTP handler does not validate the request body — a malformed JSON crashes the server.",
			},
			{
				"result":             "conflicts",
				"merge_cost_rating":  "medium",
				"recommended_action": "escalate_to_human",
				"conflict_files":     []string{"internal/auth/oauth.go", "internal/session/manager.go"},
			},
			{
				"result":             "high_risk",
				"merge_cost_rating":  "high",
				"recommended_action": "escalate_to_human",
				"reason":             "Rewrites the token-refresh flow; touches concurrency invariants that only a human should clear.",
			},
		},
		ErrorGuidance: `Never leave recommended_action blank — Chief's AutoMode path depends on it. If you cannot reach a verdict (e.g. diff empty or corrupted), return result="needs_work" with reason="diff unreadable" and recommended_action="request_changes".`,
		RoleAccess:    []Role{RoleEvaluate},
	},

	// ------------------------------------------------------------
	// Merge agent — git merge execution
	// ------------------------------------------------------------
	"merge_output": {
		Name: "merge_output",
		Description: `Emit the merge execution verdict after attempting to merge the PR branch into the main branch. Call EXACTLY ONCE. Three outcomes are legitimate: clean merge, conflict-resolved merge, or abort.`,
		Parameters: []ToolParam{
			{Name: "result", Type: "string", Required: true,
				Description: `Exactly one of:
  - "success"           — merged without conflicts.
  - "conflict_resolved" — had conflicts, resolved them with simple rules (whitespace, non-semantic), merged.
  - "failed"            — conflicts too complex or merge refused; aborted. Provide 'reason'.
Refuse to invent "partial" / "manual_needed" variants; use "failed" with a reason instead.`},
			{Name: "reason", Type: "string", Required: false,
				Description: `Required when result="failed". One sentence explaining why the merge cannot be completed programmatically. Example: "Conflict in auth.go spans overlapping semantic changes to the session refresh flow; requires human review."`},
			{Name: "resolved_files", Type: "array", Required: false,
				Description: `Required when result="conflict_resolved". List of files you resolved conflicts in, with the resolution strategy. Each element: {file, strategy}. Example: [{"file": "go.mod", "strategy": "take_both"}].`},
			{Name: "merge_commit_sha", Type: "string", Required: false,
				Description: `Optional: the SHA of the merge commit (full 40-char hex). Useful for audit trails; the platform records this against the PR.`},
		},
		Examples: []map[string]any{
			{"result": "success", "merge_commit_sha": "abc123def456..."},
			{
				"result": "conflict_resolved",
				"resolved_files": []map[string]any{
					{"file": "go.mod", "strategy": "take_both"},
					{"file": "README.md", "strategy": "concatenate"},
				},
			},
			{"result": "failed", "reason": "Conflict in internal/auth/session.go spans overlapping semantic rewrites; human required."},
		},
		ErrorGuidance: `Do NOT attempt to resolve conflicts in core business logic files (anything under internal/, src/, or app/) — return result="failed" for those. Whitespace, go.mod/go.sum, and top-level docs are the only safe auto-resolution targets.`,
		RoleAccess:    []Role{RoleMerge},
	},

	// ------------------------------------------------------------
	// Maintain agent — PR business review
	// ------------------------------------------------------------
	"biz_review_output": {
		Name: "biz_review_output",
		Description: `Emit the business evaluation verdict for a PR that has already passed tech review. Call EXACTLY ONCE. This is the last gate before merge approval — decide whether the change aligns with the current milestone and direction.`,
		Parameters: []ToolParam{
			{Name: "result", Type: "string", Required: true,
				Description: `Exactly one of:
  - "approved"     — aligned with milestone/direction, move to merge approval.
  - "needs_changes"— scope creep or tangential work; submitter should re-scope.
  - "rejected"     — fundamentally misaligned (wrong milestone, off-direction); send back.`},
			{Name: "biz_review", Type: "string", Required: true,
				Description: `One short paragraph (3-6 sentences) explaining alignment: which milestone item this closes, which direction pillar it supports, and any scope concerns. This text is shown to humans on the PR card.`},
			{Name: "version_suggestion", Type: "string", Required: false,
				Description: `Optional: semver bump suggestion if approved. Format "MAJOR.MINOR.PATCH" (e.g. "1.4.0"). Use major for breaking changes, minor for features, patch for fixes. If omitted, the platform auto-increments minor.`},
			{Name: "alignment_rationale", Type: "string", Required: false,
				Description: `Optional: generalised lesson captured as Experience — what made this PR aligned/misaligned with direction, framed for future submitters. One sentence, imperative mood.`},
		},
		Examples: []map[string]any{
			{
				"result":             "approved",
				"biz_review":         "Closes milestone item M3-2 (add retry on 429). Aligns with the reliability pillar of DIRECTION.md. No scope concerns.",
				"version_suggestion": "1.4.0",
			},
			{
				"result":     "needs_changes",
				"biz_review": "Core logic is correct but the PR also refactors the logging package, which is out of scope for this milestone. Split into two PRs.",
			},
			{
				"result":     "rejected",
				"biz_review": "The current milestone is auth hardening; this PR adds a billing module. Direction doesn't mention monetisation. Defer to a future milestone.",
			},
		},
		RoleAccess: []Role{RoleMaintain},
	},

	// ------------------------------------------------------------
	// Analyze agent — experience distillation
	// ------------------------------------------------------------
	"analyze_output": {
		Name: "analyze_output",
		Description: `Emit the distillation output after reviewing a batch of raw experiences. Call EXACTLY ONCE. Your job is to turn repeated observations into reusable skills, policy suggestions, and tag adjustments. Do not invent — every artifact must be backed by at least one raw experience ID.`,
		Parameters: []ToolParam{
			{Name: "distilled_experience_ids", Type: "array", Required: true,
				Description: `Array of experience IDs (from the "Experience IDs" list in your prompt) that you actually processed. Marks them as distilled so they aren't re-analyzed next run. Return an empty array if the batch was unusable.`},
			{Name: "skill_candidates", Type: "array", Required: false,
				Description: `New skills synthesised from the experiences. Each element is {name, type, applicable_tags, precondition, action, prohibition, evidence}.
  - name            : short imperative title.
  - type            : "pattern" | "antipattern" | "checklist".
  - applicable_tags : which task tags this applies to (e.g. ["bugfix","auth"]).
  - precondition    : when to apply this (1 sentence).
  - action          : what to do (imperative mood).
  - prohibition     : what NOT to do (optional but recommended).
  - evidence        : ref to 2+ experience IDs that support this.`},
			{Name: "policy_suggestions", Type: "array", Required: false,
				Description: `New governance policies derived from patterns. Each element is {name, match_condition, actions, priority}. match_condition and actions MUST be valid JSON objects. priority 0-100 (higher = earlier match).`},
			{Name: "tag_suggestions", Type: "array", Required: false,
				Description: `Fresh tags Analyze wants to attach to existing tasks, based on real execution data. Each element is {task_id, suggested_tags, dimension?}. Dimension defaults to "category". Idempotent — duplicates are dropped.`},
			{Name: "tag_reviews", Type: "array", Required: false,
				Description: `Re-adjudicate rule-proposed tags that you can now verify against real outcomes. Each element is {tag_id, action, note}. action is "confirm" | "reject". note is a 1-sentence rationale.`},
			{Name: "model_suggestions", Type: "object", Required: false,
				Description: `Optional: suggestions for tuning model choices per role. Shape: {role: suggested_model_id}. Logged for human review only; does not auto-apply.`},
		},
		Examples: []map[string]any{
			{
				"distilled_experience_ids": []string{"exp_abc123", "exp_def456"},
				"skill_candidates": []map[string]any{
					{
						"name":             "Wrap goroutine panics in auth middleware",
						"type":             "pattern",
						"applicable_tags":  []string{"auth", "concurrency"},
						"precondition":     "Spawning a goroutine inside an HTTP handler.",
						"action":           "defer recover() and log the panic with request-id.",
						"prohibition":      "Letting a panic in a detached goroutine take down the server.",
						"evidence":         "exp_abc123, exp_def456",
					},
				},
				"tag_reviews": []map[string]any{
					{"tag_id": "ttag_xyz", "action": "reject", "note": "Execution showed this was a refactor, not a bugfix."},
				},
			},
		},
		RoleAccess: []Role{RoleAnalyze},
	},

	// ------------------------------------------------------------
	// Chief agent — governance & human-voice interface
	// ------------------------------------------------------------
	//
	// Design rule: Chief NEVER mutates the work queue (tasks, milestones,
	// direction) directly. That's Maintain's turf. Chief is a human proxy
	// that approves / rejects / switches / delegates — never deletes an
	// in-flight task out from under a working agent.
	"approve_pr": {
		Name: "approve_pr",
		Description: `Approve a PR, either for tech review (move to Evaluate) or for final merge (trigger the merge). Only usable when the PR is in the matching pending_human_* state. Chief calls this in AutoMode when policy / risk permits; otherwise a human clicks the UI button directly.`,
		Parameters: []ToolParam{
			{Name: "pr_id", Type: "string", Required: true,
				Description: `Exact PR ID from the GlobalState "PR 状态" block (e.g. "pr_abc123"). Not the title.`},
			{Name: "action", Type: "string", Required: true,
				Description: `Exactly one of:
  - "approve_review" — PR is pending_human_review → trigger Evaluate Agent.
  - "approve_merge"  — PR is pending_human_merge → execute merge now.
The tool errors if the PR is in a different state; check "PR 状态" before calling.`},
			{Name: "reason", Type: "string", Required: true,
				Description: `One sentence explaining WHY the approval is safe. Reference the matching policy if applicable. Example: "Matches policy 'small-frontend-auto' (files=3, all *.tsx). No security flags in evaluate_output."`},
		},
		Examples: []map[string]any{
			{"pr_id": "pr_abc123", "action": "approve_review", "reason": "Matches policy 'small-docs-auto' — only README and CHANGELOG changed."},
			{"pr_id": "pr_abc123", "action": "approve_merge", "reason": "Tech review approved, biz review approved, merge_cost_rating=low, no conflicts."},
		},
		ErrorGuidance: `If the PR is not in the expected pending_human_* state, the call fails. Do NOT retry with the other action value — inspect "PR 状态" first and pick the matching one.`,
		RoleAccess:    []Role{RoleChief},
	},

	"reject_pr": {
		Name: "reject_pr",
		Description: `Reject a PR, sending it back to the submitter with a reason. Use when policy requires human involvement but AutoMode is on, or when the PR has clear issues the submitter should address before re-submitting.`,
		Parameters: []ToolParam{
			{Name: "pr_id", Type: "string", Required: true,
				Description: `Exact PR ID (e.g. "pr_abc123"). Not the title.`},
			{Name: "reason", Type: "string", Required: true,
				Description: `One-paragraph rejection reason shown verbatim to the submitter. Be specific and actionable — "adjust retry backoff to exponential" beats "retry logic needs work".`},
		},
		Examples: []map[string]any{
			{"pr_id": "pr_abc123", "reason": "Policy 'db-schema-human-only' matched (file internal/db/schema.sql changed). Needs a human DBA to confirm."},
		},
		RoleAccess: []Role{RoleChief},
	},

	"switch_milestone": {
		Name: "switch_milestone",
		Description: `Archive the current milestone (mark completed) and activate a new one. Non-destructive — tasks are NOT deleted, they stay in the DB tagged to the completed milestone. Use when the current milestone's success criteria are met, OR when a human explicitly requests a pivot.`,
		Parameters: []ToolParam{
			{Name: "milestone_id", Type: "string", Required: true,
				Description: `Exact milestone ID to activate. Must already exist in the DB (created by Maintain via write_milestone earlier). Chief cannot create milestones — ask Maintain via delegate_to_maintain first.`},
			{Name: "reason", Type: "string", Required: true,
				Description: `One-sentence rationale. Example: "Milestone M1 (auth hardening) completed — MFA shipped, rate-limit shipped, audit-log merged. Switching to M2 (observability)."`},
		},
		Examples: []map[string]any{
			{"milestone_id": "ms_obs_v1", "reason": "M1 success criteria met; human PM confirmed pivot to observability."},
		},
		ErrorGuidance: `Do NOT call this if there are claimed tasks on the current milestone — it would orphan in-flight work. Check the "任务概览" block; if "进行中" > 0, call delegate_to_maintain first to complete or reassign those tasks.`,
		RoleAccess:    []Role{RoleChief},
	},

	"create_policy": {
		Name: "create_policy",
		Description: `Persist a decision policy — the human's risk preferences turned into a rule Chief can match against future PRs. Use when a human tells Chief "always X" or "never Y". The policy is active immediately and participates in future AutoMode decisions.`,
		Parameters: []ToolParam{
			{Name: "name", Type: "string", Required: true,
				Description: `Short kebab-case identifier (e.g. "db-schema-human-only"). Used in logs and policy-matched rationales.`},
			{Name: "match_condition", Type: "string", Required: true,
				Description: `Match criteria as a valid JSON object string (not a Go map literal). Known keys:
  - scope          : "pr_review" | "pr_merge" | "milestone_switch" | "task_create"
  - file_count_gt  : int
  - file_count_lt  : int
  - file_pattern   : glob (e.g. "*schema*")
  - merge_cost_in  : array of "low"/"medium"/"high"
  - submitter      : agent ID
Concatenate multiple keys for AND logic. Example: {"scope":"pr_review","file_count_gt":5}`},
			{Name: "actions", Type: "string", Required: true,
				Description: `Actions as a valid JSON object string. Known keys:
  - require_human : true   → Chief must reject_pr with human-required rationale.
  - auto_approve  : true   → Chief may approve_pr without further checks.
  - notify_channel: string → Platform also broadcasts to this channel.
Example: {"require_human":true}`},
			{Name: "priority", Type: "number", Required: false,
				Description: `Match priority 0-100; higher wins when multiple policies match. Default 0.`},
		},
		Examples: []map[string]any{
			{
				"name":            "db-schema-human-only",
				"match_condition": "{\"scope\":\"pr_merge\",\"file_pattern\":\"*schema*\"}",
				"actions":         "{\"require_human\":true}",
				"priority":        90,
			},
			{
				"name":            "small-frontend-auto",
				"match_condition": "{\"scope\":\"pr_review\",\"file_count_lt\":5,\"file_pattern\":\"*.tsx\"}",
				"actions":         "{\"auto_approve\":true}",
				"priority":        20,
			},
		},
		ErrorGuidance: `match_condition and actions MUST be JSON-valid strings. Use double-quoted keys, not Go map syntax. If JSON parsing fails the policy is rejected and the tool call errors.`,
		RoleAccess:    []Role{RoleChief},
	},

	"delegate_to_maintain": {
		Name: "delegate_to_maintain",
		Description: `Forward a human instruction about tasks, milestones, or direction to the Maintain Agent. Chief MUST use this instead of touching the work queue directly — Chief doesn't own tasks and can't safely delete/rename them without colliding with agents in flight. Maintain picks up the delegated instruction in its own session and issues the correct tool call (create_task / update_milestone / write_milestone / propose_direction / delete_task).`,
		Parameters: []ToolParam{
			{Name: "instruction", Type: "string", Required: true,
				Description: `The human's instruction, rephrased in imperative mood with enough context for Maintain to act without the chat history. Keep it to <=3 sentences.`},
			{Name: "scope", Type: "string", Required: true,
				Description: `Exactly one of "tasks" | "milestone" | "direction" | "mixed". Tells Maintain which toolset to reach for first.`},
			{Name: "urgency", Type: "string", Required: false,
				Description: `"now" | "next" | "whenever". Default "next". "now" means Maintain jumps the queue; reserve for genuine blockers.`},
		},
		Examples: []map[string]any{
			{
				"instruction": "Add a high-priority task: migrate the login flow to OAuth2. Acceptance: all existing email/password tests still pass.",
				"scope":       "tasks",
				"urgency":     "next",
			},
			{
				"instruction": "Pivot the direction to lean on the self-evolution loop as the primary product differentiator; deprioritise manual triage.",
				"scope":       "direction",
				"urgency":     "next",
			},
		},
		ErrorGuidance: `Never use this to request PR approvals (use approve_pr/reject_pr) or policy creation (use create_policy). Delegation is strictly for task/milestone/direction edits — the things Chief is forbidden from mutating directly.`,
		RoleAccess:    []Role{RoleChief},
	},

	"chief_output": {
		Name: "chief_output",
		Description: `Emit the Chief session's final user-facing summary. Call EXACTLY ONCE at the end of every Chief session (decision or chat). The summary is what the human sees on the dashboard — write it in their language, referencing the concrete actions you took (via other tool calls) in this session.`,
		Parameters: []ToolParam{
			{Name: "result", Type: "string", Required: true,
				Description: `Short status label, exactly one of:
  - "reported"   — chat / status response; no state mutations.
  - "approved"   — you called approve_pr.
  - "rejected"   — you called reject_pr.
  - "delegated" — you called delegate_to_maintain.
  - "switched"   — you called switch_milestone.
  - "policy_added" — you called create_policy.
  - "no_action"  — you chose to wait / ask the human.`},
			{Name: "summary", Type: "string", Required: true,
				Description: `The human-facing message, 1-4 sentences. Match the human's language (中/英). Reference IDs for anything you touched (PR ID, milestone ID, policy name). Example: "已批准 PR pr_abc123 进入技术评审（匹配策略 small-frontend-auto）。剩余 3 个 PR 等待审批。"`},
			{Name: "pending_questions", Type: "array", Required: false,
				Description: `Optional: questions Chief wants the human to answer next session. Each is a string. Surface ambiguities here instead of guessing.`},
		},
		Examples: []map[string]any{
			{
				"result":  "approved",
				"summary": "已批准 PR pr_abc123 进入合并（技术评审通过，无冲突，匹配策略 small-frontend-auto）。",
			},
			{
				"result":            "no_action",
				"summary":           "PR pr_def456 风险较高（涉及 schema.sql），当前策略要求人类确认。暂不操作，等你发话。",
				"pending_questions": []string{"要批准 PR pr_def456 的 schema 变更吗？"},
			},
			{
				"result":  "delegated",
				"summary": "已把\"新增 OAuth2 迁移任务\"交给 Maintain 处理（urgency=next）。完成后会回报。",
			},
		},
		ErrorGuidance: `Calling chief_output without doing the thing you say you did is a platform-level violation. If result="approved" but you didn't call approve_pr in this session, the session is flagged as a hallucination. Always invoke the matching state-change tool first, then chief_output to confirm.`,
		RoleAccess:    []Role{RoleChief},
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