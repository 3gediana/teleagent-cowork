package loopcheck

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/a3c/platform/internal/model"
)

// checkHeartbeat reports on the heartbeat-driven resource release
// subsystem. Two numbers matter: how many agents are online right
// now (coarse liveness) and how many have timed out recently (the
// scheduler must be actually running to produce these transitions).
//
// Detecting "timed out recently" is imprecise because the scheduler
// doesn't log a timeout event — it just flips the agent's Status
// field and releases locks. We approximate by counting agents whose
// Status=='offline' but whose LastHeartbeat is within the window,
// which means the background loop saw them and expired them.
func checkHeartbeat(opts Options) *Check {
	var online, offline, offlineRecently int64

	onlineQ := model.DB.Model(&model.Agent{}).Where("status = ?", "online")
	if opts.ProjectID != "" {
		onlineQ = onlineQ.Where("current_project_id = ?", opts.ProjectID)
	}
	onlineQ.Count(&online)

	offlineQ := model.DB.Model(&model.Agent{}).Where("status = ?", "offline")
	if opts.ProjectID != "" {
		offlineQ = offlineQ.Where("current_project_id = ?", opts.ProjectID)
	}
	offlineQ.Count(&offline)

	// Recently-expired agents: flipped to offline AND last heartbeat
	// within the window.
	since := time.Now().Add(-time.Duration(opts.WindowDays) * 24 * time.Hour)
	recentExpiredQ := model.DB.Model(&model.Agent{}).
		Where("status = ? AND last_heartbeat IS NOT NULL AND last_heartbeat > ?", "offline", since)
	if opts.ProjectID != "" {
		recentExpiredQ = recentExpiredQ.Where("current_project_id = ?", opts.ProjectID)
	}
	recentExpiredQ.Count(&offlineRecently)

	var lastHB time.Time
	lastHBQ := model.DB.Model(&model.Agent{})
	if opts.ProjectID != "" {
		lastHBQ = lastHBQ.Where("current_project_id = ?", opts.ProjectID)
	}
	lastHBQ.Select("MAX(last_heartbeat)").Row().Scan(&lastHB)

	status := StatusHealthy
	var summary string
	switch {
	case online == 0 && offline == 0:
		status = StatusUnused
		summary = "No agents ever registered."
	case online == 0:
		status = StatusStale
		summary = fmt.Sprintf("All %d agents offline; no live clients.", offline)
	default:
		summary = fmt.Sprintf("%d online, %d offline (%d expired in last %dd).", online, offline, offlineRecently, opts.WindowDays)
	}

	return &Check{
		Name:    "heartbeat",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"online":           online,
			"offline":          offline,
			"offline_recently": offlineRecently,
		},
		LastActivity: timePtr(lastHB),
	}
}

