// Command shadowdiff — compare two agent.Session rows from the same
// project side-by-side. Purpose-built for operators running the
// migration-runbook shadow step: flip the same role between opencode
// and the native runtime for a week, then diff the two output
// populations to decide if the native verdicts are trustworthy.
//
// Two invocation modes:
//
//   # Compare the last N sessions for a role across runtimes.
//   shadowdiff --project <id> --role audit_1 --limit 20
//
//   # Compare two specific sessions by id (useful for bug repro).
//   shadowdiff --session-a sess_123 --session-b sess_456
//
// Output is a pipe-separated table + per-row verdict diff when
// running in the role-comparison mode. Exit code 0 always; this is
// an inspection tool, not a gate.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/model"
)

func main() {
	log.SetFlags(0)

	projectID := flag.String("project", "", "project id (required for --role mode)")
	role := flag.String("role", "", "role to compare across runtimes (e.g. audit_1)")
	limit := flag.Int("limit", 20, "max sessions per runtime to inspect")
	sessionA := flag.String("session-a", "", "specific session id, side A (overrides --role mode)")
	sessionB := flag.String("session-b", "", "specific session id, side B (paired with --session-a)")
	showOutput := flag.Bool("show-output", false, "print full Output field, not just the first 200 chars")
	flag.Parse()

	cfg := config.Load("")
	if err := model.InitDB(&cfg.Database); err != nil {
		log.Fatalf("db: %v", err)
	}

	if *sessionA != "" && *sessionB != "" {
		diffPair(*sessionA, *sessionB, *showOutput)
		return
	}
	if *projectID == "" || *role == "" {
		fmt.Fprintln(os.Stderr, "Need either --session-a and --session-b, or --project and --role.")
		flag.Usage()
		os.Exit(2)
	}
	diffRunPopulation(*projectID, *role, *limit, *showOutput)
}

// diffRunPopulation prints a table of the last N sessions for the
// role, split by runtime (native/opencode detected via the session's
// ModelProvider prefix — or a join through RoleOverride, but simpler
// here to just infer from AgentSession.ModelProvider which is already
// persisted per session).
func diffRunPopulation(projectID, role string, limit int, showOutput bool) {
	// We pull 2× limit so we can fill up to `limit` on each side even
	// when the distribution is skewed.
	var rows []model.AgentSession
	err := model.DB.Where("project_id = ? AND role = ?", projectID, role).
		Order("created_at DESC").
		Limit(limit * 4).
		Find(&rows).Error
	if err != nil {
		log.Fatalf("query sessions: %v", err)
	}

	var native, legacy []model.AgentSession
	for _, r := range rows {
		if strings.HasPrefix(r.ModelProvider, "llm_") {
			if len(native) < limit {
				native = append(native, r)
			}
		} else {
			if len(legacy) < limit {
				legacy = append(legacy, r)
			}
		}
	}

	fmt.Printf("\n== Role %s on project %s ==\n", role, projectID)
	fmt.Printf("  native sessions: %d (provider prefix llm_*)\n", len(native))
	fmt.Printf("  legacy sessions: %d (all other provider ids)\n\n", len(legacy))

	printSide("NATIVE", native, showOutput)
	printSide("LEGACY (opencode)", legacy, showOutput)

	fmt.Println("\n-- summary --")
	fmt.Printf("  native   completed/failed: %d / %d   avg iters-ish: ~n/a (no DB col)\n",
		countStatus(native, "completed"), countStatus(native, "failed"))
	fmt.Printf("  legacy   completed/failed: %d / %d\n",
		countStatus(legacy, "completed"), countStatus(legacy, "failed"))
	fmt.Printf("  native   avg duration_ms:  %.0f\n", avgDuration(native))
	fmt.Printf("  legacy   avg duration_ms:  %.0f\n", avgDuration(legacy))

	if len(native) > 0 && len(legacy) > 0 {
		// A completion-rate gap >10% is the runbook's red-flag signal.
		nRate := rateComplete(native)
		lRate := rateComplete(legacy)
		if diff := nRate - lRate; diff < -0.1 {
			fmt.Printf("  ⚠ native completion rate is %.1fpp lower — investigate before flipping prod\n",
				-diff*100)
		} else if diff > 0.1 {
			fmt.Printf("  ✓ native completion rate %.1fpp higher than opencode\n", diff*100)
		} else {
			fmt.Printf("  ≈ completion rates within 10pp (delta %.1fpp)\n", diff*100)
		}
	}
}

