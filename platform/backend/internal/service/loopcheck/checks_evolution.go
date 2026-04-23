package loopcheck

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/a3c/platform/internal/model"
)

// analyzeRawThreshold is the minimum number of raw experiences
// before the Analyze Agent is worth running. Kept in sync with
// service.analyzeMinRawExperiences — when that knob changes, this
// threshold should too. Duplicated here (not imported) because the
// service package already imports loopcheck indirectly via the
// handler; importing back would create a cycle.
const analyzeRawThreshold = 10

// checkFeedbackToExperience counts Experience rows in the window.
// Breaks them down by source_type so you can see whether agent
// feedback (client-submitted) vs structured output (audit/fix/etc)
// are both flowing.
func checkFeedbackToExperience(opts Options, since time.Time) *Check {
	var total int64
	scoped(opts, &model.Experience{}).
		Where("created_at > ?", since).
		Count(&total)

	type row struct {
		SourceType string
		N          int64
	}
	var rows []row
	scoped(opts, &model.Experience{}).
		Select("source_type, COUNT(*) as n").
		Where("created_at > ?", since).
		Group("source_type").
		Scan(&rows)

	bySource := make(map[string]int64, len(rows))
	for _, r := range rows {
		bySource[r.SourceType] = r.N
	}

	var last time.Time
	scoped(opts, &model.Experience{}).
		Select("MAX(created_at)").
		Row().Scan(&last)

	// The "starved" threshold scales with the query window so that a
	// 1-day lookback can legitimately see only a handful of writes
	// without raising a stale flag. Rough intent: ~1 write per day
	// as the floor, capped at 10 so a 30-day window doesn't demand
	// 30+ writes before declaring the loop healthy.
	minHealthy := int64(opts.WindowDays)
	switch {
	case minHealthy < 1:
		minHealthy = 1
	case minHealthy > 10:
		minHealthy = 10
	}

	status := StatusHealthy
	var summary string
	switch {
	case total == 0:
		status = StatusUnused
		summary = fmt.Sprintf("No Experience writes in %dd — feedback/output tools are either unused or the write path is broken.", opts.WindowDays)
	case total < minHealthy:
		status = StatusStale
		summary = fmt.Sprintf("Only %d writes in %dd (expected ≥%d) — data starved.", total, opts.WindowDays, minHealthy)
	default:
		summary = fmt.Sprintf("%d Experience writes in %dd across %d source types.", total, opts.WindowDays, len(bySource))
	}

	return &Check{
		Name:    "feedback_to_experience",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"window_total":      total,
			"by_source_type":    bySource,
			"source_type_count": len(bySource),
			"min_healthy":       minHealthy,
		},
		LastActivity: timePtr(last),
	}
}

