package llm

import (
	"math"
	"testing"
)

func TestComputeUSD_SumsAllFourComponents(t *testing.T) {
	m := ModelInfo{
		InputPricePerMTok:       2.0,  // $ per million
		OutputPricePerMTok:      10.0,
		CacheReadPricePerMTok:   0.5,
		CacheCreatePricePerMTok: 2.5,
	}
	u := Usage{
		InputTokens:         1_000_000,
		OutputTokens:        500_000,
		CacheReadTokens:     200_000,
		CacheCreationTokens: 100_000,
	}
	got := ComputeUSD(u, m)
	// Expected: 1.0M × 2.0  + 0.5M × 10 + 0.2M × 0.5 + 0.1M × 2.5 = 2 + 5 + 0.1 + 0.25 = 7.35
	want := 7.35
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("ComputeUSD: got %v want %v", got, want)
	}
}

func TestComputeUSD_MissingPriceGivesZero(t *testing.T) {
	// Silent zero is the right default — we refuse to guess pricing
	// because bad guesses leak into operator dashboards.
	m := ModelInfo{ID: "unknown-model"}
	u := Usage{InputTokens: 1000, OutputTokens: 1000}
	if got := ComputeUSD(u, m); got != 0 {
		t.Errorf("unknown model should cost 0, got %v", got)
	}
}

func TestMergePricing_FillsZerosFromBuiltinTable(t *testing.T) {
	// Operator lists a MiniMax model by id without filling in pricing.
	// MergePricing must populate the fields from BuiltinPricing.
	in := ModelInfo{ID: "MiniMax-M2.7"}
	out := MergePricing(in)
	if out.InputPricePerMTok == 0 {
		t.Error("InputPricePerMTok should be filled from builtin table")
	}
	if out.ContextWindow == 0 {
		t.Error("ContextWindow should be filled from builtin table")
	}
	if !out.SupportsTools {
		t.Error("SupportsTools should be true for MiniMax-M2.7")
	}
}

func TestMergePricing_DoesNotOverwriteExplicitFields(t *testing.T) {
	// Operator-supplied price wins over builtin — lets deployments
	// reflect negotiated rates without editing Go code.
	in := ModelInfo{ID: "MiniMax-M2.7", InputPricePerMTok: 999.99}
	out := MergePricing(in)
	if out.InputPricePerMTok != 999.99 {
		t.Errorf("user-supplied price should win, got %v", out.InputPricePerMTok)
	}
}

func TestLookupPricing_UnknownReturnsStubNotZero(t *testing.T) {
	got := LookupPricing("no-such-model")
	if got.ID != "no-such-model" {
		t.Errorf("stub should echo the id, got %q", got.ID)
	}
	if got.Name != "no-such-model" {
		t.Errorf("stub should default Name to id, got %q", got.Name)
	}
	if got.InputPricePerMTok != 0 {
		t.Errorf("unknown model must have zero input price, got %v", got.InputPricePerMTok)
	}
}

func TestAttachCost_IsFluentAndAccurate(t *testing.T) {
	m := ModelInfo{InputPricePerMTok: 1, OutputPricePerMTok: 2}
	u := Usage{InputTokens: 500_000, OutputTokens: 250_000}
	u = AttachCost(u, m)
	want := 1.0 // 0.5M * 1 + 0.25M * 2
	if math.Abs(u.USD-want) > 1e-9 {
		t.Errorf("USD: got %v want %v", u.USD, want)
	}
}
