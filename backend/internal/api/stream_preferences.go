// Stream (audio/subtitle) selection preferences: tier 2 (the caller's own
// user profile, GET/PUT /stream-preferences/me) and tiers 3-4 (admin
// channel/global config, GET/PUT /stream-preferences/config), plus a
// lightweight GET /stream-preferences/meta so the client can poll for
// staleness without paying for the full payload every time. Tier 1
// (item/channel local override) never reaches the backend at all — see
// domain/stream_preference.go and the tracking issue for the full 4-tier
// design. The backend only stores and serves tiers 2-4; it never evaluates
// the cascade against real track data (that only happens client-side, at
// playback time, against the player's own getMediaInfo()).
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerStreamPreferences(mux Router, db storage.DB) {
	mux.HandleFunc("GET /stream-preferences/me", getMyStreamPreference(db))
	mux.HandleFunc("PUT /stream-preferences/me", putMyStreamPreference(db))
	mux.HandleFunc("GET /stream-preferences/config", getStreamPreferenceRule(db))
	mux.HandleFunc("PUT /stream-preferences/config", putStreamPreferenceRule(db))
	mux.HandleFunc("GET /stream-preferences/meta", getStreamPreferencesMeta(db))
}

const userStreamPreferenceColumns = "id, tenant_id, user_id, rules, updated_at"

// streamPreferenceRuleColumns aliases scope_type to the struct field name
// domain.StreamPreferenceRule actually has (Scope) — same reason/pattern as
// fillerRuleColumns in internal/api/filler.go.
const streamPreferenceRuleColumns = "id, tenant_id, scope_type AS scope, scope_id, rules, updated_at"

// streamRuleCascadeRequest is the request/response body for both tiers'
// rules — domain.StreamRuleCascade already carries the right JSON tags, so
// this just names the wrapping shape (a single "rules" field) shared by both
// endpoints, keeping the typed cascade itself defined once in domain.
type streamRuleCascadeRequest struct {
	Rules domain.StreamRuleCascade `json:"rules"`
}

// validateStreamRuleCascade keeps v1 validation intentionally light (per the
// issue's "keep it simple" rule content section): reject an obviously
// malformed step rather than trying to fully validate language tag syntax.
// A cap on step count guards against an unbounded cascade someone pastes in
// by mistake, not a real expected use case.
const maxStreamRuleSteps = 20

func validateStreamRuleCascade(c domain.StreamRuleCascade) (string, bool) {
	if len(c.Steps) > maxStreamRuleSteps {
		return "rules.steps must have at most 20 entries", false
	}
	for _, step := range c.Steps {
		for _, t := range step.TitleBlocklist {
			if t == "" {
				return "rules.steps[].title_blocklist entries must not be empty", false
			}
		}
		for _, t := range step.TitleAllowlist {
			if t == "" {
				return "rules.steps[].title_allowlist entries must not be empty", false
			}
		}
	}
	return "", true
}

func decodeRules(raw string) (domain.StreamRuleCascade, error) {
	var c domain.StreamRuleCascade
	if raw == "" {
		return c, nil
	}
	err := json.Unmarshal([]byte(raw), &c)
	return c, err
}

func encodeRules(c domain.StreamRuleCascade) (string, error) {
	encoded, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// --- Tier 2: the caller's own user profile ---

func fetchMyStreamPreference(db storage.DB, r *http.Request, userID string) (domain.UserStreamPreference, error) {
	var p domain.UserStreamPreference
	err := db.Query(r.Context(), &p,
		"SELECT "+userStreamPreferenceColumns+" FROM stream_preference WHERE tenant_id = ? AND user_id = ?",
		DefaultTenantID, userID,
	)
	return p, err
}

func writeStreamPreference(w http.ResponseWriter, status int, p domain.UserStreamPreference) {
	rules, err := decodeRules(p.Rules)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, status, struct {
		ID        string                   `json:"id"`
		UserID    string                   `json:"user_id"`
		Rules     domain.StreamRuleCascade `json:"rules"`
		UpdatedAt int64                    `json:"updated_at"`
	}{ID: p.ID, UserID: p.UserID, Rules: rules, UpdatedAt: p.UpdatedAt})
}

// getMyStreamPreference returns an empty cascade (not a 404) when the caller
// has never set one yet — a fresh user shouldn't need to special-case "no
// row" before it can read its own preference, same reasoning getEPGMeta
// already applies to a tenant with no epg_revision row yet.
func getMyStreamPreference(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := r.Context().Value(ctxUserID).(string)
		if !ok || userID == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		p, err := fetchMyStreamPreference(db, r, userID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeStreamPreference(w, http.StatusOK, domain.UserStreamPreference{UserID: userID})
				return
			}
			handleDBError(w, err)
			return
		}
		writeStreamPreference(w, http.StatusOK, p)
	}
}

