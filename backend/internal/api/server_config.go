// Server-side Jellyfin configuration for the single tenant today (keyed by
// tenant_id so per-profile tokens can be added later). This resource reads and
// updates the same encrypted media_source row that pairing.go's
// readJellyfinConfig/saveJellyfinConfigIfAbsent use, so the pairing flow and
// this REST resource never diverge. The API key is never returned in full:
// GET reports only api_key_set and at most the last 4 characters as a hint.
package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerServerConfig(mux Router, db storage.DB, encKey [32]byte) {
	mux.HandleFunc("GET /server-config/jellyfin", getServerConfig(db, encKey))
	mux.HandleFunc("PUT /server-config/jellyfin", updateServerConfig(db, encKey))
}

// apiKeyHint returns at most the last 4 characters of key, or "" when unset.
// Short keys are not echoed in full (they would reveal more than a hint).
func apiKeyHint(key string) string {
	if len(key) <= 4 {
		return ""
	}
	return key[len(key)-4:]
}

type serverConfigResponse struct {
	BaseURL    string `json:"base_url"`
	APIKeySet  bool   `json:"api_key_set"`
	APIKeyHint string `json:"api_key_hint"`
}

// readServerConfigRow returns the tenant's raw jellyfin media_source row. found
// is false when no row exists; a row may exist with no base_url/credential.
func readServerConfigRow(ctx context.Context, db storage.DB) (domain.MediaSource, bool, error) {
	var ms domain.MediaSource
	err := db.Query(ctx, &ms,
		"SELECT "+mediaSourceColumns+" FROM media_source WHERE tenant_id = ? AND type = ? ORDER BY created_at LIMIT 1",
		DefaultTenantID, string(domain.SourceJellyfin),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ms, false, nil
	}
	if err != nil {
		return ms, false, err
	}
	return ms, true, nil
}

// getServerConfig is GET /server-config/jellyfin. It never decrypts more than
// needed and never writes the plaintext key anywhere.
func getServerConfig(db storage.DB, encKey [32]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := serverConfigResponse{}
		ms, found, err := readServerConfigRow(r.Context(), db)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if found {
			if ms.BaseURL != nil {
				resp.BaseURL = *ms.BaseURL
			}
			if len(ms.CredentialCipher) > 0 {
				plaintext, err := crypto.Decrypt(encKey, ms.CredentialCipher)
				if err != nil {
					handleDBError(w, err)
					return
				}
				if len(plaintext) > 0 {
					resp.APIKeySet = true
					resp.APIKeyHint = apiKeyHint(string(plaintext))
				}
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// validJellyfinBaseURL accepts only absolute http/https URLs with a host.
func validJellyfinBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

type updateServerConfigRequest struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// updateServerConfig is PUT /server-config/jellyfin. It validates base_url,
// updates the existing jellyfin row or creates it, and keeps the stored API key
// when the request omits it (an empty api_key with nothing stored is an error).
func updateServerConfig(db storage.DB, encKey [32]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req updateServerConfigRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.BaseURL == "" {
			writeError(w, http.StatusBadRequest, "base_url is required")
			return
		}
		if !validJellyfinBaseURL(req.BaseURL) {
			writeError(w, http.StatusBadRequest, "base_url must be an http or https URL with a host")
			return
		}

		ms, found, err := readServerConfigRow(r.Context(), db)
		if err != nil {
			handleDBError(w, err)
			return
		}

		// An empty api_key keeps the stored key; it is an error only when no
		// key is stored yet (including a row without a credential).
		var sealed []byte
		hintKey := req.APIKey
		if req.APIKey == "" {
			if !found || len(ms.CredentialCipher) == 0 {
				writeError(w, http.StatusBadRequest, "api_key is required when no key is stored")
				return
			}
			sealed = ms.CredentialCipher
			if plaintext, decErr := crypto.Decrypt(encKey, ms.CredentialCipher); decErr == nil {
				hintKey = string(plaintext)
			}
		} else {
			sealed, err = crypto.Encrypt(encKey, []byte(req.APIKey))
			if err != nil {
				handleDBError(w, err)
				return
			}
		}

		now := time.Now().UnixMilli()
		baseURL := req.BaseURL
		if found {
			err = db.Exec(r.Context(),
				`UPDATE media_source SET base_url = ?, credential_cipher = ?, updated_at = ?
				 WHERE tenant_id = ? AND type = ?`,
				baseURL, sealed, now, DefaultTenantID, string(domain.SourceJellyfin),
			)
		} else {
			err = db.Exec(r.Context(),
				`INSERT INTO media_source (id, tenant_id, type, name, base_url, credential_cipher, sync_mode, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?)`,
				uuid.NewString(), DefaultTenantID, string(domain.SourceJellyfin), "Jellyfin", baseURL, sealed,
				string(domain.SyncPull), now, now,
			)
		}
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, serverConfigResponse{
			BaseURL:    baseURL,
			APIKeySet:  len(sealed) > 0,
			APIKeyHint: apiKeyHint(hintKey),
		})
	}
}
