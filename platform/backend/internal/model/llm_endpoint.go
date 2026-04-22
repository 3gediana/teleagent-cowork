package model

// LLMEndpoint is a user-registered LLM connection. Think of it as a
// "provider instance" — concrete BaseURL + APIKey + model catalog that
// somebody filled in through the dashboard.
//
// One LLMEndpoint can expose multiple models (Anthropic's account-level
// key reaches every Claude model; an OpenRouter key reaches hundreds).
// Agents bind to a specific (endpoint, model) pair via RoleOverride,
// so swapping Claude Sonnet for Claude Opus is a role-level edit, not
// a new endpoint.
//
// Security: APIKey is stored plaintext in the DB (like most platforms
// that ship before a secrets manager lands). GET responses redact it.
// Human-only endpoints gate mutations — matches the same IsHuman
// contract used for /task/create, /chief/chat, etc.

import "time"

// LLMEndpoint is the persisted row. Models is a JSON array of model
// descriptors ([{id, name, supports_tools, ...}]) — GORM's JSON column
// serialization plays fine with []byte so we store it as raw JSON and
// decode lazily in the handler / llm loader.
type LLMEndpoint struct {
	ID string `gorm:"primaryKey;size:64" json:"id"`

	// Name is a human-friendly label shown in the model picker
	// ("MiniMax prod", "Anthropic staging"). Unique per deployment so
	// operators can rename freely without breaking RoleOverride links.
	Name string `gorm:"size:128;not null;uniqueIndex" json:"name"`

	// Format identifies the wire protocol. Two values:
	//   "openai"    — /chat/completions schema (MiniMax/DeepSeek/xAI/etc.)
	//   "anthropic" — /v1/messages schema
	Format string `gorm:"size:32;not null;index:idx_llm_format" json:"format"`

	// BaseURL is the /v1 root of the service, e.g. "https://api.minimaxi.com/v1".
	// Empty string defaults to the format's canonical URL (OpenAI's
	// api.openai.com/v1 or Anthropic's api.anthropic.com/v1). Handy for
	// "quick register" where the user has only an API key.
	BaseURL string `gorm:"size:512" json:"base_url"`

	// APIKey is sent verbatim as Authorization header / x-api-key. Never
	// returned by GET endpoints (handler layer redacts to "sk-...last4").
	APIKey string `gorm:"size:1024;not null" json:"-"`

	// Models is a JSON-encoded []ModelEntry. We keep it in-row instead
	// of normalising to a child table because models are only ever read
	// as a group ("list everything this endpoint exposes"), and rows
	// rarely exceed a handful of models per endpoint.
	Models string `gorm:"type:json" json:"-"`

	// DefaultModel is the model id used when a RoleOverride specifies
	// the endpoint but not a model — saves the user a second dropdown
	// for the common "one endpoint, one model" setup.
	DefaultModel string `gorm:"size:128" json:"default_model"`

	// Status gates usage. "active" = loaded into the runtime Registry
	// on startup / reload. "disabled" = DB row retained for audit/
	// history but skipped during registry load (avoid dangling tool
	// calls from orphaned RoleOverrides).
	Status string `gorm:"size:16;default:'active';index:idx_llm_status" json:"status"`

	// CreatedBy is the Agent.ID of the human that registered this
	// endpoint. Audit trail; surfaced on the management UI.
	CreatedBy string `gorm:"size:64" json:"created_by"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (LLMEndpoint) TableName() string { return "llm_endpoint" }

// RedactAPIKey returns a display-safe form of the stored key:
// "sk-abc...xyz" keeping 5 leading + 4 trailing chars. Used by
// handler serialization so the frontend can distinguish "same key as
// before" vs "needs re-entry" without exposing the secret.
func (e *LLMEndpoint) RedactAPIKey() string {
	return redact(e.APIKey)
}

func redact(s string) string {
	if len(s) <= 9 {
		// Too short to redact meaningfully — just stars.
		if s == "" {
			return ""
		}
		return "****"
	}
	return s[:5] + "..." + s[len(s)-4:]
}