func printSide(label string, rows []model.AgentSession, showOutput bool) {
	fmt.Printf("--- %s ---\n", label)
	if len(rows) == 0 {
		fmt.Println("  (none)")
		return
	}
	fmt.Printf("  %-20s  %-10s  %-18s  %-10s  %s\n",
		"id", "status", "model", "dur_ms", "output")
	for _, r := range rows {
		out := r.Output
		if !showOutput && len(out) > 200 {
			out = out[:200] + "…"
		}
		out = strings.ReplaceAll(out, "\n", " ")
		fmt.Printf("  %-20s  %-10s  %-18s  %-10d  %s\n",
			shorten(r.ID, 20), r.Status, shorten(r.ModelID, 18), r.DurationMs, shorten(out, 120))
	}
	fmt.Println()
}

func countStatus(rows []model.AgentSession, status string) int {
	n := 0
	for _, r := range rows {
		if r.Status == status {
			n++
		}
	}
	return n
}

func rateComplete(rows []model.AgentSession) float64 {
	if len(rows) == 0 {
		return 0
	}
	return float64(countStatus(rows, "completed")) / float64(len(rows))
}

func avgDuration(rows []model.AgentSession) float64 {
	if len(rows) == 0 {
		return 0
	}
	var sum int
	for _, r := range rows {
		sum += r.DurationMs
	}
	return float64(sum) / float64(len(rows))
}

// diffPair prints a full-fidelity comparison of two specific sessions.
// Useful when a bug reproduces only on one runtime and the operator
// wants side-by-side outputs without grepping the DB.
func diffPair(sidA, sidB string, showOutput bool) {
	var a, b model.AgentSession
	if err := model.DB.Where("id = ?", sidA).First(&a).Error; err != nil {
		log.Fatalf("session A %s: %v", sidA, err)
	}
	if err := model.DB.Where("id = ?", sidB).First(&b).Error; err != nil {
		log.Fatalf("session B %s: %v", sidB, err)
	}
	fmt.Println(strings.Repeat("═", 72))
	fmt.Println("  Pair diff")
	fmt.Println(strings.Repeat("═", 72))

	fields := []struct {
		label string
		a, b  string
	}{
		{"id", a.ID, b.ID},
		{"project", a.ProjectID, b.ProjectID},
		{"role", a.Role, b.Role},
		{"model_provider", a.ModelProvider, b.ModelProvider},
		{"model_id", a.ModelID, b.ModelID},
		{"status", a.Status, b.Status},
		{"duration_ms", itoa(a.DurationMs), itoa(b.DurationMs)},
		{"retry_count", itoa(a.RetryCount), itoa(b.RetryCount)},
		{"change_id", a.ChangeID, b.ChangeID},
		{"trigger", a.TriggerReason, b.TriggerReason},
	}
	fmt.Printf("  %-18s  %-30s  %s\n", "field", "A", "B")
	for _, f := range fields {
		marker := " "
		if f.a != f.b {
			marker = "*"
		}
		fmt.Printf("%s %-18s  %-30s  %s\n", marker, f.label,
			shorten(f.a, 30), shorten(f.b, 30))
	}

	fmt.Println("\n-- Output --")
	fmt.Println("A:")
	fmt.Println(indent(truncateTo(a.Output, 4000, showOutput)))
	fmt.Println("\nB:")
	fmt.Println(indent(truncateTo(b.Output, 4000, showOutput)))

	if a.InjectedArtifacts != "" || b.InjectedArtifacts != "" {
		fmt.Println("\n-- InjectedArtifacts --")
		fmt.Println("A:", prettyJSON(a.InjectedArtifacts))
		fmt.Println("B:", prettyJSON(b.InjectedArtifacts))
	}
}

func shorten(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func truncateTo(s string, max int, showFull bool) string {
	if showFull || len(s) <= max {
		return s
	}
	return s[:max] + "\n… (truncated; use --show-output for full)"
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func prettyJSON(raw string) string {
	if raw == "" {
		return "(empty)"
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw // not JSON, print as-is
	}
	b, _ := json.MarshalIndent(v, "    ", "  ")
	return "\n    " + string(b)
}
