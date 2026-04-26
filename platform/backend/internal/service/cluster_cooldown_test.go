package service

// Tests for the per-project cluster cooldown tracker. Covers the
// invariants other code relies on:
//
//   * append + slide preserves "most recent last" order
//   * window caps at recentClusterWindowSize
//   * snapshot is a copy (callers can't mutate shared state)
//   * empty project ID and empty cluster ID are no-ops
//   * each project has its own window
//
// The integration test (cooldown actually changes top-1 across two
// BuildTaskClaimHints calls) lives in task_hints_test.go because it
// needs the full hint-builder fixture.

import (
	"reflect"
	"testing"

	"github.com/a3c/platform/internal/model"
)

// recordCluster is a small inlined helper so each test reads cleanly
// without a fan-out of helper objects.
func recordCluster(projectID, cluster string) {
	recordTopCluster(projectID, model.KnowledgeArtifact{
		SourceEvents: `["` + cluster + `","_evt"]`,
	})
}

func TestRecentClusters_AppendPreservesOrder(t *testing.T) {
	resetRecentClusters()
	defer resetRecentClusters()

	recordCluster("p1", "ep_A")
	recordCluster("p1", "ep_B")
	recordCluster("p1", "ep_A")

	got := recentClustersFor("p1")
	want := []string{"ep_A", "ep_B", "ep_A"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("append order: got %v want %v", got, want)
	}
}

func TestRecentClusters_SlidesWhenFull(t *testing.T) {
	resetRecentClusters()
	defer resetRecentClusters()

	// Fill the window with one extra entry; oldest should drop off.
	clusters := []string{"ep_A", "ep_B", "ep_C", "ep_D", "ep_E", "ep_F"}
	for _, c := range clusters {
		recordCluster("p1", c)
	}
	got := recentClustersFor("p1")
	if len(got) != recentClusterWindowSize {
		t.Fatalf("window size after overflow: got %d want %d",
			len(got), recentClusterWindowSize)
	}
	want := []string{"ep_B", "ep_C", "ep_D", "ep_E", "ep_F"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after slide: got %v want %v", got, want)
	}
}

func TestRecentClusters_SnapshotIsCopy(t *testing.T) {
	resetRecentClusters()
	defer resetRecentClusters()

	recordCluster("p1", "ep_A")
	snap := recentClustersFor("p1")
	if len(snap) != 1 || snap[0] != "ep_A" {
		t.Fatalf("baseline snapshot wrong: %v", snap)
	}
	// Mutating the snapshot must not affect future reads.
	snap[0] = "MUTATED"
	again := recentClustersFor("p1")
	if again[0] != "ep_A" {
		t.Errorf("snapshot mutation leaked into tracker: got %v", again)
	}
}

func TestRecentClusters_EmptyProjectIDIsNoOp(t *testing.T) {
	resetRecentClusters()
	defer resetRecentClusters()

	recordCluster("", "ep_A")
	if got := recentClustersFor(""); got != nil {
		t.Errorf("empty project should not record; got %v", got)
	}
	// Other projects must remain untouched.
	if got := recentClustersFor("p1"); got != nil {
		t.Errorf("p1 should be untouched; got %v", got)
	}
}

func TestRecentClusters_EmptyClusterSkipped(t *testing.T) {
	resetRecentClusters()
	defer resetRecentClusters()

	// Legacy artifact: no SourceEvents → clusterKey == "".
	recordTopCluster("p1", model.KnowledgeArtifact{})
	if got := recentClustersFor("p1"); got != nil {
		t.Errorf("empty cluster should be skipped; got %v", got)
	}
}

func TestRecentClusters_PerProjectIsolation(t *testing.T) {
	resetRecentClusters()
	defer resetRecentClusters()

	recordCluster("p1", "ep_A")
	recordCluster("p2", "ep_X")
	recordCluster("p1", "ep_B")

	if got, want := recentClustersFor("p1"), []string{"ep_A", "ep_B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("p1: got %v want %v", got, want)
	}
	if got, want := recentClustersFor("p2"), []string{"ep_X"}; !reflect.DeepEqual(got, want) {
		t.Errorf("p2: got %v want %v", got, want)
	}
	if got := recentClustersFor("p_unknown"); got != nil {
		t.Errorf("unknown project: expected nil; got %v", got)
	}
}

func TestRecentClusters_ResetClearsAllProjects(t *testing.T) {
	resetRecentClusters()
	recordCluster("p1", "ep_A")
	recordCluster("p2", "ep_B")
	resetRecentClusters()
	if got := recentClustersFor("p1"); got != nil {
		t.Errorf("after reset p1 should be empty; got %v", got)
	}
	if got := recentClustersFor("p2"); got != nil {
		t.Errorf("after reset p2 should be empty; got %v", got)
	}
}