// checkTimerActivity infers whether the four in-process timers
// (Maintain every 20min, Heartbeat every 1min, Analyze every 24h,
// Refinery weekly) are actually ticking by looking for DB side
// effects the timers produce:
//
//   Maintain → AgentSession with trigger_reason='periodic_20min'
//   Analyze  → AgentSession with role='analyze' (any trigger)
//   Refinery → RefineryRun with trigger='scheduled' or
//              'scheduled_global'
//   Heartbeat → implicit in checkHeartbeat; we don't double-count
//
// A timer is "healthy" if we've seen activity within a reasonable
// multiple of its period; "stale" if the most recent activity is
// much older than expected; "unused" if there has never been one.
func checkTimerActivity(opts Options, since time.Time) *Check {
	now := time.Now()

	// --- Maintain (20min cadence, expect one every < 1h) ---
	var lastMaintain time.Time
	mQ := model.DB.Model(&model.AgentSession{}).
		Where("role = ? AND trigger_reason = ?", "maintain", "periodic_20min")
	if opts.ProjectID != "" {
		mQ = mQ.Where("project_id = ?", opts.ProjectID)
	}
	mQ.Select("MAX(created_at)").Row().Scan(&lastMaintain)

	// --- Analyze (24h cadence, expect one every < 48h) ---
	var lastAnalyze time.Time
	aQ := model.DB.Model(&model.AgentSession{}).Where("role = ?", "analyze")
	if opts.ProjectID != "" {
		aQ = aQ.Where("project_id = ?", opts.ProjectID)
	}
	aQ.Select("MAX(created_at)").Row().Scan(&lastAnalyze)

	// --- Refinery (weekly, expect one every < 10 days) ---
	var lastRefinery time.Time
	rQ := model.DB.Model(&model.RefineryRun{}).
		Where("trigger LIKE ?", "scheduled%")
	if opts.ProjectID != "" {
		rQ = rQ.Where("project_id = ? OR project_id = ''", opts.ProjectID)
	}
	rQ.Select("MAX(started_at)").Row().Scan(&lastRefinery)

	// Classification per timer.
	maintainOK := !lastMaintain.IsZero() && now.Sub(lastMaintain) < 2*time.Hour
	analyzeSeen := !lastAnalyze.IsZero()
	// Analyze runs only when there are ≥10 raw experiences, so being
	// silent is not automatically bad; report "stale" only when
	// there IS plenty to analyse (checked in checkExperienceToAnalyze).
	refinerySeen := !lastRefinery.IsZero()

	status := StatusHealthy
	var summary string
	switch {
	case lastMaintain.IsZero() && lastAnalyze.IsZero() && lastRefinery.IsZero():
		status = StatusUnused
		summary = "None of the three timers have produced any DB side effects yet — server may have just started."
	case !maintainOK && !lastMaintain.IsZero():
		status = StatusStale
		summary = fmt.Sprintf("Maintain timer last fired %s ago (expected < 1h).", humanAgo(now.Sub(lastMaintain)))
	default:
		parts := []string{}
		if maintainOK {
			parts = append(parts, "Maintain✓")
		} else if lastMaintain.IsZero() {
			parts = append(parts, "Maintain•never")
		}
		if analyzeSeen {
			parts = append(parts, fmt.Sprintf("Analyze %s ago", humanAgo(now.Sub(lastAnalyze))))
		} else {
			parts = append(parts, "Analyze•never")
		}
		if refinerySeen {
			parts = append(parts, fmt.Sprintf("Refinery %s ago", humanAgo(now.Sub(lastRefinery))))
		} else {
			parts = append(parts, "Refinery•never")
		}
		summary = concat(parts)
	}

	return &Check{
		Name:    "timer_activity",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"last_maintain_ago_seconds": ageSeconds(lastMaintain),
			"last_analyze_ago_seconds":  ageSeconds(lastAnalyze),
			"last_refinery_ago_seconds": ageSeconds(lastRefinery),
		},
		LastActivity: timePtr(maxTime(lastMaintain, lastAnalyze, lastRefinery)),
	}
}

