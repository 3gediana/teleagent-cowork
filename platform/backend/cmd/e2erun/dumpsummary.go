package main

// Temporary diagnostic flag: if E2E_DUMP_SUMMARIES=1, print each
// produced artifact's full Summary after refinery pass #1. Used to
// verify the topic-context extractor actually injected keywords.

import (
	"fmt"
	"os"
	"strings"

	"github.com/a3c/platform/internal/model"
)

func dumpArtifactSummaries(projectID string) {
	if os.Getenv("E2E_DUMP_SUMMARIES") != "1" {
		return
	}
	var arts []model.KnowledgeArtifact
	model.DB.Where("project_id = ?", projectID).Order("kind, confidence DESC").Find(&arts)
	fmt.Println("\n  ── full summaries (E2E_DUMP_SUMMARIES=1) ───────────────")
	for _, a := range arts {
		fmt.Printf("  [%s] %s\n    %s\n\n", a.Kind, a.Name, strings.TrimSpace(a.Summary))
	}
}
