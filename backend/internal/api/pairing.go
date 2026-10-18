// Temporary-PIN device pairing ("quick connect" style). An unconfigured client
// with no credentials calls POST /pair and gets a short PIN plus a one-time
// poll secret; the human opens GET /pin (see pairing_html.go), enters the PIN
// and their existing app_user credentials, and approves. The client polls
// POST /pair/poll with its poll secret and, once approved, receives a device
// token plus the tenant's Jellyfin URL and API key.
//
// Nothing here is authenticated by bearer token: the routes are public (see
// isOpenPath in auth.go) and carry their own credentials in the body. The raw
// device token is never stored — it is minted here at poll time, in the same
// storage.DB transaction that deletes the pairing row, so it is delivered
// exactly once. The Jellyfin URL/key are read from the existing jellyfin
// media_source row (reused rather than duplicated); when the tenant has none
// yet, /pin/approve can create it from the two extra form fields.
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Pairing lifetime and abuse limits. A pairing stays approvable for
// pairingTTL; the client polls every pairingPollInterval seconds. The caps
// mirror password_reset.go's: per-address windows for the two public write
// routes, plus a per-PIN window so one PIN can't be guessed from many
// addresses. maxPairingFailures deletes a pairing after that many wrong
// credentials against a valid PIN.
const (
	pairingTTL          = 10 * time.Minute
	pairingPollInterval = 3 // seconds, reported to the client

	pairingPinAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no I, O, 0, 1
	pairingPinLength   = 8

	maxPendingPairings = 20
	maxPairingFailures = 5

	pairingWindow       = 15 * time.Minute
	pairPerAddrLimit    = 10
	pollPerAddrLimit    = 600 // a 10-min pairing polls ~200 times; leave headroom
	approvePerAddrLimit = 10
	approvePerPinLimit  = 5
)

// pairingColumns is the explicit column list for domain.Pairing; the storage
// scanner matches columns to fields by snake_case.
const pairingColumns = "id, tenant_id, pin_hash, poll_secret_hash, name, platform, status, approved_by, expires_at, failed_attempts, created_at"

// errPairingNotFound keeps "unknown poll secret" and "expired" indistinguishable
// so the poll response is not an oracle for which pairings exist.
var errPairingNotFound = errors.New("pairing not found or expired")

func registerPairing(mux Router, db storage.DB, encKey [32]byte) {
	mux.HandleFunc("POST /pair", createPairing(db))
	mux.HandleFunc("POST /pair/poll", pollPairing(db, encKey))
	mux.HandleFunc("POST /pin/approve", approvePairing(db, encKey))
}

// generatePairingPin returns a PIN in the shown "XXXX-XXXX" form, drawn
// uniformly from pairingPinAlphabet with rejection sampling (a plain modulo
// would bias toward the first letters).
func generatePairingPin() (string, error) {
	buf := make([]byte, 1)
	out := make([]byte, 0, pairingPinLength)
	for len(out) < pairingPinLength {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		v := int(buf[0])
		// Reject the tail that would not divide evenly over the alphabet.
		if v >= 256-(256%len(pairingPinAlphabet)) {
			continue
		}
		out = append(out, pairingPinAlphabet[v%len(pairingPinAlphabet)])
	}
	return string(out[:4]) + "-" + string(out[4:]), nil
}

