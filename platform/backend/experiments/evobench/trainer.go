package main

// Per-signal RRF weight learner.
//
// Model (minimal on purpose — 4 parameters, no hidden layers):
//
//   score(a) = w_sem * 1/(k + r_sem(a))
//            + w_tag * 1/(k + r_tag(a))
//            + w_imp * 1/(k + r_imp(a))
//            + w_rec * 1/(k + r_rec(a))
//
// With w_i ≥ 0 and a softmax re-normalisation so the weights always
// sum to 1 (otherwise SGD can blow up or zero everything out).
//
// Loss — pairwise (Bradley-Terry / BPR):
//
//   for each (pos, neg) pair extracted from feedback:
//       p = sigmoid(score(pos) - score(neg))
//       L = -log p
//
// pos / neg pairs come from two sources:
//   * L0 feedback:  a_credited  >  a_tail   (credited > ignored)
//   * LLM judge:    a_judge_top1 > a_other  (judge ground-truth)
//
// Training runs once per benchmark, in-memory. Output: the final
// weight vector and a before/after retrieval comparison (top-1
// agreement with the judge's picks, using held-out rounds).
//
// Why not a bigger network: 4 features, ~100-300 training pairs per
// benchmark run. A 2-layer MLP would overfit and lose the
// interpretability (operators can *read* the trained weights and
// decide whether to promote them to production).

import (
	"fmt"
	"math"
	"math/rand"
	"sort"

	"github.com/a3c/platform/internal/service"
)

// SignalWeights is the learned parameter vector. Declared such that
// index order matches the RRFSignals field order, so feature
// extraction can just slice it.
type SignalWeights struct {
	Sem float64
	Tag float64
	Imp float64
	Rec float64
}

// DefaultWeights is the 1/4 / 1/4 / 1/4 / 1/4 fixed RRF baseline.
// Used for A/B comparison and as the training initial condition.
func DefaultWeights() SignalWeights {
	return SignalWeights{0.25, 0.25, 0.25, 0.25}
}

// Sum returns the L1 norm — after softmax this should always be 1,
// before softmax it's the raw parameter sum.
func (w SignalWeights) Sum() float64 { return w.Sem + w.Tag + w.Imp + w.Rec }

// String renders weights as "sem=.. tag=.. imp=.. rec=..".
func (w SignalWeights) String() string {
	return fmt.Sprintf("sem=%.3f tag=%.3f imp=%.3f rec=%.3f",
		w.Sem, w.Tag, w.Imp, w.Rec)
}

// TrainingPair is one observation for pairwise ranking loss. `Pos`
// should score higher than `Neg` under a good weight vector.
type TrainingPair struct {
	Pos service.RRFSignals
	Neg service.RRFSignals
	// Src tracks where this pair came from, used for reporting and
	// optional upweighting (judge pairs are much stronger signal).
	Src string // "audit_L0" | "audit_L1" | "judge"
}

// TrainingDataset collects pairs across the benchmark run.
type TrainingDataset struct {
	Pairs []TrainingPair
}

// Add appends a pair; silently ignores pairs where both sides share
// identical ranks (no gradient to learn from).
func (d *TrainingDataset) Add(pos, neg service.RRFSignals, src string) {
	if pos == neg {
		return
	}
	d.Pairs = append(d.Pairs, TrainingPair{Pos: pos, Neg: neg, Src: src})
}

// BuildAuditPairs turns one retrieval round's (ranked artifacts,
// audit verdict) into pairs. On L0/L1 we assume the top-1 artifact
// was "the right call" and pair it against a lower-ranked artifact.
// On L2 we do the opposite (the top-1 likely misled the agent → it
// should be ranked BELOW a random tail).
func BuildAuditPairs(ranked []service.InjectedArtifact, auditLevel string, rng *rand.Rand) []TrainingPair {
	if len(ranked) < 2 {
		return nil
	}
	var pairs []TrainingPair
	top := ranked[0].Signals
	tailStart := len(ranked) / 2
	if tailStart < 1 {
		tailStart = 1
	}
	// pick 2 random tail candidates to keep the dataset balanced
	for i := 0; i < 2; i++ {
		idx := tailStart + rng.Intn(len(ranked)-tailStart)
		tail := ranked[idx].Signals
		switch auditLevel {
		case "L0", "L1":
			pairs = append(pairs, TrainingPair{Pos: top, Neg: tail, Src: "audit_" + auditLevel})
		case "L2":
			pairs = append(pairs, TrainingPair{Pos: tail, Neg: top, Src: "audit_L2"})
		}
	}
	return pairs
}

// BuildJudgePairs turns a judge verdict into pairs. Strongest signal
// since it's a targeted relevance assessment rather than a noisy
// downstream proxy.
func BuildJudgePairs(ranked []service.InjectedArtifact, judgePick string) []TrainingPair {
	// Find the judge's pick
	posIdx := -1
	for i, ia := range ranked {
		if ia.Artifact.ID == judgePick {
			posIdx = i
			break
		}
	}
	if posIdx < 0 {
		return nil
	}
	pos := ranked[posIdx].Signals
	var pairs []TrainingPair
	for i, ia := range ranked {
		if i == posIdx {
			continue
		}
		pairs = append(pairs, TrainingPair{Pos: pos, Neg: ia.Signals, Src: "judge"})
	}
	return pairs
}

// scoreWith computes the weighted RRF score for a single artifact
// given a weight vector. Uses the *service-level* rrfK so training
// and inference stay aligned.
func scoreWith(s service.RRFSignals, w SignalWeights) float64 {
	k := service.RRFK()
	return w.Sem/(k+float64(s.RankSemantic)) +
		w.Tag/(k+float64(s.RankTag)) +
		w.Imp/(k+float64(s.RankImportance)) +
		w.Rec/(k+float64(s.RankRecency))
}