// checkExperienceToAnalyze tells you whether the Analyze Agent is
// keeping up with the raw Experience stream. Two failure modes:
// (a) raw pile is huge but nothing ever gets distilled — timer is
// silent or Analyze is failing; (b) nothing to distill because
// source feedback never arrives — upstream checkFeedbackToExperience
// would be "unused".
func checkExperienceToAnalyze(opts Options, since time.Time) *Check {
	var rawCount, distilledCount int64
	scoped(opts, &model.Experience{}).Where("status = ?", "raw").Count(&rawCount)
	scoped(opts, &model.Experience{}).Where("status = ?", "distilled").Count(&distilledCount)

	// Last Analyze session (distinct from last distilled row because
	// a run can finish with zero newly-distilled rows).
	var lastRun time.Time
	q := model.DB.Model(&model.AgentSession{}).Where("role = ?", "analyze")
	if opts.ProjectID != "" {
		q = q.Where("project_id = ?", opts.ProjectID)
	}
	q.Select("MAX(created_at)").Row().Scan(&lastRun)

	// Count successful vs failed Analyze sessions in the window.
	// We build two separate queries instead of cloning the base with
	// gorm.Session{} — it's a couple more lines but avoids the
	// subtle condition-stacking traps gorm's chainable API can
	// introduce.
	var okRuns, failedRuns int64
	okQ := model.DB.Model(&model.AgentSession{}).
		Where("role = ? AND created_at > ? AND status = ?", "analyze", since, "completed")
	failedQ := model.DB.Model(&model.AgentSession{}).
		Where("role = ? AND created_at > ? AND status = ?", "analyze", since, "failed")
	if opts.ProjectID != "" {
		okQ = okQ.Where("project_id = ?", opts.ProjectID)
		failedQ = failedQ.Where("project_id = ?", opts.ProjectID)
	}
	okQ.Count(&okRuns)
	failedQ.Count(&failedRuns)

	// For the failure-rate tests below we need enough runs to say
	// anything with confidence. 10 is arbitrary but matches
	// analyzeRawThreshold — if the system has run Analyze at least
	// as many times as it nominally "should have", the sample is
	// large enough to trust.
	totalRuns := okRuns + failedRuns
	var failureRate float64
	if totalRuns > 0 {
		failureRate = float64(failedRuns) / float64(totalRuns)
	}
	const minRunsForRatio = int64(10)

	status := StatusHealthy
	var summary string
	switch {
	case rawCount == 0 && distilledCount == 0:
		status = StatusUnused
		summary = "Experience table is empty — nothing upstream has produced feedback yet."
	case rawCount >= analyzeRawThreshold && lastRun.Before(since):
		status = StatusStale
		summary = fmt.Sprintf("%d raw experiences waiting; Analyze last ran more than %dd ago — timer may be wedged.", rawCount, opts.WindowDays)
	case failedRuns > 0 && okRuns == 0:
		status = StatusBroken
		summary = fmt.Sprintf("All %d Analyze runs in window failed.", failedRuns)
	case totalRuns >= minRunsForRatio && failureRate > 0.90:
		// Essentially broken — the few successes are likely noise.
		status = StatusBroken
		summary = fmt.Sprintf("%d/%d Analyze runs failed (%.0f%%). Skill/Policy/Refinery downstream cannot advance.",
			failedRuns, totalRuns, failureRate*100)
	case totalRuns >= minRunsForRatio && failureRate > 0.50:
		// Majority-fail — downstream will be starved even if it's
		// technically producing *something*. Worth alerting.
		status = StatusStale
		summary = fmt.Sprintf("%d/%d Analyze runs failed (%.0f%%) — downstream Skill/Policy pipeline is starved.",
			failedRuns, totalRuns, failureRate*100)
	case rawCount < analyzeRawThreshold && distilledCount == 0:
		status = StatusStale
		summary = fmt.Sprintf("Only %d raw experiences (threshold %d). Analyze has nothing worth running on.", rawCount, analyzeRawThreshold)
	default:
		summary = fmt.Sprintf("%d raw / %d distilled; %d Analyze runs OK, %d failed in %dd.", rawCount, distilledCount, okRuns, failedRuns, opts.WindowDays)
	}

	return &Check{
		Name:    "experience_to_analyze",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"raw_count":       rawCount,
			"distilled_count": distilledCount,
			"threshold":       analyzeRawThreshold,
			"runs_ok":         okRuns,
			"runs_failed":     failedRuns,
			"failure_rate":    failureRate,
		},
		LastActivity: timePtr(lastRun),
	}
}

// checkSkillToPolicy surfaces the skill-candidate funnel. A healthy
// system generates candidates regularly, has humans approving some
// fraction, and those approvals turn into active policies.
func checkSkillToPolicy(opts Options) *Check {
	// SkillCandidate lives in a table without project_id — skills
	// are cross-project in the current schema. We ignore
	// opts.ProjectID here and count platform-wide.
	var candidate, approved, active, rejected, deprecated int64
	model.DB.Model(&model.SkillCandidate{}).Where("status = ?", "candidate").Count(&candidate)
	model.DB.Model(&model.SkillCandidate{}).Where("status = ?", "approved").Count(&approved)
	model.DB.Model(&model.SkillCandidate{}).Where("status = ?", "active").Count(&active)
	model.DB.Model(&model.SkillCandidate{}).Where("status = ?", "rejected").Count(&rejected)
	model.DB.Model(&model.SkillCandidate{}).Where("status = ?", "deprecated").Count(&deprecated)

	var lastUpdated time.Time
	model.DB.Model(&model.SkillCandidate{}).Select("MAX(updated_at)").Row().Scan(&lastUpdated)

	total := candidate + approved + active + rejected + deprecated

	status := StatusHealthy
	var summary string
	switch {
	case total == 0:
		status = StatusUnused
		summary = "No SkillCandidate rows at all — Analyze has never produced anything to review."
	case candidate > 0 && approved == 0 && active == 0:
		status = StatusStale
		summary = fmt.Sprintf("%d candidates awaiting review; zero ever approved.", candidate)
	case active == 0:
		status = StatusStale
		summary = fmt.Sprintf("%d candidates, %d approved, none active — approval flow may be incomplete.", candidate, approved)
	default:
		summary = fmt.Sprintf("%d active skills; %d pending review.", active, candidate)
	}

	return &Check{
		Name:    "skill_to_policy",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"candidate":  candidate,
			"approved":   approved,
			"active":     active,
			"rejected":   rejected,
			"deprecated": deprecated,
			"total":      total,
		},
		LastActivity: timePtr(lastUpdated),
	}
}

