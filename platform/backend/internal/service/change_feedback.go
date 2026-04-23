package service

// Change → artifact feedback loop
// ===============================
//
// When an MCP client agent claims a task it receives hints — a curated
// set of KnowledgeArtifact IDs chosen by the refinery + semantic
// retrieval. The agent carries those IDs through the pipeline (stored on
// the Change row as InjectedArtifacts) and submits its change.
//
// Audit then gives the change a verdict:
//   L0 → clean, no conflict         → the hints worked   (success_count++)
//   L1 → minor fix needed           → partial credit     (no bump)
//   L2 → reviewer rejected entirely → the hints failed   (failure_count++)
//
// That's the full "client agent self-evolution" loop closed. Without this
// function the client half of the system has no way to learn — the server
// bumps counters for Chief and Analyze via HandleSessionCompletion, but
// client-facing artifacts just accumulated usage with no success signal.
//
// Idempotent: FeedbackApplied guard on the Change row prevents the hook
// from firing twice if an audit gets updated or a session retries.

import (
	"encoding/json"
	"log"
	"sort"

	"github.com/a3c/platform/internal/model"
	"gorm.io/gorm"
)

// HandleChangeAudit is called by the audit pipeline once a Change has
// been given a definitive L0/L1/L2 verdict. Safe to call with any
// audit level (including unknown ones — it becomes a no-op).
//
// Behaviour summary:
//   - L0     → top-K attributed artifacts get success_count += 1
//   - L1     → top-1 attributed artifact gets success_count += 1 (partial credit)
//   - L2     → top-K attributed artifacts get failure_count += 1
//   - empty  → early return, no work
//
// Attribution (rank-based, no LLM): when refs carry Score, we pick the
// K highest-scoring IDs to credit/punish. When refs is empty (legacy
// flat-id payload) we fall back to the original order — the selector
// already stored IDs in rank order. See attributionK for the K policy.
// Rationale: blanket-crediting every injected artifact on L0 inflates
// noise artifacts' success counters; with 5-10 artifacts injected per
// task usually only 1-3 actually drove the decision. L1 similarly gets
// treated as "direction was right" — the single best-matching item
// gets partial credit rather than dropping the signal on the floor.
//
// Always marks FeedbackApplied=true on the Change so the next call with
// the same ID is a no-op.
func HandleChangeAudit(changeID, auditLevel string) {
	defer func() {
		// Never let a feedback bookkeeping glitch break the real audit
		// pipeline. Log and move on.
		if r := recover(); r != nil {
			log.Printf("[ChangeFeedback] recovered from panic on change=%s: %v", changeID, r)
		}
	}()

	if changeID == "" {
		return
	}

	var change model.Change
	if err := model.DB.Where("id = ?", changeID).First(&change).Error; err != nil {
		log.Printf("[ChangeFeedback] change %s not found: %v", changeID, err)
		return
	}
	if change.FeedbackApplied {
		return // idempotency guard
	}
	if change.InjectedArtifacts == "" {
		// Nothing was injected on claim — still mark applied so we
		// don't keep re-scanning this change on every call.
		model.DB.Model(&model.Change{}).Where("id = ?", changeID).
			Update("feedback_applied", true)
		return
	}

	// The stored payload is one of two shapes:
	//   legacy: ["ka_1", "ka_2", ...]                    — PR 4
	//   rich:   [{"id":"ka_1","reason":"semantic=0.8",...}, ...]  — PR 5+
	// Both carry the same ids; the rich shape additionally tells us
	// which retrieval signal chose each artifact. We parse both and
	// extract (ids, reasons).
	ids, refs := parseInjectedArtifacts(change.InjectedArtifacts)
	if len(ids) == 0 {
		log.Printf("[ChangeFeedback] change %s has malformed or empty injected_artifacts", changeID)
		model.DB.Model(&model.Change{}).Where("id = ?", changeID).
			Update("feedback_applied", true)
		return
	}

	var (
		column  string
		verdict string
		bumpIDs []string
	)
	switch auditLevel {
	case "L0":
		column = "success_count"
		verdict = "success"
		bumpIDs = topKByRank(ids, refs, attributionK(len(ids)))
	case "L1":
		// Partial credit: L1 means "direction mostly right, execution
		// detail off" — crediting the single top-ranked artifact
		// captures that without over-rewarding noise alongside it.
		column = "success_count"
		verdict = "partial"
		bumpIDs = topKByRank(ids, refs, 1)
	case "L2":
		column = "failure_count"
		verdict = "failure"
		bumpIDs = topKByRank(ids, refs, attributionK(len(ids)))
	default:
		// Unknown level — mark applied but don't bump anything.
		model.DB.Model(&model.Change{}).Where("id = ?", changeID).
			Update("feedback_applied", true)
		return
	}

	// Defensive: if attribution policy returned zero IDs (shouldn't
	// happen when len(ids) > 0, but guard anyway), still flip the
	// applied flag so this row isn't rescanned.
	if len(bumpIDs) == 0 {
		model.DB.Model(&model.Change{}).Where("id = ?", changeID).
			Update("feedback_applied", true)
		return
	}

	// Bump + mark applied atomically so concurrent audit writes can't
	// double-count. GORM's Updates inside a transaction is sufficient
	// given the FeedbackApplied guard we checked above.
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.KnowledgeArtifact{}).
			Where("id IN ?", bumpIDs).
			Update(column, gorm.Expr(column+" + 1")).Error; err != nil {
			return err
		}
		return tx.Model(&model.Change{}).Where("id = ?", changeID).
			Update("feedback_applied", true).Error
	})
	if err != nil {
		log.Printf("[ChangeFeedback] failed to apply feedback for change %s: %v", changeID, err)
		return
	}
	log.Printf("[ChangeFeedback] change=%s audit=%s → %s on %d/%d injected artifacts",
		changeID, auditLevel, verdict, len(bumpIDs), len(ids))

	// Per-reason breakdown (PR 5): aggregate bumps by the *dominant*
	// retrieval signal so operators / dashboards can later compute
	// "semantic-driven artifacts succeed 84% of the time, importance-
	// driven ones only 41%". Logged (not persisted) on purpose — cheap
	// observability today, real storage when the signal is proven
	// useful. Empty reasons (legacy payloads) show up as "unknown".
	// Restricted to the attributed subset so the tally reflects what we
	// actually credited, not the full injection.
	if len(refs) > 0 {
		attributedRefs := filterRefsByIDs(refs, bumpIDs)
		tally := aggregateByDominantSignal(attributedRefs)
		log.Printf("[ChangeFeedback] change=%s %s by dominant-signal: %s",
			changeID, verdict, formatSignalTally(tally))
	}
}

