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
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

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
