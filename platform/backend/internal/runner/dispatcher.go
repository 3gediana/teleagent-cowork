package runner

// Dispatcher — the binding layer between agent.DispatchSession (the
// thing the platform calls when a session is ready to run) and the
// native runner Loop.
//
// The old dual-runtime routing (native vs opencode fallback) is gone:
// every platform agent now runs through the native runner. The only
// dispatch decision left is "which endpoint + model does this role
// use?" — resolved via RoleOverride, falling back to the first
// registered llm.Entry when the override is empty.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/llm"
	"github.com/a3c/platform/internal/model"
)

// SessionCompletionHandler is invoked after a native-runtime session
// finishes (regardless of success). Wired at startup to
// service.HandleSessionCompletion so the refinery feedback loop bumps
// the right counters on KnowledgeArtifacts that were injected into
// the finished session — closing the self-evolution loop that
// opencode had via a parallel hook.
//
// Signature matches service.HandleSessionCompletion exactly so the
// wire-up is a one-liner. nil = silently skip (test contexts).
var SessionCompletionHandler func(sessionID, projectID, role, status string)

// NativeRegistryBuilder is how the dispatcher constructs the Tool set
// for a given role. Set by wire.go (or by tests); defaults to a
// builder that only includes the 4 builtin file tools.
//
// Having the builder injectable, rather than hard-coded, means the
// platform-tools wrapper layer (audit_output, create_task, ...) can
// register itself in its own file without runner having to import
// every domain package.
var NativeRegistryBuilder func(role agent.Role) *Registry = DefaultRegistryBuilder

// DefaultRegistryBuilder returns just the 4 builtin file tools. Good
// enough for a first-run smoke test before the platform-tools wrapper
// layer (Phase 1D) lands.
func DefaultRegistryBuilder(role agent.Role) *Registry {
	r := NewRegistry()
	r.Register(ReadTool{})
	r.Register(GlobTool{})
	r.Register(GrepTool{})
	r.Register(EditTool{})
	return r
}

// NativePromptBuilder turns a session into (systemPrompt, userInput).
// Default reuses agent.BuildPrompt for the system prompt and stuffs
// the session's input-content field into the user turn. Replaced in
// Phase 1D by a richer builder that renders the task brief.
var NativePromptBuilder func(sess *agent.Session) (systemPrompt string, userInput string, err error) = DefaultPromptBuilder

// DefaultPromptBuilder gives a sensible default for the smoke-test
// path. The system prompt is whatever the role's template yields;
// the user turn is the session's input content (falls back to a
// short "begin" instruction if empty).
func DefaultPromptBuilder(sess *agent.Session) (string, string, error) {
	prompt, err := agent.BuildPrompt(sess.Role, sess.Context)
	if err != nil {
		return "", "", err
	}
	user := ""
	if sess.Context != nil {
		user = sess.Context.InputContent
	}
	if strings.TrimSpace(user) == "" {
		user = fmt.Sprintf("Begin your work as the %s agent. Use the tools you've been given to inspect the project and produce the required output.", sess.Role)
	}
	return prompt, user, nil
}

// Dispatch is the entry point registered with agent.RegisterDispatcher
// at server startup. Every session routes through the native runner;
// the only remaining work is resolving which LLM endpoint + model to
// use.
//
// Resolution order:
//   1. RoleOverride with both provider + model set → use both
//   2. RoleOverride with provider only            → use endpoint's default model
//   3. No override                                 → use the first registered
//      llm.Entry (fresh-install ergonomics; stops agents from silently
//      failing before the operator has configured roles explicitly)
//
// When the registry is empty we fail loudly: there's literally no way
// to run an LLM agent without at least one endpoint.
func Dispatch(sess *agent.Session) error {
	cfg := agent.GetRoleConfigWithOverride(sess.Role)
	if cfg == nil {
		return fmt.Errorf("dispatcher: unknown role %q", sess.Role)
	}

	resolved, err := resolveEndpointForRole(cfg)
	if err != nil {
		return fmt.Errorf("dispatcher: role %q: %w", sess.Role, err)
	}

	log.Printf("[Dispatcher] session=%s role=%s → native runner (endpoint=%s model=%s)",
		sess.ID, sess.Role, resolved.endpointID, resolved.modelID)
	return runNative(sess, resolved)
}

// resolvedRoute packages the outcome of role → endpoint resolution so
// runNative doesn't need to re-do the work.
type resolvedRoute struct {
	endpointID string
	modelID    string
}

