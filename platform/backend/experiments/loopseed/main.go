// Command loopseed — lights up every downstream loop the loopcheck
// diagnostic knows about, so operators can see what a populated
// Self-Evolution pipeline looks like end-to-end without waiting the
// real (days-to-weeks) cadence that feeds it in production.
//
// It does three things against whichever DB the --dbname flag points
// at (defaults to a3c_live because that's where planninglive leaves
// its artefacts):
//
//  1. Runs the real refinery.Run() pass set against the project's
//     agent_session + experience history. This produces live
//     RefineryRun rows and (when the passes find enough signal)
//     real KnowledgeArtifact rows — nothing synthetic about step 1.
//
//  2. If step 1 didn't yield a KnowledgeArtifact (small demo data,
//     5-session floor), inserts one synthetic KnowledgeArtifact and
//     bumps its usage/success counters so the artifact_injection
//     check has something to classify as healthy.
//
//  3. Seeds the SkillCandidate → Policy chain directly: one row in
//     each of (candidate, approved, active) status plus a single
//     active Policy with hit_count=3. This proves the four
//     skill/policy checks can produce healthy output; the live
//     Analyze runs that would normally populate them are a 24h
//     timer and we don't want to block a demo on that.
//
// The tool is explicitly additive — it never drops or truncates.
// Re-running it will keep stacking seeds until you drop+recreate the
// DB manually (which is the pattern planninglive itself follows).
//
// Usage:
//
//	cd platform/backend
//	go run ./experiments/loopseed --dbname a3c_live --project proj_live_planner
//	go run ./experiments/loopcheck --dbname a3c_live --window 1
//
// The second command should now show every downstream check flipping
// from '·  unused' to '✓  healthy' (or 'stale' for the policy matching
// check — active with hit_count is healthy, but an active one with
// zero hits would rightly be stale).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service/refinery"
)

func main() {
	var (
		configPath = flag.String("config", "", "Optional config file override; defaults to configs/config.yaml")
		dbName     = flag.String("dbname", "a3c_live", "Target database. Defaults to a3c_live (planninglive's target).")
		projectID  = flag.String("project", "proj_live_planner", "Project ID to attach the seed to")
		lookbackH  = flag.Int("lookback-hours", 24*14, "Lookback window for refinery passes")
	)
	flag.Parse()

	cfg := config.Load(*configPath)
	cfg.Database.DBName = *dbName
	if err := model.InitDB(&cfg.Database); err != nil {
		log.Fatalf("database init (%s) failed: %v", *dbName, err)
	}
	fmt.Printf("→ target database: %s\n", *dbName)
	fmt.Printf("→ target project:  %s\n", *projectID)

	// Sanity — the project must exist. Creating one on the fly would
	// be convenient but also invite silent typos; fail loud.
	var project model.Project
	if err := model.DB.Where("id = ?", *projectID).First(&project).Error; err != nil {
		log.Fatalf("project %s not found in %s: %v\n\nHint: run experiments/planninglive first to seed a demo project, or pass --project <existing_id>", *projectID, *dbName, err)
	}
	fmt.Printf("→ project status:  %s\n\n", project.Status)

	section("Phase 1: real refinery run")
	runRealRefinery(*projectID, *lookbackH)

	section("Phase 2: KnowledgeArtifact top-up")
	ensureKnowledgeArtifact(*projectID)

	section("Phase 3: Skill → Policy chain seed")
	seedSkillsAndPolicy(*projectID)

	section("Done")
	printFinalCounts(*projectID)

	fmt.Printf("\nNext step — run the diagnostic:\n")
	fmt.Printf("    go run ./experiments/loopcheck --dbname %s --window 1\n\n", *dbName)
}

func section(title string) {
	fmt.Printf("\n── %s %s\n", title, strings.Repeat("─", 60-len(title)))
}

// runRealRefinery kicks off refinery.New()'s full pass set. Even if
// the project has only a handful of sessions, calling Run() gives us
// (a) a RefineryRun row for the refinery_pipeline check, and (b) a
// best-effort chance the PatternExtractor finds something real.
func runRealRefinery(projectID string, lookbackHours int) {
	r := refinery.New()
	run, err := r.Run(projectID, lookbackHours, "loopseed")
	if err != nil {
		log.Fatalf("refinery.Run failed: %v", err)
	}
	fmt.Printf("  RefineryRun %s status=%s duration=%dms\n",
		run.ID, run.Status, run.DurationMs)
	if run.PassStats != "" && run.PassStats != "{}" {
		var stats map[string]any
		_ = json.Unmarshal([]byte(run.PassStats), &stats)
		for name, raw := range stats {
			b, _ := json.Marshal(raw)
			fmt.Printf("    pass %-24s %s\n", name, string(b))
		}
	}
}

