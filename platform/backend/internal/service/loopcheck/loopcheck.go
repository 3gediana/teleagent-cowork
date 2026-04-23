// Package loopcheck diagnoses whether the self-evolution and
// automation loops of the A3C platform are actually flowing.
//
// The project's two marquee features — "the system learns from
// itself" (Phase 3B) and "the system runs itself" (Phase 3A) — are
// scaffolded across ~30 service functions + 5 timers + 6 agent
// roles. When something breaks or falls silent, there is no single
// place to look. This package is that place.
//
// It NEVER mutates the database. It only queries counts, statuses
// and timestamps, then classifies each loop segment into one of:
//
//   healthy  — data flowing in the expected cadence
//   stale    — the loop is wired but hasn't been exercised lately
//   unused   — feature exists but was never actually triggered
//   broken   — a hard failure signal (e.g. recent retries all failed)
//
// The same Report is consumed by:
//   - experiments/loopcheck (CLI formatter)
//   - handler/loopcheck.go  (HTTP JSON response)
//   - frontend LoopCheckPage (renders JSON into cards)
package loopcheck

import (
	"time"

	"gorm.io/gorm"

	"github.com/a3c/platform/internal/model"
)

// Status is a coarse health classifier. Frontend uses it to pick a
// badge colour; the CLI uses it to pick an ANSI escape. The order
// here matters: Rollup() picks the *worst* status across children.
type Status string

const (
	StatusHealthy Status = "healthy" // green
	StatusStale   Status = "stale"   // yellow — wired but quiet
	StatusUnused  Status = "unused"  // grey — never invoked
	StatusBroken  Status = "broken"  // red — hard failure
)

// severity ranks statuses. Higher == worse.
func severity(s Status) int {
	switch s {
	case StatusBroken:
		return 3
	case StatusStale:
		return 2
	case StatusUnused:
		return 1
	case StatusHealthy:
		return 0
	}
	return 0
}

// Rollup returns the worst status in xs. Empty list yields healthy
// because "no checks" is a less alarming signal than a concrete
// broken child.
func Rollup(xs ...Status) Status {
	worst := StatusHealthy
	for _, s := range xs {
		if severity(s) > severity(worst) {
			worst = s
		}
	}
	return worst
}

// Check is the common shape every loop segment produces. Keeping
// this uniform lets the frontend render all checks with one card
// component and the CLI with one banner format.
type Check struct {
	Name    string            `json:"name"`
	Status  Status            `json:"status"`
	Summary string            `json:"summary"`
	Metrics map[string]any    `json:"metrics,omitempty"`
	// LastActivity is the most recent timestamp any row the check
	// looked at was touched. Zero value means "no rows seen".
	LastActivity *time.Time `json:"last_activity,omitempty"`
}

// Report is the top-level payload. One Report covers one project.
// ProjectID=="" means "aggregate across all projects".
type Report struct {
	GeneratedAt   time.Time     `json:"generated_at"`
	WindowDays    int           `json:"window_days"`
	ProjectID     string        `json:"project_id,omitempty"`
	SelfEvolution Loop          `json:"self_evolution"`
	Automation    Loop          `json:"automation"`
	OverallStatus Status        `json:"overall_status"`
}

// Loop is one of the two top-level pillars. Its OverallStatus is
// rolled up from its children so a single glance at a Loop header
// tells you whether to drill in.
type Loop struct {
	Name          string   `json:"name"`
	OverallStatus Status   `json:"overall_status"`
	Checks        []*Check `json:"checks"`
}

// Options controls what the report covers.
type Options struct {
	// ProjectID scopes every query to one project. Empty = platform-
	// wide aggregate.
	ProjectID string
	// WindowDays is the look-back window for "recent activity"
	// counts. Default 7.
	WindowDays int
}

// Generate runs all checks and returns a full Report. The DB
// handle is taken from model.DB (global). This function is safe to
// call from both a CLI process (no HTTP server running) and the
// running server — it only reads.
func Generate(opts Options) *Report {
	if opts.WindowDays <= 0 {
		opts.WindowDays = 7
	}
	since := time.Now().Add(-time.Duration(opts.WindowDays) * 24 * time.Hour)

	evo := Loop{
		Name: "self_evolution",
		Checks: []*Check{
			checkFeedbackToExperience(opts, since),
			checkExperienceToAnalyze(opts, since),
			checkSkillToPolicy(opts),
			checkPolicyMatching(opts),
			checkArtifactInjection(opts, since),
			checkRefineryPipeline(opts, since),
		},
	}
	auto := Loop{
		Name: "automation",
		Checks: []*Check{
			checkHeartbeat(opts),
			checkTimerActivity(opts, since),
			checkChiefAutoApproval(opts, since),
			checkRetryMechanism(opts, since),
			checkFailureModes(opts, since),
		},
	}

	evoStatuses := make([]Status, len(evo.Checks))
	for i, c := range evo.Checks {
		evoStatuses[i] = c.Status
	}
	evo.OverallStatus = Rollup(evoStatuses...)

	autoStatuses := make([]Status, len(auto.Checks))
	for i, c := range auto.Checks {
		autoStatuses[i] = c.Status
	}
	auto.OverallStatus = Rollup(autoStatuses...)

	return &Report{
		GeneratedAt:   time.Now(),
		WindowDays:    opts.WindowDays,
		ProjectID:     opts.ProjectID,
		SelfEvolution: evo,
		Automation:    auto,
		OverallStatus: Rollup(evo.OverallStatus, auto.OverallStatus),
	}
}

// scoped returns a *gorm.DB chain pre-filtered by project_id when
// opts.ProjectID is set. Every table in this package uses the same
// column name, so a single helper keeps the checks short.
func scoped(opts Options, table any) *gorm.DB {
	q := model.DB.Model(table)
	if opts.ProjectID != "" {
		q = q.Where("project_id = ?", opts.ProjectID)
	}
	return q
}

// timePtr copies t so we can take its address safely in returned
// structs. (Most DB rows hand us value types; the Check struct
// wants a pointer so "no activity" serialises as null.)
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