// resolveEndpointForRole walks the resolution order documented on
// Dispatch. Exported as lowercase so tests in the same package can
// assert on specific fallback paths without going through runNative.
func resolveEndpointForRole(cfg *agent.RoleConfig) (resolvedRoute, error) {
	// Explicit override — just validate the endpoint exists.
	if cfg.ModelProvider != "" {
		entry, err := llm.DefaultRegistry.Get(cfg.ModelProvider)
		if err != nil {
			return resolvedRoute{}, fmt.Errorf("configured endpoint %q not registered: %w", cfg.ModelProvider, err)
		}
		modelID := cfg.ModelID
		if modelID == "" {
			modelID = entry.DefaultModel
		}
		if modelID == "" {
			return resolvedRoute{}, fmt.Errorf("endpoint %q has no default model and no explicit model_id on the role", cfg.ModelProvider)
		}
		return resolvedRoute{endpointID: cfg.ModelProvider, modelID: modelID}, nil
	}

	// No override — pick the first registered endpoint with a default
	// model. Ordering is non-deterministic but stable within a single
	// process run, which is good enough for fresh-install ergonomics;
	// operators set an explicit override as soon as they have a
	// preference.
	for _, e := range llm.DefaultRegistry.List() {
		if e.DefaultModel != "" {
			return resolvedRoute{endpointID: e.EndpointID, modelID: e.DefaultModel}, nil
		}
	}
	return resolvedRoute{}, fmt.Errorf("no LLM endpoints registered — add one via the dashboard first")
}

// runNative wires a ready-to-run session through the native Loop.
// Errors are recorded onto the session (Status=failed) before
// returning so the dispatcher's caller doesn't need to duplicate
// that bookkeeping.
func runNative(sess *agent.Session, route resolvedRoute) error {
	// Mark running right away. On failure we flip to failed below;
	// on success the caller updates to completed once output has
	// been ingested by the platform.
	markSession(sess, "running", "")

	reg := NativeRegistryBuilder(sess.Role)
	systemPrompt, userInput, err := NativePromptBuilder(sess)
	if err != nil {
		markSession(sess, "failed", fmt.Sprintf("prompt: %v", err))
		return fmt.Errorf("dispatcher: build prompt: %w", err)
	}

	// Session-scoped context: we pick a generous 15-minute budget
	// because audit / fix flows can involve many tool round-trips;
	// the underlying ChatStream also honours ctx.Done quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	started := time.Now()
	res, err := Run(ctx, sess, reg, RunOptions{
		EndpointID:    route.endpointID,
		Model:         route.modelID,
		SystemPrompt:  systemPrompt,
		UserInput:     userInput,
		MaxTokens:     defaultMaxTokensForRole(sess.Role),
		MaxIterations: 25,
		// Auto-compaction protects long chief/analyze sessions from
		// hitting the provider's context window. See compaction.go
		// for the two-tier design (microcompact then summarize).
		Compaction: DefaultCompactionPolicy,
		// Tier-0 clear policy — only applied to long-lived / chat
		// roles. Audit / fix / evaluate / merge are single-shot and
		// short; a clear inside them would be pointless (they'll
		// hit their terminal tool and exit anyway).
		Clear: clearPolicyForRole(sess.Role),
	})
	duration := time.Since(started)
	if err != nil {
		markSession(sess, "failed", err.Error())
		fireSessionCompletion(sess, "failed")
		return fmt.Errorf("dispatcher: native run: %w", err)
	}

	// Persist the assembled output onto the in-memory Session + DB
	// so downstream consumers (change audit, PR gate, dashboard)
	// see it the same way they always have.
	//
	// Guard: a terminal tool handler (chief_output, evaluate_output,
	// etc.) may have already set sess.Output to a structured summary
	// via agent.DefaultManager.UpdateSessionOutput. In that case
	// res.FinalText is usually just the model's closing "Done." and
	// overwriting would lose the useful summary. Only fill from the
	// trailing text when the tool path didn't.
	if strings.TrimSpace(sess.Output) == "" {
		sess.Output = res.FinalText
	}
	persistRunMetadata(sess, route, res, duration)
	markSession(sess, "completed", "")
	fireSessionCompletion(sess, "completed")
	return nil
}

// fireSessionCompletion closes the refinery feedback loop — identical
// to opencode.SessionCompletionHandler firing at end of serve session.
// Guarded by recover() so a panicky feedback path can't stain the
// dispatcher's success signal. Nil handler = silently skip (smoke
// tests that don't wire the full service stack).
func fireSessionCompletion(sess *agent.Session, status string) {
	if SessionCompletionHandler == nil || sess == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Dispatcher] session-completion hook panicked: %v", r)
		}
	}()
	SessionCompletionHandler(sess.ID, sess.ProjectID, string(sess.Role), status)
}

