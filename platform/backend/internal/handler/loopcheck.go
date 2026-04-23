package handler

// Loop-check endpoint — shares its data-collection core with the
// experiments/loopcheck CLI. See internal/service/loopcheck for the
// rationale behind the split and the meaning of each check's
// status. This file is intentionally tiny: all it does is parse
// query params, call loopcheck.Generate, return the JSON.
//
// Bound under the authenticated group as GET /api/v1/loopcheck.
// It's read-only, so any authenticated caller (dashboard operator
// or internal CI) may poll it safely. Heavy queries are avoided in
// the loopcheck package itself so a 1/min dashboard poll is fine.

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/a3c/platform/internal/service/loopcheck"
)

type LoopCheckHandler struct{}

func NewLoopCheckHandler() *LoopCheckHandler { return &LoopCheckHandler{} }

// GET /api/v1/loopcheck?project_id=...&window_days=7
//
// Returns the full Report object. With project_id empty the report
// is platform-wide. window_days defaults to 7 and is capped at 90
// to keep queries cheap.
func (h *LoopCheckHandler) Get(c *gin.Context) {
	projectID := c.Query("project_id")

	window := 7
	if v := c.Query("window_days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 90 {
			window = n
		}
	}

	report := loopcheck.Generate(loopcheck.Options{
		ProjectID:  projectID,
		WindowDays: window,
	})

	c.JSON(http.StatusOK, gin.H{"success": true, "data": report})
}