// ensureKnowledgeArtifact guarantees the project has at least one
// active KnowledgeArtifact with non-zero usage_count, so the
// artifact_injection check has material to report. If refinery
// already produced one (real passes hit), the synthetic row is
// still added — the check is count-based, and having two is fine
// for a demo seed.
func ensureKnowledgeArtifact(projectID string) {
	var existing int64
	model.DB.Model(&model.KnowledgeArtifact{}).
		Where("project_id = ?", projectID).Count(&existing)
	fmt.Printf("  existing KnowledgeArtifact rows for project: %d\n", existing)

	now := time.Now()
	lastUsed := now.Add(-2 * time.Hour)
	ka := &model.KnowledgeArtifact{
		ID:           model.GenerateID("ka"),
		ProjectID:    projectID,
		Kind:         "tool_recipe",
		Name:         "commit small focused changes",
		Summary:      "When a fix touches multiple unrelated files, split it into separate commits so Audit_1 can reason about each concern in isolation.",
		Payload:      `{"max_files_per_commit":3,"example":"audit tool"}`,
		ProducedBy:   "loopseed/v1",
		SourceEvents: "[]",
		Confidence:   0.82,
		HitCount:     4,
		UsageCount:   5,
		SuccessCount: 4,
		FailureCount: 1,
		Status:       "active",
		Version:      1,
		LastUsedAt:   &lastUsed,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := model.DB.Create(ka).Error; err != nil {
		log.Fatalf("create KnowledgeArtifact: %v", err)
	}
	fmt.Printf("  seeded KnowledgeArtifact %s (usage=5 success=4 failure=1)\n", ka.ID)
}

// seedSkillsAndPolicy creates a realistic snapshot of the
// Skill/Policy chain: one row in each reviewable status, plus one
// active Policy with a handful of hits. This is the only place
// loopseed fabricates data outright — if you want the real Analyze
// output path, let the daily timer run a few cycles against real
// feedback.
func seedSkillsAndPolicy(projectID string) {
	now := time.Now()

	skills := []model.SkillCandidate{
		{
			ID:             model.GenerateID("skill"),
			Name:           "prefer structured audit findings",
			Type:           "prompt",
			ApplicableTags: `["audit","quality"]`,
			Precondition:   "Audit agent is reviewing a change with >3 files",
			Action:         "Emit findings as a bullet list keyed by file path.",
			Prohibition:    "Do not return prose paragraphs spanning multiple files.",
			Evidence:       "6/8 of the last Audit sessions that returned structured bullets yielded actionable fixes on first try.",
			SourceCaseIDs:  "[]",
			Status:         "candidate",
			Version:        1,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		{
			ID:             model.GenerateID("skill"),
			Name:           "escalate layered deps to human",
			Type:           "routing",
			ApplicableTags: `["dependency","escalation"]`,
			Precondition:   "Change touches package.json or go.mod AND adds > 1 dep",
			Action:         "Route PR to human review regardless of AutoMode.",
			Prohibition:    "Never auto-merge multi-dep additions, even trivial ones.",
			Evidence:       "Previously auto-merged multi-dep PRs caused 3 rollbacks in prior quarter.",
			SourceCaseIDs:  "[]",
			Status:         "approved",
			ApprovedBy:     "seed",
			Version:        1,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		{
			ID:             model.GenerateID("skill"),
			Name:           "short summary before full answer",
			Type:           "process",
			ApplicableTags: `["chief","dialogue"]`,
			Precondition:   "Chief is replying in a multi-turn dialogue where the latest user turn is a question",
			Action:         "Open the reply with a one-sentence summary before any detail.",
			Prohibition:    "Do not lead with tool plan output or raw state dumps.",
			Evidence:       "Operators marked these replies 'useful' 4.6/5 vs 3.1/5 for non-summarised ones.",
			SourceCaseIDs:  "[]",
			Status:         "active",
			ApprovedBy:     "seed",
			Version:        1,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
	}
	for _, s := range skills {
		if err := model.DB.Create(&s).Error; err != nil {
			log.Fatalf("create SkillCandidate %s: %v", s.Name, err)
		}
		fmt.Printf("  seeded SkillCandidate %-10s %s\n", s.Status, s.Name)
	}

	// One active Policy with hits — matches policy_matching's "active
	// with non-zero hits = healthy" branch.
	pol := &model.Policy{
		ID:             model.GenerateID("pol"),
		Name:           "large PR requires human approval",
		MatchCondition: `{"scope":"pr_review","file_count_gt":10}`,
		Actions:        `{"require_human":true,"warn":"large diff"}`,
		Priority:       10,
		Status:         "active",
		Source:         "analyze",
		HitCount:       3,
		SuccessRate:    0.67,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := model.DB.Create(pol).Error; err != nil {
		log.Fatalf("create Policy: %v", err)
	}
	fmt.Printf("  seeded Policy     active     %s (hits=%d)\n", pol.Name, pol.HitCount)
}

func printFinalCounts(projectID string) {
	type row struct {
		Label string
		N     int64
	}
	var out []row
	add := func(label string, q *gorm.DB) {
		var n int64
		q.Count(&n)
		out = append(out, row{label, n})
	}

	add("Experience (project)",
		model.DB.Model(&model.Experience{}).Where("project_id = ?", projectID))
	add("SkillCandidate (all statuses)",
		model.DB.Model(&model.SkillCandidate{}))
	add("Policy (active)",
		model.DB.Model(&model.Policy{}).Where("status = ?", "active"))
	add("KnowledgeArtifact (project, active)",
		model.DB.Model(&model.KnowledgeArtifact{}).Where("project_id = ? AND status = ?", projectID, "active"))
	add("RefineryRun (project)",
		model.DB.Model(&model.RefineryRun{}).Where("project_id = ?", projectID))

	fmt.Println()
	for _, r := range out {
		fmt.Printf("  %-40s  %d\n", r.Label+":", r.N)
	}

	// Safety surface: if something went wrong, fail loudly rather than
	// quietly reporting zeros.
	for _, r := range out {
		if r.N == 0 {
			fmt.Fprintf(os.Stderr, "\n⚠ %s is still zero — loopcheck will still show unused for that pillar.\n", r.Label)
		}
	}
}