// TrainWeights runs minibatch SGD on a dataset of pairs and returns
// the learned weight vector. Parameters are reasonable defaults; a
// future PR will expose them as flags when the L1 auto-tuner needs
// tuning the tuner.
func TrainWeights(data *TrainingDataset, rng *rand.Rand) (SignalWeights, []float64) {
	if len(data.Pairs) == 0 {
		return DefaultWeights(), nil
	}

	// Parameterise with logits; softmax to weights. Softmax keeps
	// weights non-negative and normalised without clipping.
	logits := [4]float64{0, 0, 0, 0} // uniform start → softmax = [1/4]*4

	const (
		epochs       = 200
		learningRate = 10.0 // feat-diff magnitudes are tiny (~1/60² effective)
		batchSize    = 16
		// Judge pairs are direct relevance labels; audit pairs are a
		// noisy downstream proxy. Upweight judge so their gradient
		// dominates when the two disagree.
		judgeWeight = 10.0
		auditWeight = 1.0
		// L2 regularization on logits. Tuned so that at logit=±2
		// (softmax weight ≈ 0.88 vs the others) the pull-back per
		// step is comparable to a typical data gradient — anything
		// more makes L2 dominate and freezes learning, anything less
		// lets a noisy signal collapse the weights.
		l2Lambda = 0.0003
	)

	lossHistory := make([]float64, 0, epochs)

	for epoch := 0; epoch < epochs; epoch++ {
		rng.Shuffle(len(data.Pairs), func(i, j int) {
			data.Pairs[i], data.Pairs[j] = data.Pairs[j], data.Pairs[i]
		})

		epochLoss := 0.0
		weightSum := 0.0

		for start := 0; start < len(data.Pairs); start += batchSize {
			end := start + batchSize
			if end > len(data.Pairs) {
				end = len(data.Pairs)
			}
			batch := data.Pairs[start:end]
			gradLogits := [4]float64{}
			batchLoss := 0.0
			batchWeight := 0.0

			for _, p := range batch {
				pw := auditWeight
				if p.Src == "judge" {
					pw = judgeWeight
				}
				w := softmaxWeights(logits)
				sPos := scoreWith(p.Pos, w)
				sNeg := scoreWith(p.Neg, w)
				diff := sPos - sNeg
				sig := 1.0 / (1.0 + math.Exp(-diff))
				loss := -math.Log(math.Max(sig, 1e-12))
				batchLoss += pw * loss
				batchWeight += pw

				dLoss_dDiff := -(1.0 - sig) * pw
				featDiff := [4]float64{
					1.0/(service.RRFK()+float64(p.Pos.RankSemantic)) - 1.0/(service.RRFK()+float64(p.Neg.RankSemantic)),
					1.0/(service.RRFK()+float64(p.Pos.RankTag)) - 1.0/(service.RRFK()+float64(p.Neg.RankTag)),
					1.0/(service.RRFK()+float64(p.Pos.RankImportance)) - 1.0/(service.RRFK()+float64(p.Neg.RankImportance)),
					1.0/(service.RRFK()+float64(p.Pos.RankRecency)) - 1.0/(service.RRFK()+float64(p.Neg.RankRecency)),
				}
				ws := [4]float64{w.Sem, w.Tag, w.Imp, w.Rec}
				for j := 0; j < 4; j++ {
					sum := 0.0
					for i := 0; i < 4; i++ {
						var ind float64
						if i == j {
							ind = 1
						}
						dw_dLogit := ws[i] * (ind - ws[j])
						sum += featDiff[i] * dw_dLogit
					}
					gradLogits[j] += dLoss_dDiff * sum
				}
			}
			if batchWeight == 0 {
				continue
			}
			scale := 1.0 / batchWeight
			for j := 0; j < 4; j++ {
				// Data gradient + L2 pull-back toward 0 logit.
				// d/dlogit_j (λ/2 * logit²) = λ * logit.
				logits[j] -= learningRate * (gradLogits[j]*scale + l2Lambda*logits[j])
			}
			epochLoss += batchLoss
			weightSum += batchWeight
		}
		if weightSum > 0 {
			lossHistory = append(lossHistory, epochLoss/weightSum)
		}
	}

	w := softmaxWeights(logits)
	return w, lossHistory
}

// softmaxWeights returns the softmax of four logits, ordered
// [sem, tag, imp, rec]. Uses a max-subtract stabiliser so large
// logits don't overflow the exp.
func softmaxWeights(logits [4]float64) SignalWeights {
	m := logits[0]
	for _, v := range logits[1:] {
		if v > m {
			m = v
		}
	}
	exps := [4]float64{
		math.Exp(logits[0] - m),
		math.Exp(logits[1] - m),
		math.Exp(logits[2] - m),
		math.Exp(logits[3] - m),
	}
	sum := exps[0] + exps[1] + exps[2] + exps[3]
	if sum == 0 {
		return DefaultWeights()
	}
	return SignalWeights{
		Sem: exps[0] / sum,
		Tag: exps[1] / sum,
		Imp: exps[2] / sum,
		Rec: exps[3] / sum,
	}
}

// RerankWithWeights takes an already-scored list and re-sorts it
// under a new weight vector. Used to evaluate a learned model
// without re-running the whole selector pipeline.
func RerankWithWeights(scored []service.InjectedArtifact, w SignalWeights) []service.InjectedArtifact {
	out := make([]service.InjectedArtifact, len(scored))
	copy(out, scored)
	for i := range out {
		out[i].Score = scoreWith(out[i].Signals, w)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}
