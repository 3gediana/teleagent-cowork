package main

// EvalSet — held-out retrieval rounds paired with judge verdicts,
// used to compare fixed RRF against a learned weight vector.
//
// Why a distinct struct from TrainingDataset: training consumes
// pairs (pos/neg) and throws away the ranked order; evaluation
// needs the full ranked list to re-rank under new weights.

import (
	"github.com/a3c/platform/internal/service"
)

// EvalSample is one retrieval round with the judge's top-1 pick.
type EvalSample struct {
	Ranked    []service.InjectedArtifact // original ranked list from evobench
	JudgePick string                     // artifact ID the judge picked
}

type EvalSet struct {
	Samples []EvalSample
}

func (e *EvalSet) Add(ranked []service.InjectedArtifact, judgePick string) {
	if judgePick == "" || len(ranked) == 0 {
		return
	}
	// deep copy the top-K to insulate from downstream mutations
	cp := make([]service.InjectedArtifact, len(ranked))
	copy(cp, ranked)
	e.Samples = append(e.Samples, EvalSample{Ranked: cp, JudgePick: judgePick})
}

func (e *EvalSet) Size() int { return len(e.Samples) }

// AgreementResult captures (agree / total) for one weight config.
type AgreementResult struct {
	Agree int
	Total int
}

// CompareAgreement measures top-1 agreement with the judge under
// both the fixed RRF (already-sorted list) and the learned weights.
func (e *EvalSet) CompareAgreement(w SignalWeights) (base, learned AgreementResult) {
	for _, s := range e.Samples {
		base.Total++
		learned.Total++
		if s.Ranked[0].Artifact.ID == s.JudgePick {
			base.Agree++
		}
		reranked := RerankWithWeights(s.Ranked, w)
		if reranked[0].Artifact.ID == s.JudgePick {
			learned.Agree++
		}
	}
	return
}

// countPairs tallies audit vs judge pairs for reporting.
func countPairs(d *TrainingDataset) (audit, judge int) {
	for _, p := range d.Pairs {
		if p.Src == "judge" {
			judge++
		} else {
			audit++
		}
	}
	return
}
