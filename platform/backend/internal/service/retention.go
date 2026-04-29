package service

// Retention worker: prune high-volume volatile tables on a daily cadence.
//
// Why this exists. Until now, two tables grew unbounded:
//
//   * tool_call_trace        one row per platform-tool invocation
//   * dialogue_message       one row per chief/maintain chat turn
//
// On a busy operator deployment (≈100 tool calls / 50 chat turns per
// day) this is multi-GB after a few months, slows down the dashboard
// queries that LEFT JOIN them, and provides ~zero additional value
// past the first few weeks of retention because the Refinery has
// already distilled the long-term signal into KnowledgeArtifacts.
//
// What this is NOT.
//
//   * It's NOT pruning Experience / Episode / KnowledgeArtifact —
//     those are *inputs* to the self-evolution loop and have
//     long-term value. If we ever want to bound those too, add a
//     separate, more careful retention rule (probably keyed by
//     status='deprecated' rather than just age).
//   * It's NOT pruning Change / PullRequest / Task / Milestone /
//     ContentBlock — those are project state and pruning them would
//     hide history operators rely on.
//   * It's NOT a vacuum / OPTIMIZE — we only DELETE; reclaiming
//     pages is the DBA's call.
//
// Tuning.
//
// Default retention is 90 days. Override with A3C_RETENTION_DAYS
// (must be a positive integer; otherwise the default applies and
// we log a warning at boot).
//
// Cadence is once per 24h. Cron-style "at 03:00" would be nicer
// but adds dependency surface for negligible win — we just track
// "last run" and re-fire whenever 24h has elapsed since.
//
// Idempotency.
//
// DELETE-where-created_at-< cutoff is naturally idempotent: a second
// pass within the cadence simply finds nothing to delete. We also
// log the per-table delete count so operators can see how much was
// reaped on each tick.

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/a3c/platform/internal/model"
)

// retentionDays returns the configured retention window in days,
// honouring A3C_RETENTION_DAYS and falling back to a 90-day default
// on missing/invalid input. Logs a warning if the env var is set
// but unparseable so operators don't get silent default-fallback.
func retentionDays() int {
	const defaultDays = 90
	raw := strings.TrimSpace(os.Getenv("A3C_RETENTION_DAYS"))
	if raw == "" {
		return defaultDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Printf("[Retention] A3C_RETENTION_DAYS=%q invalid, falling back to %d days", raw, defaultDays)
		return defaultDays
	}
	return n
}

// StartRetentionTimer kicks off a goroutine that prunes tool_call_trace
// and dialogue_message rows older than retentionDays() once per 24h.
// First run fires after a 5-minute warmup to keep boot quiet.
//
// Like the other Start* timers (heartbeat, refinery, analyze), this
// goroutine has no Stop method — process exit reclaims it. If we
// later add a poolCtx-style cancellation channel for all background
// loops, this is one of the consumers.
func StartRetentionTimer() {
	days := retentionDays()
	log.Printf("[Retention] starting daily worker (retention=%d days)", days)

	go func() {
		// Warm-up delay so we don't pile this onto boot-time DB
		// connections that are already loaded with migration / seed
		// queries.
		time.Sleep(5 * time.Minute)

		runOnce := func() {
			cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
			pruneOlderThan(cutoff)
		}

		runOnce()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			runOnce()
		}
	}()
}

// pruneOlderThan deletes rows older than cutoff from the target
// tables. Each table is pruned independently — a failure on one
// doesn't abort the others.
//
// Implementation note: we use Where().Delete(&Type{}) so GORM still
// honours soft-delete columns if any model ever adds them. None of
// the current targets have one, but defensive coding here is
// cheap insurance against silent regression if someone adds
// `gorm.DeletedAt` to a target later.
func pruneOlderThan(cutoff time.Time) {
	type pruneTarget struct {
		name string
		// model: a typed pointer so we can use it as both the
		// destination of Where().Delete() and the source of
		// per-table reflection if ever needed.
		model interface{}
	}

	targets := []pruneTarget{
		{"tool_call_trace", &model.ToolCallTrace{}},
		{"dialogue_message", &model.DialogueMessage{}},
	}

	for _, t := range targets {
		res := model.DB.Where("created_at < ?", cutoff).Delete(t.model)
		if res.Error != nil {
			log.Printf("[Retention] %s prune failed: %v", t.name, res.Error)
			continue
		}
		if res.RowsAffected > 0 {
			log.Printf("[Retention] %s pruned %d rows older than %s",
				t.name, res.RowsAffected, cutoff.Format("2006-01-02"))
		}
	}
}
