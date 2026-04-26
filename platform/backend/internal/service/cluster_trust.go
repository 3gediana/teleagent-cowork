package service

// Cluster trust signal — per-project counter of how often a cluster's
// top-1 selection has been independently corroborated.
// =================================================================
//
// Why this exists: cooldown alone can't tell a self-reinforced
// monopoly apart from a "gold cluster" that legitimately holds the
// best artifacts (see artifact_context.go section 5b backlog note).
// Magnitude-aware decay was the first attempt; RRF dense-rank
// compression made its dominance signal too weak to be useful in
// practice (cluster gaps stay below ~2 % even in the most extreme
// rank-1-on-every-signal case, while cooldown decay starts at 15 %).
//
// This file is the second attempt. Instead of inferring
// trustworthiness from RRF score, it accepts an *external* signal:
// every time a grader — LLM judge, ops review, hand-picked golden
// dataset — agrees that the top-1 artifact for a cluster was the
// right pick, the caller invokes RecordClusterAgreement on that
// cluster. The selector then exempts trusted clusters from cooldown
// entirely.
//
// Storage: process-global, in-memory, project-scoped. Survives no
// restart. Suitable for evobench (where judge calls run inside a
// single bench process) and for a future offline-judging job that
// can rehydrate scores at startup. NOT yet suitable as an
// authoritative production signal — production has no judge in the
// hot path. Persistence (a JudgeAgreementLog table or similar) is
// tracked as backlog: do that when a real production grader exists.
//
// Calling convention used by evobench (and any future caller):
//
//	for _, jr := range judgeResults {
//	    if !jr.Result.Skipped && jr.Result.BestID == jr.Task.Ranked[0].Artifact.ID {
//	        cluster := clusterKey(jr.Task.Ranked[0].Artifact)
//	        RecordClusterAgreement(projectID, cluster)
//	    }
//	}
//	trusted := TopTrustedClusters(projectID, /*minCount=*/3, /*limit=*/5)
//	query.TrustedClusters = trusted

import (
	"sort"
	"sync"
	"time"
)

// ClusterTrustEntry exposes the per-cluster counter for inspection
// (debugging, evobench reports). Production callers should use the
// helper functions below, not this struct directly.
type ClusterTrustEntry struct {
	ProjectID   string
	ClusterID   string
	AgreedCount int
	UpdatedAt   time.Time
}

var (
	clusterTrustMu sync.Mutex
	clusterTrust   = map[string]map[string]*ClusterTrustEntry{}
)

// RecordClusterAgreement bumps the trust counter for one
// (project, cluster) pair. Callers invoke this once per
// independent corroboration event (one judge call agreeing,
// one ops thumbs-up, one offline regression-set hit). Empty
// projectID or empty clusterID is silently dropped — same
// defensive contract the cooldown tracker uses.
func RecordClusterAgreement(projectID, clusterID string) {
	if projectID == "" || clusterID == "" {
		return
	}
	clusterTrustMu.Lock()
	defer clusterTrustMu.Unlock()
	per, ok := clusterTrust[projectID]
	if !ok {
		per = map[string]*ClusterTrustEntry{}
		clusterTrust[projectID] = per
	}
	entry, ok := per[clusterID]
	if !ok {
		entry = &ClusterTrustEntry{
			ProjectID: projectID,
			ClusterID: clusterID,
		}
		per[clusterID] = entry
	}
	entry.AgreedCount++
	entry.UpdatedAt = time.Now()
}

// TopTrustedClusters returns the cluster IDs for a project whose
// agreement count is at least minCount, sorted by descending count
// then by cluster ID (lex) for tie-break stability. The limit caps
// the slice; pass 0 for no limit.
//
// minCount lets callers tune sensitivity: a low value (1-2) treats
// any corroboration as trust, useful when judge calls are sparse;
// a higher value (5-10) requires repeat agreement, suitable for
// production paths where false-trust is costly.
func TopTrustedClusters(projectID string, minCount, limit int) []string {
	if projectID == "" {
		return nil
	}
	clusterTrustMu.Lock()
	defer clusterTrustMu.Unlock()
	per, ok := clusterTrust[projectID]
	if !ok || len(per) == 0 {
		return nil
	}
	type kv struct {
		cluster string
		count   int
	}
	all := make([]kv, 0, len(per))
	for cluster, entry := range per {
		if entry.AgreedCount < minCount {
			continue
		}
		all = append(all, kv{cluster: cluster, count: entry.AgreedCount})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].count != all[j].count {
			return all[i].count > all[j].count
		}
		return all[i].cluster < all[j].cluster
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	out := make([]string, len(all))
	for i, kv := range all {
		out[i] = kv.cluster
	}
	return out
}

// ClusterTrustSnapshot returns a defensive copy of all entries for
// inspection — used by evobench reports and debugging tools, not by
// the hot ranking path. Returns entries sorted by project, then by
// descending count for predictable output.
func ClusterTrustSnapshot() []ClusterTrustEntry {
	clusterTrustMu.Lock()
	defer clusterTrustMu.Unlock()
	var out []ClusterTrustEntry
	for _, per := range clusterTrust {
		for _, entry := range per {
			out = append(out, *entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID != out[j].ProjectID {
			return out[i].ProjectID < out[j].ProjectID
		}
		if out[i].AgreedCount != out[j].AgreedCount {
			return out[i].AgreedCount > out[j].AgreedCount
		}
		return out[i].ClusterID < out[j].ClusterID
	})
	return out
}

// resetClusterTrust wipes all in-memory state. Tests use this in
// teardown helpers to keep tests isolated; production code never
// calls this.
func resetClusterTrust() {
	clusterTrustMu.Lock()
	clusterTrust = map[string]map[string]*ClusterTrustEntry{}
	clusterTrustMu.Unlock()
}