// checkPolicyMatching looks at the runtime side of the policy
// table: are active policies actually getting hit? A pile of
// active-but-zero-hit policies means either the PolicyEngine is
// bypassed or matching conditions never match real tasks.
func checkPolicyMatching(opts Options) *Check {
	var active, candidate, deprecated int64
	model.DB.Model(&model.Policy{}).Where("status = ?", "active").Count(&active)
	model.DB.Model(&model.Policy{}).Where("status = ?", "candidate").Count(&candidate)
	model.DB.Model(&model.Policy{}).Where("status = ?", "deprecated").Count(&deprecated)

	// Total hits across all active policies.
	var totalHits struct{ N int64 }
	model.DB.Model(&model.Policy{}).Where("status = ?", "active").
		Select("COALESCE(SUM(hit_count), 0) as n").Scan(&totalHits)

	var zeroHitActive int64
	model.DB.Model(&model.Policy{}).
		Where("status = ? AND hit_count = 0", "active").Count(&zeroHitActive)

	var lastUpdated time.Time
	model.DB.Model(&model.Policy{}).Select("MAX(updated_at)").Row().Scan(&lastUpdated)

	status := StatusHealthy
	var summary string
	switch {
	case active+candidate+deprecated == 0:
		status = StatusUnused
		summary = "No policies at all — PolicyEngine has nothing to match against."
	case active == 0:
		status = StatusStale
		summary = fmt.Sprintf("%d candidates awaiting activation; zero policies are live.", candidate)
	case active > 0 && totalHits.N == 0:
		status = StatusStale
		summary = fmt.Sprintf("%d active policies but zero hits — tasks never match their conditions.", active)
	case zeroHitActive > active/2:
		status = StatusStale
		summary = fmt.Sprintf("%d/%d active policies have never matched anything.", zeroHitActive, active)
	default:
		summary = fmt.Sprintf("%d active policies, %d total hits.", active, totalHits.N)
	}

	return &Check{
		Name:    "policy_matching",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"active":          active,
			"candidate":       candidate,
			"deprecated":      deprecated,
			"total_hits":      totalHits.N,
			"zero_hit_active": zeroHitActive,
		},
		LastActivity: timePtr(lastUpdated),
	}
}