// markSession updates both the in-memory Session and the DB row.
// Uses the columns that actually exist on model.AgentSession — see
// @platform/backend/internal/model/agent_session.go for the schema.
// The runner-native path doesn't go through opencode's hooks so we
// replicate the minimum DB bookkeeping here.
func markSession(sess *agent.Session, status, errMsg string) {
	sess.Status = status
	updates := map[string]any{"status": status}
	if errMsg != "" {
		updates["last_error"] = errMsg
	}
	if status == "completed" || status == "failed" {
		now := time.Now()
		updates["completed_at"] = &now
	}
	if err := model.DB.Model(&model.AgentSession{}).
		Where("id = ?", sess.ID).
		Updates(updates).Error; err != nil {
		log.Printf("[Dispatcher] failed to persist session %s status=%s: %v", sess.ID, status, err)
	}
}

// persistRunMetadata writes output + duration onto the DB row. We
// don't track per-session token counts in the DB yet (no columns for
// it on AgentSession); the cost line is logged so operators can see
// it in the server journal while the schema catches up.
func persistRunMetadata(sess *agent.Session, route resolvedRoute, res *RunResult, duration time.Duration) {
	// Best-effort pricing: registry's model catalogue may have rates
	// the endpoint didn't list explicitly.
	u := res.Usage
	if u.USD == 0 {
		if entry, err := llm.DefaultRegistry.Get(route.endpointID); err == nil {
			for _, m := range entry.Provider.Models() {
				if m.ID == route.modelID {
					u = llm.AttachCost(u, llm.MergePricing(m))
					break
				}
			}
		}
	}
	log.Printf("[Dispatcher] session=%s role=%s tokens=%d/%d cache=%d/%d cost=$%.6f iters=%d duration=%s",
		sess.ID, sess.Role, u.InputTokens, u.OutputTokens,
		u.CacheReadTokens, u.CacheCreationTokens, u.USD,
		res.Iterations, duration)

	// Same guard as runNative — use whatever the terminal tool
	// already wrote onto sess.Output; only fall back to the LLM's
	// closing text when no tool filled it in.
	outputToWrite := sess.Output
	if strings.TrimSpace(outputToWrite) == "" {
		outputToWrite = res.FinalText
	}
	updates := map[string]any{
		"output":      outputToWrite,
		"duration_ms": int(duration.Milliseconds()),
	}
	if err := model.DB.Model(&model.AgentSession{}).
		Where("id = ?", sess.ID).
		Updates(updates).Error; err != nil {
		log.Printf("[Dispatcher] failed to persist run metadata for %s: %v", sess.ID, err)
	}
}

// defaultMaxTokensForRole gives each role a sensible output budget.
// Audit / decision-making roles only need to emit a structured tool
// call, so 1024 is plenty; maintenance / write roles need room for
// rich milestone docs.
func defaultMaxTokensForRole(role agent.Role) int {
	switch role {
	case agent.RoleAudit1, agent.RoleAudit2, agent.RoleFix:
		return 2048
	case agent.RoleMaintain, agent.RoleAssess:
		return 8192
	default:
		return 4096
	}
}

// clearPolicyForRole returns the tier-0 ClearPolicy appropriate for a
// given role. Heuristic:
//
//   - Chat / long-lived roles (Chief, Consult) → DefaultClearPolicy.
//     These are the sessions where topic-shift / idle-gap / terminal-
//     output clears actually earn their keep — a human operator may
//     Chief-chat for hours, switching topics, and we don't want every
//     new question to come dragging 200 turns of unrelated history.
//
//   - Analyze / Assess → DefaultClearPolicy too (they run long
//     transcripts over batches of experiences; topic-shift inside
//     one batch is rare but idle-gap still matters across days).
//
//   - Short-lived roles (audit_1/2, fix, evaluate, merge) → disabled.
//     These sessions have a known terminal tool; the loop exits the
//     moment it's called. Clearing inside them is dead code that
//     would just cost cycles running the Jaccard every iteration.
func clearPolicyForRole(role agent.Role) ClearPolicy {
	switch role {
	case agent.RoleChief, agent.RoleConsult, agent.RoleAnalyze, agent.RoleAssess, agent.RoleMaintain:
		return DefaultClearPolicy
	default:
		return ClearPolicy{} // disabled
	}
}
