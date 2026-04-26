package service

// Per-project rolling window of recent top-1 cluster IDs.
// =======================================================
//
// Why this exists: evobench (seed=42, 100 rounds, 60 artifacts) showed
// that without cross-round diversification, whichever cluster happened
// to host the highest-importance artifacts at simulation start kept
// dominating top-1 forever. Each successful inject reinforced that
// cluster's importance score, which fed back into RRF, which kept it
// at top-1, etc. — a textbook self-reinforcing bias loop.
//
// Wiring it in: SelectArtifactsForInjection now respects an optional
// q.RecentTopClusters []string, multiplying the offending cluster's
// RRF score by ClusterCooldownDecay per occurrence. But the *selector*
// stays stateless — it doesn't know about projects-as-time-series. The
// dispatcher / hint-builder layer is the natural owner of "what was
// last round's top-1 for this project?".
//
// This file is that owner. Process-global, sync.Mutex-protected map
// keyed by project ID. Each entry is a sliding window of cluster IDs
// (most-recent last), capped at recentClusterWindowSize. Persistence
// is intentionally absent: a server restart resets the cooldown, which
// means we lose at most a few rounds of anti-monopoly bias before the
// window refills. That's an acceptable trade for not adding a Redis
// dependency to the inject path.
//
// Calling convention used by BuildTaskClaimHints:
//
//	recent := recentClustersFor(task.ProjectID)
//	query.RecentTopClusters = recent
//	results := SelectArtifactsForInjection(ctx, query)
//	if len(results) > 0 {
//	    recordTopCluster(task.ProjectID, results[0].Artifact)
//	}

import (
	"sync"

	"github.com/a3c/platform/internal/model"
)

// recentClusterWindowSize bounds the sliding window per project.
// Five rounds is enough to break a 100% monopoly down to ~20% (see
// evobench output: ep_H 100/100 → 21/100 with EVOBENCH_COOLDOWN=1
// at this window size). Bigger windows tax legitimate gold clusters
// more, so we deliberately keep it short.
const recentClusterWindowSize = 5

var (
	recentClusterMu sync.Mutex
	recentClusters  = map[string][]string{}
)

// recentClustersFor returns a copy of the rolling window for a project.
// Returns nil for unknown / empty project IDs so callers can blindly
// forward the result into ArtifactQuery.RecentTopClusters (which
// treats an empty slice as "no penalty"). Returning a copy — not the
// internal slice — protects against callers mutating shared state.
func recentClustersFor(projectID string) []string {
	if projectID == "" {
		return nil
	}
	recentClusterMu.Lock()
	defer recentClusterMu.Unlock()
	win := recentClusters[projectID]
	if len(win) == 0 {
		return nil
	}
	out := make([]string, len(win))
	copy(out, win)
	return out
}

// recordTopCluster appends a top-1 artifact's cluster key to the
// project's rolling window. Empty clusters (legacy artifacts that
// pre-date SourceEvents tracking) are skipped: they can't be
// penalised by the selector, so recording them would just push
// real clusters out of the window for nothing.
func recordTopCluster(projectID string, top model.KnowledgeArtifact) {
	if projectID == "" {
		return
	}
	cluster := clusterKey(top)
	if cluster == "" {
		return
	}
	recentClusterMu.Lock()
	defer recentClusterMu.Unlock()
	win := recentClusters[projectID]
	win = append(win, cluster)
	if len(win) > recentClusterWindowSize {
		win = win[len(win)-recentClusterWindowSize:]
	}
	recentClusters[projectID] = win
}

// resetRecentClusters wipes all per-project history. Used by test
// teardown helpers to keep tests isolated; production code never
// calls this.
func resetRecentClusters() {
	recentClusterMu.Lock()
	recentClusters = map[string][]string{}
	recentClusterMu.Unlock()
}
