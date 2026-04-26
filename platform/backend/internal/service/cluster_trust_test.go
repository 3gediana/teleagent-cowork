package service

// Tests for the cluster trust tracker. The tracker is the source of
// truth for "which clusters has an external grader independently
// corroborated?", and is consumed by the selector via
// ArtifactQuery.TrustedClusters. The end-to-end integration test
// (selector exempts trusted clusters from cooldown) lives in
// artifact_context_test.go.

import (
	"reflect"
	"testing"
)

func TestClusterTrust_RecordIncrementsCount(t *testing.T) {
	resetClusterTrust()
	defer resetClusterTrust()

	RecordClusterAgreement("p1", "ep_A")
	RecordClusterAgreement("p1", "ep_A")
	RecordClusterAgreement("p1", "ep_A")

	got := TopTrustedClusters("p1", 1, 0)
	if !reflect.DeepEqual(got, []string{"ep_A"}) {
		t.Errorf("after 3 records: got %v, want [ep_A]", got)
	}

	// Inspect raw count via snapshot.
	snap := ClusterTrustSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot size: got %d want 1", len(snap))
	}
	if snap[0].AgreedCount != 3 {
		t.Errorf("AgreedCount: got %d want 3", snap[0].AgreedCount)
	}
}

func TestClusterTrust_TopNHonoursMinCount(t *testing.T) {
	resetClusterTrust()
	defer resetClusterTrust()

	// ep_A reaches 3, ep_B reaches 1. Threshold 2 should drop ep_B.
	for i := 0; i < 3; i++ {
		RecordClusterAgreement("p1", "ep_A")
	}
	RecordClusterAgreement("p1", "ep_B")

	got := TopTrustedClusters("p1", 2, 0)
	if !reflect.DeepEqual(got, []string{"ep_A"}) {
		t.Errorf("threshold=2: got %v, want [ep_A]", got)
	}

	got = TopTrustedClusters("p1", 1, 0)
	if !reflect.DeepEqual(got, []string{"ep_A", "ep_B"}) {
		t.Errorf("threshold=1: got %v, want [ep_A, ep_B]", got)
	}
}

func TestClusterTrust_TopNHonoursLimit(t *testing.T) {
	resetClusterTrust()
	defer resetClusterTrust()

	// 4 clusters, varying counts.
	RecordClusterAgreement("p1", "ep_A")
	RecordClusterAgreement("p1", "ep_A")
	RecordClusterAgreement("p1", "ep_A") // count=3
	RecordClusterAgreement("p1", "ep_B")
	RecordClusterAgreement("p1", "ep_B") // count=2
	RecordClusterAgreement("p1", "ep_C") // count=1
	RecordClusterAgreement("p1", "ep_D") // count=1

	got := TopTrustedClusters("p1", 1, 2)
	if !reflect.DeepEqual(got, []string{"ep_A", "ep_B"}) {
		t.Errorf("limit=2: got %v, want [ep_A, ep_B]", got)
	}

	// limit=0 means no cap.
	got = TopTrustedClusters("p1", 1, 0)
	if len(got) != 4 {
		t.Errorf("limit=0: got %d entries, want 4 (got: %v)", len(got), got)
	}
}

func TestClusterTrust_PerProjectIsolation(t *testing.T) {
	resetClusterTrust()
	defer resetClusterTrust()

	RecordClusterAgreement("p1", "ep_A")
	RecordClusterAgreement("p1", "ep_A")
	RecordClusterAgreement("p2", "ep_X")

	if got := TopTrustedClusters("p1", 1, 0); !reflect.DeepEqual(got, []string{"ep_A"}) {
		t.Errorf("p1: got %v, want [ep_A]", got)
	}
	if got := TopTrustedClusters("p2", 1, 0); !reflect.DeepEqual(got, []string{"ep_X"}) {
		t.Errorf("p2: got %v, want [ep_X]", got)
	}
	if got := TopTrustedClusters("p_unknown", 1, 0); got != nil {
		t.Errorf("unknown project: got %v, want nil", got)
	}
}

func TestClusterTrust_TieBreakByClusterID(t *testing.T) {
	resetClusterTrust()
	defer resetClusterTrust()

	// ep_B and ep_A both end at count=2. Tie-break is lexicographic.
	for i := 0; i < 2; i++ {
		RecordClusterAgreement("p1", "ep_B")
		RecordClusterAgreement("p1", "ep_A")
	}

	got := TopTrustedClusters("p1", 1, 0)
	if !reflect.DeepEqual(got, []string{"ep_A", "ep_B"}) {
		t.Errorf("tie-break: got %v, want [ep_A, ep_B] (lex order)", got)
	}
}

func TestClusterTrust_EmptyInputsAreNoOp(t *testing.T) {
	resetClusterTrust()
	defer resetClusterTrust()

	RecordClusterAgreement("", "ep_A")
	RecordClusterAgreement("p1", "")
	RecordClusterAgreement("", "")

	if got := TopTrustedClusters("p1", 1, 0); got != nil {
		t.Errorf("p1 should still be empty after empty-input records; got %v", got)
	}
	if got := TopTrustedClusters("", 1, 0); got != nil {
		t.Errorf("empty project lookup should return nil; got %v", got)
	}
}

func TestClusterTrust_ResetClearsAll(t *testing.T) {
	RecordClusterAgreement("p1", "ep_A")
	RecordClusterAgreement("p2", "ep_B")
	resetClusterTrust()
	if got := ClusterTrustSnapshot(); len(got) != 0 {
		t.Errorf("after reset snapshot should be empty; got %v", got)
	}
}