// attributionK decides how many of the N injected artifacts deserve
// credit/punishment for a non-L1 audit verdict. Rationale: small
// injections (n≤2) are almost always "all drove the decision"; medium
// injections (n=3..4) partial-credit the top 2; larger pools cap at 3.
// L1 always uses 1 — handled at the call site, not here.
//
// The cap of 3 is intentional: with a budget of 10 artifacts per
// injection the long tail is almost certainly noise; crediting the
// bottom half of the list pollutes the success-rate signal for those
// artifacts. 3 matches our empirical guess at "how many did the agent
// likely lean on?" — tune by log data once we've shipped.
func attributionK(n int) int {
	switch {
	case n <= 0:
		return 0
	case n <= 2:
		return n
	case n <= 4:
		return 2
	default:
		return 3
	}
}

// topKByRank returns the IDs of the top-K artifacts by Score.
//
//   - When refs carry usable scores (Score > 0 for at least one entry),
//     orders by Score DESC and returns the k highest.
//   - When refs is empty or all-zero scores (legacy flat-id payload, or
//     older clients that never emitted scores), falls back to the
//     caller's original order — the retrieval selector already stored
//     IDs in rank order, so "first K" is still a reasonable proxy.
//
// Returns nil when k ≤ 0 or the input list is empty; returns the
// original slice unchanged when k ≥ len(ids).
func topKByRank(ids []string, refs []InjectedRef, k int) []string {
	if k <= 0 || len(ids) == 0 {
		return nil
	}
	if k >= len(ids) {
		return ids
	}
	if len(refs) == len(ids) && hasUsableScores(refs) {
		sorted := make([]InjectedRef, len(refs))
		copy(sorted, refs)
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].Score > sorted[j].Score
		})
		out := make([]string, 0, k)
		for i := 0; i < k; i++ {
			out = append(out, sorted[i].ID)
		}
		return out
	}
	return ids[:k]
}

