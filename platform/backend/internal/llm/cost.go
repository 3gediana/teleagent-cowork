package llm

// Cost tracking: turn a Usage row into a USD amount given the pricing
// attached to a ModelInfo.
//
// We compute cost at the provider level (right after Usage is known)
// so downstream consumers don't have to repeat the math. Pricing data
// comes from ModelInfo which itself is seeded from either config.yaml
// or from per-provider "seed" registrations in the adapter files.

// ComputeUSD turns a Usage and the model it came from into a dollar
// figure. Returns 0 when any price row is missing — we prefer silent
// zero over guessed pricing because a bad guess propagates into
// operator dashboards and misleads budget decisions.
//
// Caller is expected to pass the specific ModelInfo for the model ID
// in use (not any arbitrary one from the provider).
func ComputeUSD(u Usage, m ModelInfo) float64 {
	perM := func(tokens int, ratePerM float64) float64 {
		if ratePerM <= 0 {
			return 0
		}
		return float64(tokens) * ratePerM / 1_000_000.0
	}
	return perM(u.InputTokens, m.InputPricePerMTok) +
		perM(u.OutputTokens, m.OutputPricePerMTok) +
		perM(u.CacheReadTokens, m.CacheReadPricePerMTok) +
		perM(u.CacheCreationTokens, m.CacheCreatePricePerMTok)
}

// AttachCost mutates u in place to fill the USD field. Returns u for
// fluent-style usage at call sites.
func AttachCost(u Usage, m ModelInfo) Usage {
	u.USD = ComputeUSD(u, m)
	return u
}

// BuiltinPricing is a minimal seed table for models we've used or
// plan to use. Providers can override per instance by supplying their
// own ModelInfo list via config.yaml; this table is only consulted as
// a fallback when the adapter was registered without explicit pricing.
//
// Numbers are USD per million tokens at published list prices as of
// 2025-Q1. Keep in sync manually — automating this isn't worth the
// build-time fetch.
var BuiltinPricing = map[string]ModelInfo{
	// Anthropic.
	"claude-opus-4-5-20251015": {
		ID: "claude-opus-4-5-20251015", Name: "Claude Opus 4.5",
		ContextWindow: 200_000, MaxOutputTokens: 32_000,
		SupportsTools: true, SupportsVision: true, SupportsReasoning: true,
		InputPricePerMTok: 15.0, OutputPricePerMTok: 75.0,
		CacheReadPricePerMTok: 1.5, CacheCreatePricePerMTok: 18.75,
	},
	"claude-sonnet-4-5-20250929": {
		ID: "claude-sonnet-4-5-20250929", Name: "Claude Sonnet 4.5",
		ContextWindow: 200_000, MaxOutputTokens: 64_000,
		SupportsTools: true, SupportsVision: true, SupportsReasoning: true,
		InputPricePerMTok: 3.0, OutputPricePerMTok: 15.0,
		CacheReadPricePerMTok: 0.3, CacheCreatePricePerMTok: 3.75,
	},
	"claude-haiku-4-5-20251001": {
		ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5",
		ContextWindow: 200_000, MaxOutputTokens: 32_000,
		SupportsTools: true, SupportsVision: true, SupportsReasoning: false,
		InputPricePerMTok: 1.0, OutputPricePerMTok: 5.0,
		CacheReadPricePerMTok: 0.1, CacheCreatePricePerMTok: 1.25,
	},

	// OpenAI flagship lineup.
	"gpt-4o": {
		ID: "gpt-4o", Name: "GPT-4o",
		ContextWindow: 128_000, MaxOutputTokens: 16_384,
		SupportsTools: true, SupportsVision: true,
		InputPricePerMTok: 2.5, OutputPricePerMTok: 10.0,
	},
	"gpt-4o-mini": {
		ID: "gpt-4o-mini", Name: "GPT-4o mini",
		ContextWindow: 128_000, MaxOutputTokens: 16_384,
		SupportsTools: true, SupportsVision: true,
		InputPricePerMTok: 0.15, OutputPricePerMTok: 0.6,
	},
	"o1": {
		ID: "o1", Name: "OpenAI o1",
		ContextWindow: 200_000, MaxOutputTokens: 100_000,
		SupportsTools: false, SupportsVision: true, SupportsReasoning: true,
		InputPricePerMTok: 15.0, OutputPricePerMTok: 60.0,
	},
	"o3-mini": {
		ID: "o3-mini", Name: "OpenAI o3-mini",
		ContextWindow: 200_000, MaxOutputTokens: 100_000,
		SupportsTools: true, SupportsReasoning: true,
		InputPricePerMTok: 1.1, OutputPricePerMTok: 4.4,
	},

	// MiniMax (what we actually run today).
	"MiniMax-M2.7": {
		ID: "MiniMax-M2.7", Name: "MiniMax M2.7",
		ContextWindow: 204_800, MaxOutputTokens: 131_072,
		SupportsTools: true, SupportsReasoning: true,
		// MiniMax's "coding plan" pricing is flat at the subscription
		// level; per-token billing price is listed at 0.30/2.10 USD/Mtok.
		InputPricePerMTok: 0.30, OutputPricePerMTok: 2.10,
	},

	// Gemini.
	"gemini-2.0-flash": {
		ID: "gemini-2.0-flash", Name: "Gemini 2.0 Flash",
		ContextWindow: 1_048_576, MaxOutputTokens: 8_192,
		SupportsTools: true, SupportsVision: true,
		InputPricePerMTok: 0.1, OutputPricePerMTok: 0.4,
	},
	"gemini-2.0-pro": {
		ID: "gemini-2.0-pro", Name: "Gemini 2.0 Pro",
		ContextWindow: 2_097_152, MaxOutputTokens: 8_192,
		SupportsTools: true, SupportsVision: true,
		InputPricePerMTok: 1.25, OutputPricePerMTok: 5.0,
	},
}

