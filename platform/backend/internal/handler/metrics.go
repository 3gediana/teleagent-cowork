package handler

// Metrics endpoints (PR 9)
// ========================
//
// Read-only analytics derived from change-audit feedback. Today only
// one endpoint is exposed — /metrics/injection-signal — because that's
// the first signal we can actually trust now that PR 5 records per-
// artifact retrieval reasons.
//
// Requiring `project_id` here (rather than allowing project-wide
// aggregation) matches the existing data-scoping convention used by
// the refinery endpoints and prevents accidental cross-project leaks.

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/runner"
	"github.com/a3c/platform/internal/service"
)

type MetricsHandler struct{}

func NewMetricsHandler() *MetricsHandler { return &MetricsHandler{} }

// GET /metrics/injection-signal?project_id=...&limit=500
// Returns per-signal success/failure tallies over the most recent
// `limit` feedback-applied changes (default 500, cap 5000).
func (h *MetricsHandler) InjectionSignal(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"error": gin.H{"code": "INVALID_PARAMS", "message": "project_id required"}})
		return
	}
	limit := 500
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 5000 {
			limit = n
		}
	}

	metrics := service.ComputeInjectionMetrics(projectID, limit)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": metrics})
}

// Prometheus emits operator-grade metrics in Prometheus text format
// (https://prometheus.io/docs/instrumenting/exposition_formats/).
//
// Routed at /metrics (no auth). The endpoint is intentionally open
// because Prometheus scrapers don't carry tokens and the operator
// firewall is what gates external access. The values exposed are
// purely operational counters/gauges — no PII, no sample data, no
// per-project leakage. Specifically:
//
//   * a3c_agents_online                    gauge   total agents with status='online'
//   * a3c_agents_offline                   gauge   total agents with status='offline'
//   * a3c_projects_total                   gauge   project rows
//   * a3c_tasks_pending                    gauge   tasks with status in ('pending','claimed')
//   * a3c_tasks_completed_total            gauge   completed cumulative
//   * a3c_changes_pending                  gauge   change rows awaiting verdict
//   * a3c_compaction_circuit_breaker_trips counter compaction breaker trip count
//   * a3c_compaction_summarize_failures    counter all summarize errors
//   * a3c_compaction_summarize_success     counter all successful summarize
//   * a3c_compaction_micro_success         counter tier-1 microcompacts that worked
//   * a3c_compaction_auto_recover          counter breaker self-heals
//   * a3c_uptime_seconds                   counter process uptime
//
// Add new metrics by appending writeMetric calls below; keep the
// same project-agnostic policy (no labels that include user data).
var processStart = time.Now()

func (h *MetricsHandler) Prometheus(c *gin.Context) {
	var sb strings.Builder

	writeHelp := func(name, help, kind string) {
		fmt.Fprintf(&sb, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&sb, "# TYPE %s %s\n", name, kind)
	}
	writeMetric := func(name string, value any) {
		fmt.Fprintf(&sb, "%s %v\n", name, value)
	}

	// --- DB-derived gauges (one query each — cheap; metrics endpoint
	//     is scraped at most every 15s).
	var agentsOnline, agentsOffline int64
	model.DB.Model(&model.Agent{}).Where("status = ?", "online").Count(&agentsOnline)
	model.DB.Model(&model.Agent{}).Where("status = ?", "offline").Count(&agentsOffline)

	var projectsTotal int64
	model.DB.Model(&model.Project{}).Count(&projectsTotal)

	var tasksPending, tasksCompleted int64
	model.DB.Model(&model.Task{}).Where("status IN ?", []string{"pending", "claimed"}).Count(&tasksPending)
	model.DB.Model(&model.Task{}).Where("status = ?", "completed").Count(&tasksCompleted)

	var changesPending int64
	model.DB.Model(&model.Change{}).Where("status IN ?", []string{"pending", "pending_human_confirm", "evaluating", "merging"}).Count(&changesPending)

	writeHelp("a3c_agents_online", "Agents currently marked online.", "gauge")
	writeMetric("a3c_agents_online", agentsOnline)
	writeHelp("a3c_agents_offline", "Agents currently marked offline.", "gauge")
	writeMetric("a3c_agents_offline", agentsOffline)
	writeHelp("a3c_projects_total", "Total project rows.", "gauge")
	writeMetric("a3c_projects_total", projectsTotal)
	writeHelp("a3c_tasks_pending", "Pending or claimed tasks.", "gauge")
	writeMetric("a3c_tasks_pending", tasksPending)
	writeHelp("a3c_tasks_completed_total", "Completed tasks (cumulative).", "gauge")
	writeMetric("a3c_tasks_completed_total", tasksCompleted)
	writeHelp("a3c_changes_pending", "Changes awaiting verdict / merge.", "gauge")
	writeMetric("a3c_changes_pending", changesPending)

	// --- Runner compaction telemetry (lock-free atomic reads).
	cm := runner.CompactionMetrics()
	writeHelp("a3c_compaction_circuit_breaker_trips", "Compaction circuit-breaker trip events.", "counter")
	writeMetric("a3c_compaction_circuit_breaker_trips", cm.CircuitBreakerTrips)
	writeHelp("a3c_compaction_summarize_failures", "Compaction summarize() errors (cumulative).", "counter")
	writeMetric("a3c_compaction_summarize_failures", cm.SummarizeFailures)
	writeHelp("a3c_compaction_summarize_success", "Compaction summarize() successes.", "counter")
	writeMetric("a3c_compaction_summarize_success", cm.SummarizeSuccess)
	writeHelp("a3c_compaction_micro_success", "Tier-1 microcompacts that brought tokens below threshold.", "counter")
	writeMetric("a3c_compaction_micro_success", cm.MicrocompactSuccess)
	writeHelp("a3c_compaction_auto_recover", "Circuit-breaker self-heal events.", "counter")
	writeMetric("a3c_compaction_auto_recover", cm.AutoRecover)

	// --- Process uptime — useful for "did we just restart?" checks.
	writeHelp("a3c_uptime_seconds", "Seconds since this process started.", "counter")
	writeMetric("a3c_uptime_seconds", int64(time.Since(processStart).Seconds()))

	c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	c.String(http.StatusOK, sb.String())
}
