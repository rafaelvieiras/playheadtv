package domain

// PreferenceScope selects whether a StreamPreferenceRule applies tenant-wide
// (tier 4, the last admin fallback before "no opinion") or to one specific
// Channel (tier 3, overriding the tenant-wide default for everyone watching
// it). This is a separate enum from FillerScope even though the
// scope_type/scope_id shape is copied from FillerRule
// (backend/internal/domain/filler.go) — FillerScope's values (channel|block)
// are semantically wrong here: there is no "block" scope for stream
// selection, and "channel" here means something narrower (one channel's
// admin default, not "every block on this channel").
type PreferenceScope string

const (
	PreferenceScopeGlobal  PreferenceScope = "global"
	PreferenceScopeChannel PreferenceScope = "channel"
)

// StreamRuleStep is one ordered fallback step of a stream selection cascade
// (v1, deliberately simpler than ErsatzTV's SQL-like conditions — see
// docs/competitive-analysis/ersatztv.md section 6, "Custom Stream
// Selector"). Audio and subtitle are resolved together, atomically, from the
// same step — client-side resolution never picks them independently.
type StreamRuleStep struct {
	// AudioLanguage is a language tag, wildcard-capable (e.g. "pt*" matches
	// "pt", "pt-BR", "pt-PT"). Empty ("") matches any language — the
	// deliberate way to express "any audio track is fine" at this step.
	AudioLanguage string `json:"audio_language"`
	// SubtitleLanguage is optional: nil means this step has no subtitle
	// preference at all (audio-only match); a non-nil pointer requests a
	// subtitle at this step, wildcard-capable same as AudioLanguage, with ""
	// meaning "any subtitle language is fine".
	SubtitleLanguage *string `json:"subtitle_language"`
	// TitleBlocklist/TitleAllowlist match by case-insensitive substring
	// against a track's title metadata (e.g. block "Commentary"). A track
	// matching any blocklist entry is never selected at this step; when
	// TitleAllowlist is non-empty, only tracks matching at least one of its
	// entries are eligible.
	TitleBlocklist []string `json:"title_blocklist"`
	TitleAllowlist []string `json:"title_allowlist"`
}

// StreamRuleCascade is the typed shape of the JSON stored in
// UserStreamPreference.Rules / StreamPreferenceRule.Rules: an ordered list
// of fallback steps, most-preferred first. Client-side resolution (see
// client/lib) applies the first step whose criteria match an available
// audio(+subtitle) track pair.
type StreamRuleCascade struct {
	Steps []StreamRuleStep `json:"steps"`
}

// UserStreamPreference is tier 2 of the 4-tier stream selection cascade
// (see the tracking issue / AGENTS.md "Client app structure" for the
// others): the user's personal default, syncing across every device that
// user logs into via GET/PUT /stream-preferences/me. Rules is JSON-encoded
// (unmarshals to StreamRuleCascade) and stored as free-form text — same
// storage shape as FillerPool.Criteria
// (backend/internal/domain/filler.go) — because storage.scan (see
// internal/storage/scan.go) only maps scalar struct fields to columns; the
// API layer is what encodes/decodes this into the typed StreamRuleCascade,
// never leaving it as an opaque string past that boundary.
type UserStreamPreference struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id"`
	UserID    string `json:"user_id"`
	Rules     string `json:"rules"`
	UpdatedAt int64  `json:"updated_at"`
}

// StreamPreferenceRule is tiers 3 (channel) and 4 (global) of the cascade:
// admin-authored config read/written via GET/PUT /stream-preferences/config.
// ScopeID is "" for PreferenceScopeGlobal (at most one row per tenant) and a
// channel.id for PreferenceScopeChannel. Same JSON-as-text storage shape as
// UserStreamPreference.Rules, for the same reason.
type StreamPreferenceRule struct {
	ID        string          `json:"id"`
	TenantID  string          `json:"tenant_id"`
	Scope     PreferenceScope `json:"scope_type"`
	ScopeID   string          `json:"scope_id"`
	Rules     string          `json:"rules"`
	UpdatedAt int64           `json:"updated_at"`
}