// checkChiefAutoApproval measures AutoMode usage: how many projects
// have AutoMode=true, and how many Chief sessions were triggered
// by PR approval events in the window. "Unused" means the feature
// is wired but nobody has opted in.
func checkChiefAutoApproval(opts Options, since time.Time) *Check {
	var autoModeProjects int64
	amQ := model.DB.Model(&model.Project{}).Where("auto_mode = ?", true)
	if opts.ProjectID != "" {
		amQ = amQ.Where("id = ?", opts.ProjectID)
	}
	amQ.Count(&autoModeProjects)

	var chiefSessionsInWindow int64
	cQ := model.DB.Model(&model.AgentSession{}).
		Where("role = ? AND created_at > ?", "chief", since)
	if opts.ProjectID != "" {
		cQ = cQ.Where("project_id = ?", opts.ProjectID)
	}
	cQ.Count(&chiefSessionsInWindow)

	var chiefApprovals int64
	chiefApprovalsQ := model.DB.Model(&model.AgentSession{}).
		Where("role = ? AND created_at > ? AND (trigger_reason LIKE ? OR trigger_reason LIKE ?)",
			"chief", since, "pr_approval%", "approve_%")
	if opts.ProjectID != "" {
		chiefApprovalsQ = chiefApprovalsQ.Where("project_id = ?", opts.ProjectID)
	}
	chiefApprovalsQ.Count(&chiefApprovals)

	var lastChief time.Time
	lastQ := model.DB.Model(&model.AgentSession{}).Where("role = ?", "chief")
	if opts.ProjectID != "" {
		lastQ = lastQ.Where("project_id = ?", opts.ProjectID)
	}
	lastQ.Select("MAX(created_at)").Row().Scan(&lastChief)

	// Sample-size floor below which a zero-approval reading isn't
	// meaningful. Chief sessions are relatively frequent when the
	// platform is busy, so asking for at least 5 in window before
	// calling "0 approvals" a problem is conservative.
	const minChiefSessionsForStale = int64(5)

	status := StatusHealthy
	var summary string
	switch {
	case autoModeProjects == 0 && chiefSessionsInWindow == 0:
		status = StatusUnused
		summary = "No projects have AutoMode on, and Chief has never been called. Feature is wired but unused."
	case autoModeProjects == 0:
		status = StatusHealthy
		summary = fmt.Sprintf("Chief ran %d times in %dd (all manual-mode chats; no AutoMode projects).", chiefSessionsInWindow, opts.WindowDays)
	case autoModeProjects > 0 && chiefApprovals == 0 && chiefSessionsInWindow < minChiefSessionsForStale:
		// Too few Chief sessions to tell whether the approval path
		// is broken. Don't raise a stale flag yet — the natural
		// next Chief session will settle it.
		status = StatusHealthy
		summary = fmt.Sprintf("%d AutoMode project(s); %d Chief session(s) in %dd, no approvals yet (small sample, needs ≥%d to assess).",
			autoModeProjects, chiefSessionsInWindow, opts.WindowDays, minChiefSessionsForStale)
	case autoModeProjects > 0 && chiefApprovals == 0:
		status = StatusStale
		summary = fmt.Sprintf("%d AutoMode project(s) with %d Chief session(s) in %dd but zero approvals — check auto-approval wiring.",
			autoModeProjects, chiefSessionsInWindow, opts.WindowDays)
	default:
		summary = fmt.Sprintf("%d AutoMode project(s); Chief did %d approvals in %dd (of %d total Chief sessions).", autoModeProjects, chiefApprovals, opts.WindowDays, chiefSessionsInWindow)
	}

	return &Check{
		Name:    "chief_auto_approval",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"auto_mode_projects":    autoModeProjects,
			"chief_sessions_window": chiefSessionsInWindow,
			"chief_approvals":       chiefApprovals,
		},
		LastActivity: timePtr(lastChief),
	}
}

// checkRetryMechanism surfaces how often the retry loop actually
// fired and whether retries eventually succeeded — if all retries
// still fail, the retry is just wasted tokens.
func checkRetryMechanism(opts Options, since time.Time) *Check {
	var sessionsWithRetries int64
	rQ := model.DB.Model(&model.AgentSession{}).
		Where("retry_count > 0 AND created_at > ?", since)
	if opts.ProjectID != "" {
		rQ = rQ.Where("project_id = ?", opts.ProjectID)
	}
	rQ.Count(&sessionsWithRetries)

	var succeededAfterRetry int64
	sQ := model.DB.Model(&model.AgentSession{}).
		Where("retry_count > 0 AND status = ? AND created_at > ?", "completed", since)
	if opts.ProjectID != "" {
		sQ = sQ.Where("project_id = ?", opts.ProjectID)
	}
	sQ.Count(&succeededAfterRetry)

	var failedAfterRetry int64
	fQ := model.DB.Model(&model.AgentSession{}).
		Where("retry_count > 0 AND status = ? AND created_at > ?", "failed", since)
	if opts.ProjectID != "" {
		fQ = fQ.Where("project_id = ?", opts.ProjectID)
	}
	fQ.Count(&failedAfterRetry)

	// Recovery rate = recovered / (recovered + ultimately-failed).
	// "sessions_with_retries" (the numerator of retries-that-
	// happened) can be larger than the other two combined because
	// some sessions might still be running; exclude those from the
	// rate to avoid counting them as failures.
	resolved := succeededAfterRetry + failedAfterRetry
	var recoveryRate float64
	if resolved > 0 {
		recoveryRate = float64(succeededAfterRetry) / float64(resolved)
	}
	const minForRatio = int64(10)

	status := StatusHealthy
	var summary string
	switch {
	case sessionsWithRetries == 0:
		status = StatusHealthy
		summary = "No retries needed in window — no transient failures (or no traffic)."
	case resolved >= minForRatio && recoveryRate < 0.10:
		// <10% of retries eventually succeed — the retry policy is
		// burning tokens on permanently-failing jobs.
		status = StatusBroken
		summary = fmt.Sprintf("%d retries resolved; only %d recovered (%.0f%%). Retry policy is burning tokens on permanently-failing jobs.",
			resolved, succeededAfterRetry, recoveryRate*100)
	case resolved >= minForRatio && recoveryRate < 0.40:
		// 10-40% recovery is noticeably worse than a well-tuned
		// transient-retry policy should achieve (>70% is typical
		// for network/timeout-bucket retries). Flag as stale so
		// the operator investigates.
		status = StatusStale
		summary = fmt.Sprintf("%d retries resolved; %d recovered (%.0f%%). Recovery rate is low — retries may be firing on non-transient errors.",
			resolved, succeededAfterRetry, recoveryRate*100)
	case succeededAfterRetry == 0 && failedAfterRetry > 0:
		status = StatusBroken
		summary = fmt.Sprintf("%d retries, all still failed — retry policy is wasting tokens.", failedAfterRetry)
	default:
		summary = fmt.Sprintf("%d retries (%d recovered, %d ultimately failed).", sessionsWithRetries, succeededAfterRetry, failedAfterRetry)
	}

	return &Check{
		Name:    "retry_mechanism",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"sessions_with_retries": sessionsWithRetries,
			"recovered":             succeededAfterRetry,
			"still_failed":          failedAfterRetry,
			"recovery_rate":         recoveryRate,
		},
	}
}