// checkArtifactInjection surfaces the RRF-driven retrieval half of
// self-evolution. Counts are on KnowledgeArtifact because that's
// the thing that gets "used" and graded. The inverse — how often a
// task actually received injected artifacts — lives on AgentSession
// and is harder to turn into a single number, so we stick with
// artifact-side stats here.
func checkArtifactInjection(opts Options, since time.Time) *Check {
	// KnowledgeArtifact has a project_id (empty = cross-project).
	// We report cross-project + per-project figures separately only
	// when opts.ProjectID is set, otherwise everything is global.
	var total, active, withEmbedding int64
	scoped(opts, &model.KnowledgeArtifact{}).Count(&total)
	scoped(opts, &model.KnowledgeArtifact{}).Where("status = ?", "active").Count(&active)
	scoped(opts, &model.KnowledgeArtifact{}).Where("embedding_dim > 0").Count(&withEmbedding)

	// Aggregate usage across all artifacts in scope. These counters
	// are bumped by HandleChangeAudit (success=L0, failure=L2) and
	// by the injection step (usage_count).
	var usage, success, failure int64
	row := scoped(opts, &model.KnowledgeArtifact{}).
		Select("COALESCE(SUM(usage_count),0), COALESCE(SUM(success_count),0), COALESCE(SUM(failure_count),0)").
		Row()
	row.Scan(&usage, &success, &failure)

	// "Recently used" = LastUsedAt > since, a cheap freshness test.
	var recentlyUsed int64
	scoped(opts, &model.KnowledgeArtifact{}).
		Where("last_used_at IS NOT NULL AND last_used_at > ?", since).
		Count(&recentlyUsed)

	var lastUsed time.Time
	scoped(opts, &model.KnowledgeArtifact{}).
		Select("MAX(last_used_at)").Row().Scan(&lastUsed)

	status := StatusHealthy
	var summary string
	netPositive := success - failure
	switch {
	case total == 0:
		status = StatusUnused
		summary = "No KnowledgeArtifact rows — Refinery has never produced anything."
	case active == 0:
		status = StatusStale
		summary = fmt.Sprintf("%d artifacts exist but zero are active; nothing will be injected.", total)
	case usage == 0:
		status = StatusStale
		summary = fmt.Sprintf("%d active artifacts, but none have ever been injected into a prompt.", active)
	case recentlyUsed == 0:
		status = StatusStale
		summary = fmt.Sprintf("%d active artifacts; zero used in last %dd — injection path may have regressed.", active, opts.WindowDays)
	case failure > success:
		status = StatusBroken
		summary = fmt.Sprintf("Net outcome negative: %d successes vs %d failures — artifacts are hurting more than helping.", success, failure)
	default:
		summary = fmt.Sprintf("%d active, %d used recently, net outcome +%d (s=%d f=%d).", active, recentlyUsed, netPositive, success, failure)
	}

	return &Check{
		Name:    "artifact_injection",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"total":          total,
			"active":         active,
			"with_embedding": withEmbedding,
			"usage_count":    usage,
			"success_count":  success,
			"failure_count":  failure,
			"recently_used":  recentlyUsed,
			"net_outcome":    netPositive,
		},
		LastActivity: timePtr(lastUsed),
	}
}

// checkRefineryPipeline looks at RefineryRun rows to verify the
// weekly knowledge-distillation pipeline actually ran and produced
// something.
func checkRefineryPipeline(opts Options, since time.Time) *Check {
	// Refinery is scoped either to a project or cross-project
	// (project_id=''). When opts.ProjectID is set we include both
	// so the user sees the cross-project global-promotion runs too.
	newRefineryQ := func() *gorm.DB {
		q := model.DB.Model(&model.RefineryRun{}).Where("started_at > ?", since)
		if opts.ProjectID != "" {
			q = q.Where("project_id = ? OR project_id = ''", opts.ProjectID)
		}
		return q
	}
	var totalRuns, okRuns, failedRuns int64
	newRefineryQ().Count(&totalRuns)
	newRefineryQ().Where("status = ?", "ok").Count(&okRuns)
	newRefineryQ().Where("status IN ?", []string{"failed", "partial"}).Count(&failedRuns)

	var lastRun time.Time
	baseLast := model.DB.Model(&model.RefineryRun{})
	if opts.ProjectID != "" {
		baseLast = baseLast.Where("project_id = ? OR project_id = ''", opts.ProjectID)
	}
	baseLast.Select("MAX(started_at)").Row().Scan(&lastRun)

	status := StatusHealthy
	var summary string
	switch {
	case totalRuns == 0 && lastRun.IsZero():
		status = StatusUnused
		summary = "Refinery has never run — timer may not have fired yet (it fires weekly) or has no eligible project."
	case totalRuns == 0 && !lastRun.IsZero():
		status = StatusStale
		summary = fmt.Sprintf("No Refinery runs in %dd; last ever was %s.", opts.WindowDays, lastRun.Format("2006-01-02"))
	case failedRuns > 0 && okRuns == 0:
		status = StatusBroken
		summary = fmt.Sprintf("All %d Refinery runs in window failed or partial.", failedRuns)
	default:
		summary = fmt.Sprintf("%d runs in %dd (%d ok, %d failed/partial).", totalRuns, opts.WindowDays, okRuns, failedRuns)
	}

	return &Check{
		Name:    "refinery_pipeline",
		Status:  status,
		Summary: summary,
		Metrics: map[string]any{
			"runs_total":         totalRuns,
			"runs_ok":            okRuns,
			"runs_failed":        failedRuns,
		},
		LastActivity: timePtr(lastRun),
	}
}