// generatePollSecret returns 32 random bytes as 64 lowercase hex characters,
// matching the value POST /pair hands back (only its hash is stored).
func generatePollSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// normalizePairingPin upper-cases the PIN and strips the display dash and any
// spaces, so "abcd-efgh", "ABCD EFGH" and "ABCDEFGH" all compare equal.
func normalizePairingPin(pin string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(pin) {
		if r == '-' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// validPlatform is the same check createDevice applies; /pair validates at
// creation time so the platform is already known to be valid when the device
// row is finally created.
func validPlatform(p domain.Platform) bool {
	switch p {
	case domain.PlatformLinux, domain.PlatformWindows, domain.PlatformAndroid,
		domain.PlatformAndroidTV, domain.PlatformIOS, domain.PlatformTVOS:
		return true
	}
	return false
}

// cleanupExpiredPairings drops expired rows for the tenant. Called when a new
// pairing is requested so an abandoned client can't fill the pending cap.
func cleanupExpiredPairings(ctx context.Context, db storage.DB, now int64) error {
	return db.Exec(ctx, "DELETE FROM pairing WHERE tenant_id = ? AND expires_at <= ?", DefaultTenantID, now)
}

type createPairingRequest struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

type createPairingResponse struct {
	Pin          string `json:"pin"`
	PollSecret   string `json:"poll_secret"`
	ExpiresIn    int    `json:"expires_in"`
	PollInterval int    `json:"poll_interval"`
}

// createPairing is POST /pair. It returns the PIN (also shown as a QR) and the
// poll secret; neither is retrievable afterwards.
func createPairing(db storage.DB) http.HandlerFunc {
	byAddr := newAttemptLimiter(pairPerAddrLimit, pairingWindow)
	return func(w http.ResponseWriter, r *http.Request) {
		if !byAddr.allow(clientAddr(r)) {
			w.Header().Set("Retry-After", strconv.Itoa(int(pairingWindow.Seconds())))
			writeError(w, http.StatusTooManyRequests, "too many pairing requests, try again later")
			return
		}

		var req createPairingRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		platform := domain.Platform(req.Platform)
		if !validPlatform(platform) {
			writeError(w, http.StatusBadRequest, "platform must be one of linux, windows, android, android_tv, ios, tvos")
			return
		}

		now := time.Now().UnixMilli()
		if err := cleanupExpiredPairings(r.Context(), db, now); err != nil {
			handleDBError(w, err)
			return
		}
		var pending []int64
		if err := db.Query(r.Context(), &pending,
			"SELECT COUNT(*) FROM pairing WHERE tenant_id = ?", DefaultTenantID,
		); err != nil {
			handleDBError(w, err)
			return
		}
		if len(pending) == 1 && pending[0] >= maxPendingPairings {
			writeError(w, http.StatusTooManyRequests, "too many pending pairings, approve or wait for one to expire")
			return
		}

		pin, err := generatePairingPin()
		if err != nil {
			logging.Errorf("api: generate pairing PIN: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		pollSecret, err := generatePollSecret()
		if err != nil {
			logging.Errorf("api: generate poll secret: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		err = db.Exec(r.Context(),
			`INSERT INTO pairing (id, tenant_id, pin_hash, poll_secret_hash, name, platform, status, approved_by, expires_at, failed_attempts, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, 0, ?)`,
			uuid.NewString(), DefaultTenantID, hashToken(normalizePairingPin(pin)), hashToken(pollSecret),
			req.Name, string(platform), string(domain.PairingPending), now+pairingTTL.Milliseconds(), now,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, createPairingResponse{
			Pin:          pin,
			PollSecret:   pollSecret,
			ExpiresIn:    int(pairingTTL.Seconds()),
			PollInterval: pairingPollInterval,
		})
	}
}

type pollPairingRequest struct {
	PollSecret string `json:"poll_secret"`
}

type pollPairingResponse struct {
	Status        string `json:"status"`
	DeviceToken   string `json:"device_token,omitempty"`
	JellyfinURL   string `json:"jellyfin_url,omitempty"`
	JellyfinKey   string `json:"jellyfin_api_key,omitempty"`
}

// newDevice mints the device token and the row createDevice would persist. It
// is shared by createDevice (authenticated registration) and pollPairing so
// the two can never drift.
func newDevice(name string, platform domain.Platform) (domain.Device, string, error) {
	token, err := generateToken()
	if err != nil {
		return domain.Device{}, "", err
	}
	return domain.Device{
		ID:           uuid.NewString(),
		TenantID:     DefaultTenantID,
		Name:         name,
		Platform:     platform,
		APITokenHash: hashToken(token),
		CreatedAt:    time.Now().UnixMilli(),
	}, token, nil
}

// insertDevice writes a device row through the given storage handle, so the
// poll path can do it inside its transaction.
func insertDevice(ctx context.Context, db storage.DB, d domain.Device) error {
	return db.Exec(ctx,
		`INSERT INTO device (id, tenant_id, name, platform, api_token_hash, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.TenantID, d.Name, string(d.Platform), d.APITokenHash, d.LastSeenAt, d.CreatedAt,
	)
}

// readJellyfinConfig reads the tenant's jellyfin media_source row and decrypts
// its credential. found is false when no row (or no credential) exists yet, in
// which case the URL/key are empty and the client falls back to manual setup.
func readJellyfinConfig(ctx context.Context, db storage.DB, encKey [32]byte) (string, string, bool, error) {
	var ms domain.MediaSource
	err := db.Query(ctx, &ms,
		"SELECT "+mediaSourceColumns+" FROM media_source WHERE tenant_id = ? AND type = ? ORDER BY created_at LIMIT 1",
		DefaultTenantID, string(domain.SourceJellyfin),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if ms.BaseURL == nil || len(ms.CredentialCipher) == 0 {
		return "", "", false, nil
	}
	plaintext, err := crypto.Decrypt(encKey, ms.CredentialCipher)
	if err != nil {
		return "", "", false, err
	}
	return *ms.BaseURL, string(plaintext), true, nil
}

// pollPairing is POST /pair/poll. While the pairing is pending it answers
// {"status":"pending"}; once approved it mints the device row, deletes the
// pairing row and returns the device token plus the Jellyfin config, all in
// one transaction, so the token is handed out exactly once. An unknown or
// expired poll secret is a 404, indistinguishable between the two.
func pollPairing(db storage.DB, encKey [32]byte) http.HandlerFunc {
	byAddr := newAttemptLimiter(pollPerAddrLimit, pairingWindow)
	return func(w http.ResponseWriter, r *http.Request) {
		if !byAddr.allow(clientAddr(r)) {
			w.Header().Set("Retry-After", strconv.Itoa(int(pairingWindow.Seconds())))
			writeError(w, http.StatusTooManyRequests, "too many requests, try again later")
			return
		}

		var req pollPairingRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.PollSecret == "" {
			writeError(w, http.StatusBadRequest, "poll_secret is required")
			return
		}
		presented := hashToken(req.PollSecret)
		now := time.Now().UnixMilli()

		var deviceToken, jellyfinURL, jellyfinKey string
		// missing records an unknown/expired secret without returning an error
		// from the transaction callback: storage.Transact rolls back on a
		// non-nil error, which would undo the expired-row DELETE below.
		var missing bool
		err := db.Transact(r.Context(), func(tx storage.DB) error {
			var rows []domain.Pairing
			if err := tx.Query(r.Context(), &rows,
				"SELECT "+pairingColumns+" FROM pairing WHERE tenant_id = ?", DefaultTenantID,
			); err != nil {
				return err
			}
			var match *domain.Pairing
			for i := range rows { // no early exit: every row is compared
				if subtle.ConstantTimeCompare([]byte(rows[i].PollSecretHash), []byte(presented)) == 1 {
					match = &rows[i]
				}
			}
			if match == nil {
				missing = true
				return nil
			}
			if match.ExpiresAt <= now {
				if err := tx.Exec(r.Context(), "DELETE FROM pairing WHERE id = ?", match.ID); err != nil {
					return err
				}
				missing = true
				return nil
			}
			if match.Status != domain.PairingApproved {
				return nil // still waiting for the human
			}

			device, token, err := newDevice(match.Name, match.Platform)
			if err != nil {
				return err
			}
			if err := insertDevice(r.Context(), tx, device); err != nil {
				return err
			}
			if err := tx.Exec(r.Context(), "DELETE FROM pairing WHERE id = ?", match.ID); err != nil {
				return err
			}
			url, key, found, err := readJellyfinConfig(r.Context(), tx, encKey)
			if err != nil {
				return err
			}
			deviceToken = token
			if found {
				jellyfinURL, jellyfinKey = url, key
			}
			return nil
		})
		if err != nil {
			handleDBError(w, err)
			return
		}
		if missing {
			writeError(w, http.StatusNotFound, "pairing not found or expired")
			return
		}

		if deviceToken == "" {
			writeJSON(w, http.StatusOK, pollPairingResponse{Status: string(domain.PairingPending)})
			return
		}
		writeJSON(w, http.StatusOK, pollPairingResponse{
			Status:      string(domain.PairingApproved),
			DeviceToken: deviceToken,
			JellyfinURL: jellyfinURL,
			JellyfinKey: jellyfinKey,
		})
	}
}

type approvePairingRequest struct {
	Pin            string `json:"pin"`
	Email          string `json:"email"`
	Password       string `json:"password"`
	JellyfinURL    string `json:"jellyfin_url"`
	JellyfinAPIKey string `json:"jellyfin_api_key"`
}

// decodeApproval accepts either a JSON body or a plain HTML form post (the
// browser page has no JS), so both the SPA-less /pin form and a JSON caller
// work.
func decodeApproval(w http.ResponseWriter, r *http.Request) (approvePairingRequest, bool) {
	var req approvePairingRequest
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/x-www-form-urlencoded") || strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return req, false
		}
		req.Pin = r.PostFormValue("pin")
		req.Email = r.PostFormValue("email")
		req.Password = r.PostFormValue("password")
		req.JellyfinURL = r.PostFormValue("jellyfin_url")
		req.JellyfinAPIKey = r.PostFormValue("jellyfin_api_key")
		return req, true
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return req, false
	}
	return req, true
}

// saveJellyfinConfigIfAbsent creates the tenant's jellyfin media_source row
// only when it has none yet (the pairing form only offers the fields then, so
// this never overwrites an existing configuration). sync_mode is "pull": the
// pairing client talks to Jellyfin directly, and the operator can switch it in
// settings if the backend cannot reach that URL.
func saveJellyfinConfigIfAbsent(ctx context.Context, db storage.DB, encKey [32]byte, baseURL, apiKey string) error {
	var counts []int64
	if err := db.Query(ctx, &counts,
		"SELECT COUNT(*) FROM media_source WHERE tenant_id = ? AND type = ?",
		DefaultTenantID, string(domain.SourceJellyfin),
	); err != nil {
		return err
	}
	if len(counts) == 1 && counts[0] > 0 {
		return nil
	}
	sealed, err := crypto.Encrypt(encKey, []byte(apiKey))
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	u := baseURL
	return db.Exec(ctx,
		`INSERT INTO media_source (id, tenant_id, type, name, base_url, credential_cipher, sync_mode, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?)`,
		uuid.NewString(), DefaultTenantID, string(domain.SourceJellyfin), "Jellyfin", u, sealed,
		string(domain.SyncPull), now, now,
	)
}

// approvePairing is POST /pin/approve. It authenticates the app_user with the
// e-mail/password in the body and, on success, marks the pairing approved and
// records the approver. Every failure — unknown PIN, unknown e-mail, wrong
// password — answers the same 400 so the response is not an oracle. A wrong
// password against a valid PIN counts against that pairing row, which is
// deleted after maxPairingFailures.
func approvePairing(db storage.DB, encKey [32]byte) http.HandlerFunc {
	byAddr := newAttemptLimiter(approvePerAddrLimit, pairingWindow)
	byPin := newAttemptLimiter(approvePerPinLimit, pairingWindow)
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeApproval(w, r)
		if !ok {
			return
		}
		if req.Pin == "" || req.Email == "" || req.Password == "" {
			writeError(w, http.StatusBadRequest, "pin, email and password are required")
			return
		}
		pinHash := hashToken(normalizePairingPin(req.Pin))
		okAddr := byAddr.allow(clientAddr(r))
		okPin := byPin.allow(pinHash)
		if !okAddr || !okPin {
			w.Header().Set("Retry-After", strconv.Itoa(int(pairingWindow.Seconds())))
			writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
			return
		}

		approved := false
		err := db.Transact(r.Context(), func(tx storage.DB) error {
			// Credentials first: a wrong password must not short-circuit the
			// PIN lookup, and the PIN comparison below never early-exits.
			var user domain.User
			uerr := tx.Query(r.Context(), &user,
				"SELECT id, tenant_id, email, password_hash, created_at FROM app_user WHERE tenant_id = ? AND email = ?",
				DefaultTenantID, req.Email,
			)
			credsOK := false
			switch {
			case uerr == nil:
				credsOK = checkPassword(user.PasswordHash, req.Password)
			case errors.Is(uerr, sql.ErrNoRows):
				// Unknown e-mail: fall through with credsOK false.
			default:
				return uerr
			}

			var rows []domain.Pairing
			if err := tx.Query(r.Context(), &rows,
				"SELECT "+pairingColumns+" FROM pairing WHERE tenant_id = ?", DefaultTenantID,
			); err != nil {
				return err
			}
			now := time.Now().UnixMilli()
			var match *domain.Pairing
			for i := range rows {
				if subtle.ConstantTimeCompare([]byte(rows[i].PinHash), []byte(pinHash)) == 1 {
					match = &rows[i]
				}
			}
			if match == nil {
				return nil
			}
			if match.ExpiresAt <= now {
				return tx.Exec(r.Context(), "DELETE FROM pairing WHERE id = ?", match.ID)
			}
			if !credsOK {
				if match.FailedAttempts+1 >= maxPairingFailures {
					return tx.Exec(r.Context(), "DELETE FROM pairing WHERE id = ?", match.ID)
				}
				return tx.Exec(r.Context(),
					"UPDATE pairing SET failed_attempts = failed_attempts + 1 WHERE id = ?", match.ID)
			}

			if err := tx.Exec(r.Context(),
				"UPDATE pairing SET status = ?, approved_by = ? WHERE id = ?",
				string(domain.PairingApproved), user.ID, match.ID,
			); err != nil {
				return err
			}
			approved = true
			if req.JellyfinURL != "" && req.JellyfinAPIKey != "" {
				if err := saveJellyfinConfigIfAbsent(r.Context(), tx, encKey, req.JellyfinURL, req.JellyfinAPIKey); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			handleDBError(w, err)
			return
		}
		if !approved {
			writeError(w, http.StatusBadRequest, "invalid PIN or credentials")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": string(domain.PairingApproved)})
	}
}
