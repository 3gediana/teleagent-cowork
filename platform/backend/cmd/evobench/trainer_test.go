package main

// Mathematical sanity checks for the SGD trainer. Kept lightweight
// on purpose — these aren't integration tests; they only verify
// that the gradient points the right direction under controlled
// synthetic inputs. If these break we have a bug in the optimizer,
// not in the benchmark.

import (
	"math"
	"math/rand"
	"testing"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"
)

// helper: build an RRFSignals with given ranks.
func sig(rSem, rTag, rImp, rRec int) service.RRFSignals {
	return service.RRFSignals{
		RankSemantic:   rSem,
		RankTag:        rTag,
		RankImportance: rImp,
		RankRecency:    rRec,
	}
}

// When every training pair says "the one with better semantic rank
// wins" (and ties elsewhere), learned semantic weight should land
// far above 0.25.
func TestTrainerLearnsSemanticDominance(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	ds := &TrainingDataset{}
	for i := 0; i < 200; i++ {
		// Pos has great semantic rank, mediocre on everything else;
		// Neg is its mirror. Only semantic distinguishes them.
		pos := sig(1, 10, 10, 10)
		neg := sig(10, 10, 10, 10)
		ds.Pairs = append(ds.Pairs, TrainingPair{Pos: pos, Neg: neg, Src: "judge"})
	}

	w, hist := TrainWeights(ds, rng)
	if w.Sem <= 0.25 {
		t.Errorf("expected semantic weight > 0.25, got %.3f", w.Sem)
	}
	if w.Sem <= w.Tag || w.Sem <= w.Imp || w.Sem <= w.Rec {
		t.Errorf("expected semantic to dominate: %s", w)
	}
	if len(hist) == 0 || hist[0] <= hist[len(hist)-1]-0.01 {
		// loss should have gone DOWN (last <= first - eps)
		t.Errorf("expected loss to decrease; hist=%v", hist)
	}
}

// Mirror: when only importance distinguishes positive from negative
// examples, importance weight should dominate.
func TestTrainerLearnsImportanceDominance(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	ds := &TrainingDataset{}
	for i := 0; i < 200; i++ {
		pos := sig(20, 20, 1, 20)
		neg := sig(20, 20, 50, 20)
		ds.Pairs = append(ds.Pairs, TrainingPair{Pos: pos, Neg: neg, Src: "judge"})
	}
	w, _ := TrainWeights(ds, rng)
	if w.Imp <= 0.40 {
		t.Errorf("expected importance > 0.4, got %s", w)
	}
}

// DefaultWeights should sum to 1 within rounding.
func TestDefaultWeightsSumToOne(t *testing.T) {
	w := DefaultWeights()
	s := w.Sem + w.Tag + w.Imp + w.Rec
	if math.Abs(s-1.0) > 1e-9 {
		t.Errorf("default weights sum=%v, want 1.0", s)
	}
}

// Softmax output must be non-negative and normalised.
func TestSoftmaxInvariants(t *testing.T) {
	cases := [][4]float64{
		{0, 0, 0, 0},
		{10, 0, 0, 0},
		{-5, 3, 2, -1},
		{100, 100, 100, 100}, // test overflow safety
	}
	for _, logits := range cases {
		w := softmaxWeights(logits)
		values := []float64{w.Sem, w.Tag, w.Imp, w.Rec}
		var sum float64
		for _, v := range values {
			if v < 0 {
				t.Errorf("softmax produced negative weight: %v", values)
			}
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Errorf("softmax produced non-finite weight: %v", values)
			}
			sum += v
		}
		if math.Abs(sum-1.0) > 1e-9 {
			t.Errorf("softmax weights don't sum to 1: sum=%v values=%v", sum, values)
		}
	}
}

// RerankWithWeights must be a pure function on the input slice.
func TestRerankPreservesInput(t *testing.T) {
	ia := []service.InjectedArtifact{
		{Score: 1.0, Signals: sig(1, 5, 5, 5)},
		{Score: 0.5, Signals: sig(5, 1, 5, 5)},
	}
	copyBefore := make([]service.InjectedArtifact, len(ia))
	copy(copyBefore, ia)

	_ = RerankWithWeights(ia, SignalWeights{1, 0, 0, 0})
	// original slice unchanged
	for i := range ia {
		if ia[i].Score != copyBefore[i].Score {
			t.Errorf("rerank mutated input at %d: %v vs %v", i, ia[i], copyBefore[i])
		}
	}
}

// With a semantic-only weight vector, the artifact with the best
// semantic rank should end up first regardless of initial sort.
func TestRerankWithSemanticOnlyPutsSemanticFirst(t *testing.T) {
	ia := []service.InjectedArtifact{
		{Artifact: model.KnowledgeArtifact{ID: "A"}, Signals: sig(10, 1, 1, 1)},
		{Artifact: model.KnowledgeArtifact{ID: "B"}, Signals: sig(1, 10, 10, 10)},
	}
	out := RerankWithWeights(ia, SignalWeights{1, 0, 0, 0})
	if out[0].Artifact.ID != "B" {
		t.Errorf("expected B first (best semantic rank), got %s", out[0].Artifact.ID)
	}
}