// checkFailureModes breaks Change rows with non-empty failure_mode
// down by kind. Useful for spotting the top contributor — if
// missing_context dominates, improving prompt injection might be a
// higher-leverage fix than improving the Audit Agent.
func checkFailureModes(opts Options, since time.Time) *Check {
	type row struct {
		FailureMode string
		N           int64
	}
	var rows []row
	q := model.DB.Model(&model.Change{}).
		Select("failure_mode, COUNT(*) as n").
		Where("failure_mode != '' AND created_at > ?", since).
		Group("failure_mode")
	if opts.ProjectID != "" {
		q = q.Where("project_id = ?", opts.ProjectID)
	}
	q.Scan(&rows)

	byMode := make(map[string]int64, len(rows))
	var total int64
	var dominant string
	var dominantN int64
	for _, r := range rows {
		byMode[r.FailureMode] = r.N
		total += r.N
		if r.N > dominantN {
			dominantN = r.N
			dominant = r.FailureMode
		}
	}

	// Total Changes for a denominator (so the summary can say "8%
	// of changes have a recorded failure mode" rather than a naked
	// count).
	var totalChanges int64
	tcQ := model.DB.Model(&model.Change{}).Where("created_at > ?", since)
	if opts.ProjectID != "" {
		tcQ = tcQ.Where("project_id = ?", opts.ProjectID)
	}
	tcQ.Count(&totalChanges)

	status := StatusHealthy
	var summary string
	switch {
	case totalChanges == 0:
		status = StatusUnused
		summary = fmt.Sprintf("No Changes in %dd — can't classify failure modes.", opts.WindowDays)
	case total == 0:
		summary = fmt.Sprintf("%d changes in %dd, none with recorded failure_mode.", totalChanges, opts.WindowDays)
	default:
		pct := float64(total) / float64(totalChanges) * 100
		summary = fmt.Sprintf("%.0f%% (%d/%d) carry a failure_mode; top: %s (%d).", pct, total, totalChanges, dominant, dominantN)
	}

	return &Check{
		Name:    "failure_modes",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"total_changes_window": totalChanges,
			"with_failure_mode":    total,
			"by_failure_mode":      byMode,
			"dominant":             dominant,
		},
	}
}

// --- small helpers kept local because they read better inline ---

func humanAgo(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func ageSeconds(t time.Time) int64 {
	if t.IsZero() {
		return -1
	}
	return int64(time.Since(t).Seconds())
}

func maxTime(ts ...time.Time) time.Time {
	var m time.Time
	for _, t := range ts {
		if t.After(m) {
			m = t
		}
	}
	return m
}

func concat(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// Compile-time check: the heartbeat tx must eventually release
// resources, including branch occupants. We don't test that here —
// the service package owns the scheduler itself — but we do want to
// ensure the gorm symbol is actually referenced somewhere in this
// file so imports stay honest even if future edits thin checks out.
var _ = (*gorm.DB)(nil)