// hasUsableScores returns true when at least one ref has a non-zero
// Score. Zero-only payloads are treated as "scores missing" so we fall
// back to order-based ranking rather than returning a random subset.
func hasUsableScores(refs []InjectedRef) bool {
	for _, r := range refs {
		if r.Score > 0 {
			return true
		}
	}
	return false
}

// filterRefsByIDs returns the subset of refs whose ID is in keepIDs.
// Used by the dominant-signal logger so the tally reflects what we
// actually credited, not what was injected.
func filterRefsByIDs(refs []InjectedRef, keepIDs []string) []InjectedRef {
	if len(refs) == 0 || len(keepIDs) == 0 {
		return nil
	}
	keep := make(map[string]struct{}, len(keepIDs))
	for _, id := range keepIDs {
		keep[id] = struct{}{}
	}
	out := make([]InjectedRef, 0, len(keepIDs))
	for _, r := range refs {
		if _, ok := keep[r.ID]; ok {
			out = append(out, r)
		}
	}
	return out
}

// parseInjectedArtifacts accepts either the legacy `["id1","id2"]` shape
// or the richer `[{"id":...,"reason":...,"score":...}]` shape persisted
// starting in PR 5. Returns (ids, refs) so callers can pick the subset
// they need; refs is nil when the payload was the legacy form.
func parseInjectedArtifacts(raw string) (ids []string, refs []InjectedRef) {
	if raw == "" {
		return nil, nil
	}
	// Try rich shape first. If it decodes to a non-empty slice whose
	// first element has a non-empty ID, we trust it.
	var richAttempt []InjectedRef
	if err := json.Unmarshal([]byte(raw), &richAttempt); err == nil {
		if len(richAttempt) > 0 && richAttempt[0].ID != "" {
			ids = make([]string, 0, len(richAttempt))
			for _, r := range richAttempt {
				if r.ID != "" {
					ids = append(ids, r.ID)
				}
			}
			return ids, richAttempt
		}
	}
	// Legacy flat-id shape.
	var legacyIDs []string
	if err := json.Unmarshal([]byte(raw), &legacyIDs); err == nil {
		return legacyIDs, nil
	}
	return nil, nil
}

// aggregateByDominantSignal groups refs by their "dominant" signal —
// the first component of the reason string, which the selector emits in
// descending weight order (semantic > tag > importance > recency).
// "semantic=0.81;importance=0.34" aggregates as "semantic".
func aggregateByDominantSignal(refs []InjectedRef) map[string]int {
	out := map[string]int{}
	for _, r := range refs {
		sig := dominantSignal(r.Reason)
		out[sig]++
	}
	return out
}

// dominantSignal extracts the key of the first `key=value` token in a
// reason string. Returns "unknown" for empty or malformed reasons.
func dominantSignal(reason string) string {
	if reason == "" {
		return "unknown"
	}
	// reason format: "semantic=0.81;tag=0.20;importance=0.34;recency=1.00"
	// Take up to the first ';', then up to the first '='.
	end := len(reason)
	for i := 0; i < len(reason); i++ {
		if reason[i] == ';' {
			end = i
			break
		}
	}
	head := reason[:end]
	for i := 0; i < len(head); i++ {
		if head[i] == '=' {
			return head[:i]
		}
	}
	return head
}

// formatSignalTally renders the aggregate map as "semantic=3,importance=2"
// with keys sorted so log lines are stable across runs.
func formatSignalTally(tally map[string]int) string {
	if len(tally) == 0 {
		return "(empty)"
	}
	keys := make([]string, 0, len(tally))
	for k := range tally {
		keys = append(keys, k)
	}
	// Sort in-place without an extra import: tally is small (≤4 signals).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + itoa(tally[k])
	}
	return joinStrs(parts, ",")
}

// tiny locals so we don't widen the import block for one call each.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func joinStrs(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	total := len(sep) * (len(parts) - 1)
	for _, p := range parts {
		total += len(p)
	}
	out := make([]byte, 0, total)
	for i, p := range parts {
		if i > 0 {
			out = append(out, sep...)
		}
		out = append(out, p...)
	}
	return string(out)
}