// putMyStreamPreference upserts the caller's own row by user_id (the
// table's UNIQUE constraint) — the ON CONFLICT clause preserves the
// existing id/created semantics across updates instead of minting a fresh
// id every time the user edits their preference.
func putMyStreamPreference(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := r.Context().Value(ctxUserID).(string)
		if !ok || userID == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req streamRuleCascadeRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg, ok := validateStreamRuleCascade(req.Rules); !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		rules, err := encodeRules(req.Rules)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		err = db.Exec(r.Context(),
			`INSERT INTO stream_preference (id, tenant_id, user_id, rules, updated_at) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (user_id) DO UPDATE SET rules = excluded.rules, updated_at = excluded.updated_at`,
			uuid.NewString(), DefaultTenantID, userID, rules, time.Now().UnixMilli(),
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		p, err := fetchMyStreamPreference(db, r, userID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeStreamPreference(w, http.StatusOK, p)
	}
}

// --- Tiers 3-4: admin channel/global config ---

// parseScopeQuery reads scope_type/scope_id query params, validates
// scope_type, and normalizes scope_id to "" for PreferenceScopeGlobal
// (there's only ever one global row per tenant, so any scope_id given
// alongside scope_type=global is ignored rather than rejected — a client
// always sending scope_id="" for global is not something worth requiring).
func parseScopeQuery(r *http.Request) (domain.PreferenceScope, string, string, bool) {
	q := r.URL.Query()
	scope := domain.PreferenceScope(q.Get("scope_type"))
	switch scope {
	case domain.PreferenceScopeGlobal:
		return scope, "", "", true
	case domain.PreferenceScopeChannel:
		scopeID := q.Get("scope_id")
		if scopeID == "" {
			return "", "", "scope_id is required when scope_type=channel", false
		}
		return scope, scopeID, "", true
	default:
		return "", "", "scope_type must be one of global, channel", false
	}
}

func fetchStreamPreferenceRule(db storage.DB, r *http.Request, scope domain.PreferenceScope, scopeID string) (domain.StreamPreferenceRule, error) {
	var rule domain.StreamPreferenceRule
	err := db.Query(r.Context(), &rule,
		"SELECT "+streamPreferenceRuleColumns+" FROM stream_preference_rule WHERE tenant_id = ? AND scope_type = ? AND scope_id = ?",
		DefaultTenantID, string(scope), scopeID,
	)
	return rule, err
}

func writeStreamPreferenceRule(w http.ResponseWriter, status int, rule domain.StreamPreferenceRule) {
	rules, err := decodeRules(rule.Rules)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, status, struct {
		ID        string                   `json:"id"`
		ScopeType string                   `json:"scope_type"`
		ScopeID   string                   `json:"scope_id"`
		Rules     domain.StreamRuleCascade `json:"rules"`
		UpdatedAt int64                    `json:"updated_at"`
	}{ID: rule.ID, ScopeType: string(rule.Scope), ScopeID: rule.ScopeID, Rules: rules, UpdatedAt: rule.UpdatedAt})
}

// getStreamPreferenceRule mirrors getMyStreamPreference's "no row yet ->
// empty cascade, not 404" choice: an admin reading a channel's config before
// ever setting one shouldn't have to special-case a 404 first.
func getStreamPreferenceRule(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, scopeID, msg, ok := parseScopeQuery(r)
		if !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		rule, err := fetchStreamPreferenceRule(db, r, scope, scopeID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeStreamPreferenceRule(w, http.StatusOK, domain.StreamPreferenceRule{Scope: scope, ScopeID: scopeID})
				return
			}
			handleDBError(w, err)
			return
		}
		writeStreamPreferenceRule(w, http.StatusOK, rule)
	}
}

func putStreamPreferenceRule(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, scopeID, msg, ok := parseScopeQuery(r)
		if !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		var req streamRuleCascadeRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg, ok := validateStreamRuleCascade(req.Rules); !ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		rules, err := encodeRules(req.Rules)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		err = db.Exec(r.Context(),
			`INSERT INTO stream_preference_rule (id, tenant_id, scope_type, scope_id, rules, updated_at) VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT (tenant_id, scope_type, scope_id) DO UPDATE SET rules = excluded.rules, updated_at = excluded.updated_at`,
			uuid.NewString(), DefaultTenantID, string(scope), scopeID, rules, time.Now().UnixMilli(),
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		rule, err := fetchStreamPreferenceRule(db, r, scope, scopeID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeStreamPreferenceRule(w, http.StatusOK, rule)
	}
}

// --- Meta: cheap revision marker for polling ---

type streamPreferencesMetaResponse struct {
	Revision int64 `json:"revision"`
}

// getStreamPreferencesMeta mirrors getEPGMeta/epgMetaResponse structurally
// (backend/internal/api/epg.go) but deliberately uses a plain
// updated_at-based revision instead of a dedicated counter table like
// epg_revision: epg_revision exists because the EPG materializer does a
// full-window regeneration and needs a version stamp decoupled from any one
// row's timestamp, which doesn't apply to plain CRUD config rows like
// these — the max updated_at across the rows that matter is already
// correct and monotonic, at no extra storage/write cost. Covers both the
// caller's own tier-2 row and every tier-3/4 row for the tenant (a channel
// config change must be visible to this poll even though it's a different
// row than the user's own).
func getStreamPreferencesMeta(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := r.Context().Value(ctxUserID).(string)
		if !ok || userID == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var revs []int64
		err := db.Query(r.Context(), &revs,
			`SELECT COALESCE(MAX(u), 0) FROM (
				SELECT updated_at AS u FROM stream_preference WHERE tenant_id = ? AND user_id = ?
				UNION ALL
				SELECT updated_at AS u FROM stream_preference_rule WHERE tenant_id = ?
			)`,
			DefaultTenantID, userID, DefaultTenantID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		var revision int64
		if len(revs) == 1 {
			revision = revs[0]
		}
		writeJSON(w, http.StatusOK, streamPreferencesMetaResponse{Revision: revision})
	}
}