// LookupPricing returns the baseline entry by exact model ID, or a
// zero-value ModelInfo (stub with just the ID filled in) if unknown.
// Adapters use this to enrich user-supplied ModelInfo rows that omit
// pricing.
func LookupPricing(modelID string) ModelInfo {
	if m, ok := BuiltinPricing[modelID]; ok {
		return m
	}
	return ModelInfo{ID: modelID, Name: modelID}
}

// MergePricing fills in zero-valued price fields of `into` from the
// builtin pricing table. Capability flags also fill if zero.
// Caller-supplied non-zero fields always win, so a deployment can
// override the table via config.yaml.
func MergePricing(into ModelInfo) ModelInfo {
	base := LookupPricing(into.ID)
	if into.InputPricePerMTok == 0 {
		into.InputPricePerMTok = base.InputPricePerMTok
	}
	if into.OutputPricePerMTok == 0 {
		into.OutputPricePerMTok = base.OutputPricePerMTok
	}
	if into.CacheReadPricePerMTok == 0 {
		into.CacheReadPricePerMTok = base.CacheReadPricePerMTok
	}
	if into.CacheCreatePricePerMTok == 0 {
		into.CacheCreatePricePerMTok = base.CacheCreatePricePerMTok
	}
	if into.ContextWindow == 0 {
		into.ContextWindow = base.ContextWindow
	}
	if into.MaxOutputTokens == 0 {
		into.MaxOutputTokens = base.MaxOutputTokens
	}
	if !into.SupportsTools && base.SupportsTools {
		into.SupportsTools = true
	}
	if !into.SupportsVision && base.SupportsVision {
		into.SupportsVision = true
	}
	if !into.SupportsReasoning && base.SupportsReasoning {
		into.SupportsReasoning = true
	}
	if into.Name == "" {
		into.Name = base.Name
	}
	return into
}
