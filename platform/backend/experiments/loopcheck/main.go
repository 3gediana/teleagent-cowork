// Command loopcheck — CLI diagnostic that prints the health of every
// self-evolution and automation loop in the platform.
//
// This is the operator-facing sibling of /api/v1/internal/loopcheck;
// both call into internal/service/loopcheck and render the same
// Report. Use this one when:
//
//   - the HTTP server isn't running
//   - you want to script it from CI or cron
//   - you're doing a one-off "what's the state of things?" poke
//
// It only reads the DB (no writes, no LLM, no outbound HTTP). Safe
// to run against a live production database.
//
// Usage:
//   cd platform/backend
//   go run ./experiments/loopcheck                    # platform-wide, 7d window
//   go run ./experiments/loopcheck --project <id>     # single project
//   go run ./experiments/loopcheck --window 30        # 30-day window
//   go run ./experiments/loopcheck --json             # machine-readable
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service/loopcheck"
)

func main() {
	var (
		projectID  = flag.String("project", "", "Restrict to a single project (omit for platform-wide)")
		windowDays = flag.Int("window", 7, "Look-back window in days for recent-activity counts")
		asJSON     = flag.Bool("json", false, "Emit JSON instead of a terminal report")
		configPath = flag.String("config", "", "Optional config file override; defaults to configs/config.yaml")
		useSQLite  = flag.Bool("sqlite", false, "Skip MySQL and run against an empty in-memory SQLite (demo/smoke mode; every check will report 'unused')")
	)
	flag.Parse()

	if *useSQLite {
		if err := bootstrapSQLite(); err != nil {
			log.Fatalf("sqlite bootstrap failed: %v", err)
		}
	} else {
		cfg := config.Load(*configPath)
		if err := model.InitDB(&cfg.Database); err != nil {
			log.Fatalf("database init failed: %v\n\nTip: pass --sqlite to run against an empty in-memory DB without MySQL.", err)
		}
	}

	report := loopcheck.Generate(loopcheck.Options{
		ProjectID:  *projectID,
		WindowDays: *windowDays,
	})

	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			log.Fatalf("json encode failed: %v", err)
		}
		return
	}

	renderReport(os.Stdout, report)
}

// renderReport prints a human-readable, ANSI-coloured report to w.
// The layout is deliberately narrow (< 80 chars) so it reads well
// in a ssh-over-phone-hotspot window. We don't bother detecting
// whether stdout is a TTY; if it's piped somewhere, just use --json
// instead.
func renderReport(w *os.File, r *loopcheck.Report) {
	fmt.Fprintln(w)
	header(w, "A3C LOOP HEALTH CHECK", r.OverallStatus)
	scope := "platform-wide"
	if r.ProjectID != "" {
		scope = "project " + r.ProjectID
	}
	fmt.Fprintf(w, "  scope: %s    window: %dd    generated: %s\n\n",
		scope, r.WindowDays, r.GeneratedAt.Format("2006-01-02 15:04:05"))

	loopBlock(w, "🧬  SELF-EVOLUTION", r.SelfEvolution)
	fmt.Fprintln(w)
	loopBlock(w, "🤖  AUTOMATION", r.Automation)
	fmt.Fprintln(w)

	footer(w, r)
}

// header prints the top banner with an overall status badge.
func header(w *os.File, title string, s loopcheck.Status) {
	badge := statusBadge(s)
	fmt.Fprintln(w, strings.Repeat("─", 72))
	fmt.Fprintf(w, "  %s   %s\n", title, badge)
	fmt.Fprintln(w, strings.Repeat("─", 72))
}

func loopBlock(w *os.File, title string, loop loopcheck.Loop) {
	fmt.Fprintf(w, "%s   [%s]\n", title, statusBadge(loop.OverallStatus))
	for _, c := range loop.Checks {
		renderCheck(w, c)
	}
}

func renderCheck(w *os.File, c *loopcheck.Check) {
	fmt.Fprintf(w, "  %s  %-28s  %s\n",
		statusIcon(c.Status), c.Name, c.Summary)
	if c.LastActivity != nil {
		ago := time.Since(*c.LastActivity).Truncate(time.Second)
		fmt.Fprintf(w, "      last activity: %s (%s ago)\n",
			c.LastActivity.Format("2006-01-02 15:04"), ago)
	}
	// Only dump the metric map when a check is notable (non-healthy)
	// — otherwise the terminal gets noisy fast.
	if c.Status != loopcheck.StatusHealthy && len(c.Metrics) > 0 {
		for k, v := range c.Metrics {
			fmt.Fprintf(w, "      %s: %v\n", k, v)
		}
	}
}

func footer(w *os.File, r *loopcheck.Report) {
	fmt.Fprintln(w, strings.Repeat("─", 72))
	fmt.Fprintln(w, "  legend:")
	fmt.Fprintln(w, "    ✓ healthy — data flowing in the expected cadence")
	fmt.Fprintln(w, "    ~ stale   — wired but quiet (check the feeding loop)")
	fmt.Fprintln(w, "    · unused  — feature present but never invoked")
	fmt.Fprintln(w, "    ✗ broken  — hard failure signal (e.g. all retries fail)")
	fmt.Fprintln(w, strings.Repeat("─", 72))
}

func statusBadge(s loopcheck.Status) string {
	switch s {
	case loopcheck.StatusHealthy:
		return "\033[32m HEALTHY \033[0m"
	case loopcheck.StatusStale:
		return "\033[33m  STALE  \033[0m"
	case loopcheck.StatusUnused:
		return "\033[90m UNUSED  \033[0m"
	case loopcheck.StatusBroken:
		return "\033[31m BROKEN  \033[0m"
	}
	return string(s)
}

// bootstrapSQLite opens an in-memory SQLite, migrates the subset of
// models the loopcheck queries touch, and pins it into model.DB so
// Generate() sees it like any other live connection. Nothing is
// seeded — every check will report 'unused'. That's the point:
// when the DB is empty, the tool should correctly tell you that.
func bootstrapSQLite() error {
	db, err := gorm.Open(sqlite.Open("file:loopcheck?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.AutoMigrate(
		&model.Project{},
		&model.Agent{},
		&model.Change{},
		&model.AgentSession{},
		&model.Experience{},
		&model.SkillCandidate{},
		&model.Policy{},
		&model.KnowledgeArtifact{},
		&model.RefineryRun{},
	); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	model.DB = db
	return nil
}

func statusIcon(s loopcheck.Status) string {
	switch s {
	case loopcheck.StatusHealthy:
		return "\033[32m✓\033[0m"
	case loopcheck.StatusStale:
		return "\033[33m~\033[0m"
	case loopcheck.StatusUnused:
		return "\033[90m·\033[0m"
	case loopcheck.StatusBroken:
		return "\033[31m✗\033[0m"
	}
	return "?"
}
