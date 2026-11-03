// Auth: bootstrap of the single app_user this tenant has, session tokens for
// the web UI/MCP, and device tokens for playback clients. There is still no
// real multi-tenant logic — every row created here uses DefaultTenantID, same
// as every other handler in this package (see api.go's doc comment) — this
// slice only adds "is the caller who they say they are" on top of that one
// implicit tenant.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

// sessionTTL is how long a POST /sessions token stays valid.
const sessionTTL = 30 * 24 * time.Hour

// minPasswordLength is a floor, not real password-strength enforcement — the
// point is to reject "1234" during bootstrap, not to replace a proper policy.
const minPasswordLength = 8

// ctxKey namespaces context values this package injects after authentication
// so they can't collide with keys set by net/http or other packages.
type ctxKey int

const (
	ctxUserID ctxKey = iota
	ctxTokenHash
	ctxDeviceID
)

// hashPassword bcrypt-hashes a human-chosen password for storage in
// app_user.password_hash.
func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// checkPassword reports whether password matches hash. bcrypt's own
// comparison is already constant-time; the bool return just hides whatever
// internal error CompareHashAndPassword produced (malformed hash, mismatch —
// callers must not be able to distinguish those).
func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

var (
	timingPadOnce sync.Once
	timingPadHash string
)

// burnPasswordCheck spends the same bcrypt time a real check would. Callers use
// it when the e-mail is unknown so response time does not reveal which e-mails
// have an account.
func burnPasswordCheck(password string) {
	timingPadOnce.Do(func() {
		if h, err := hashPassword("playheadtv-timing-pad"); err == nil {
			timingPadHash = h
		}
	})
	_ = checkPassword(timingPadHash, password)
}

// generateToken returns a high-entropy, URL-safe opaque token — used both for
// session tokens and device tokens. The raw value is only ever handed to the
// caller once, at creation time; only its hash is persisted.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken derives the value stored alongside a token (session.token_hash,
// device.api_token_hash). Tokens are already high-entropy random values we
// generated ourselves, not human-chosen secrets, so a plain fast hash is the
// right tool here — bcrypt's deliberate slowness defends against guessing a
// low-entropy secret, which doesn't apply to a 256-bit random token. This is
// the same approach GitHub/GitLab use for personal access tokens.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func registerAuth(mux Router, db storage.DB) {
	mux.HandleFunc("POST /app-users", bootstrapAppUser(db))
	mux.HandleFunc("POST /sessions", login(db))
	mux.HandleFunc("DELETE /sessions", logout(db))
	mux.HandleFunc("GET /me", getMe(db))
	mux.HandleFunc("POST /password-resets/consume", consumePasswordReset(db))
	mux.HandleFunc("POST /devices", createDevice(db))
	mux.HandleFunc("GET /devices", listDevices(db))
	mux.HandleFunc("DELETE /devices/{id}", deleteDevice(db))
}

type appUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// bootstrapAppUser creates the tenant's one and only app_user. There's no
// invite/registration flow yet (no auth roadmap item covers multi-user), so
// this only ever succeeds once per tenant — after that, credentials are
// recovered with `server reset-password` (password_reset.go) until a real
// user-management slice exists.
func bootstrapAppUser(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req appUserRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Email == "" || req.Password == "" {
			writeError(w, http.StatusBadRequest, "email and password are required")
			return
		}
		if len(req.Password) < minPasswordLength {
			writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}

		var counts []int64
		if err := db.Query(r.Context(), &counts,
			"SELECT COUNT(*) FROM app_user WHERE tenant_id = ?", DefaultTenantID,
		); err != nil {
			handleDBError(w, err)
			return
		}
		if len(counts) == 1 && counts[0] > 0 {
			writeError(w, http.StatusConflict, "an app_user already exists")
			return
		}

		passwordHash, err := hashPassword(req.Password)
		if err != nil {
			logging.Errorf("api: hash password: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		user := domain.User{
			ID:           uuid.NewString(),
			TenantID:     DefaultTenantID,
			Email:        req.Email,
			PasswordHash: passwordHash,
			CreatedAt:    time.Now().UnixMilli(),
		}
		err = db.Exec(r.Context(),
			`INSERT INTO app_user (id, tenant_id, email, password_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
			user.ID, user.TenantID, user.Email, user.PasswordHash, user.CreatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, user)
	}
}

// SeedAdminUser creates the tenant's app_user with a fixed email/password
// instead of the normal POST /app-users bootstrap flow, and only if the
// tenant has no app_user yet (same one-user-per-tenant invariant
// bootstrapAppUser enforces — this never overwrites an existing user).
// cmd/server only calls this when PLAYHEADTV_SEED_ADMIN_EMAIL/_PASSWORD are
// both set in the environment, which is true for the shared local
// docker-compose instance (baked into docker-compose.yml) and nothing else
// — a real cloud-sync tenant's D1 database never sees these vars, so it
// never seeds a known credential. minPasswordLength is deliberately not
// enforced here: that check exists to stop a human from typing a weak
// password into POST /app-users, not to block a maintainer's own
// intentionally-weak, throwaway dev seed.
func SeedAdminUser(ctx context.Context, db storage.DB, email, password string) error {
	var counts []int64
	if err := db.Query(ctx, &counts, "SELECT COUNT(*) FROM app_user WHERE tenant_id = ?", DefaultTenantID); err != nil {
		return err
	}
	if len(counts) == 1 && counts[0] > 0 {
		return nil
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return db.Exec(ctx,
		`INSERT INTO app_user (id, tenant_id, email, password_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		uuid.NewString(), DefaultTenantID, email, passwordHash, time.Now().UnixMilli(),
	)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string      `json:"token"`
	ExpiresAt int64       `json:"expires_at"`
	User      domain.User `json:"user"`
}

// login never lets a caller distinguish "no such email" from "wrong
// password" — both fail through the exact same code path and message.
func login(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		var user domain.User
		err := db.Query(r.Context(), &user,
			"SELECT id, tenant_id, email, password_hash, created_at FROM app_user WHERE tenant_id = ? AND email = ?",
			DefaultTenantID, req.Email,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				burnPasswordCheck(req.Password)
				writeError(w, http.StatusUnauthorized, "invalid email or password")
				return
			}
			handleDBError(w, err)
			return
		}
		if !checkPassword(user.PasswordHash, req.Password) {
			writeError(w, http.StatusUnauthorized, "invalid email or password")
			return
		}

		token, err := generateToken()
		if err != nil {
			logging.Errorf("api: generate session token: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		now := time.Now()
		expiresAt := now.Add(sessionTTL).UnixMilli()
		err = db.Exec(r.Context(),
			`INSERT INTO session (id, tenant_id, user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), DefaultTenantID, user.ID, hashToken(token), now.UnixMilli(), expiresAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, loginResponse{Token: token, ExpiresAt: expiresAt, User: user})
	}
}

// logout deletes only the session backing the token presented on this
// request, not every session belonging to the user.
func logout(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenHash, ok := r.Context().Value(ctxTokenHash).(string)
		if !ok || tokenHash == "" {
			// RequireAuth only lets a session-authenticated request reach
			// here, so this would mean a device token was used for logout.
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM session WHERE token_hash = ?", tokenHash); err != nil {
			handleDBError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func getMe(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := r.Context().Value(ctxUserID).(string)
		if !ok || userID == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var user domain.User
		err := db.Query(r.Context(), &user,
			"SELECT id, tenant_id, email, password_hash, created_at FROM app_user WHERE tenant_id = ? AND id = ?",
			DefaultTenantID, userID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, user)
	}
}

const deviceColumns = "id, tenant_id, name, platform, api_token_hash, last_seen_at, created_at"

type deviceRequest struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

type createDeviceResponse struct {
	Device domain.Device `json:"device"`
	Token  string        `json:"token"`
}

func createDevice(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req deviceRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		switch domain.Platform(req.Platform) {
		case domain.PlatformLinux, domain.PlatformWindows, domain.PlatformAndroid,
			domain.PlatformAndroidTV, domain.PlatformIOS, domain.PlatformTVOS:
		default:
			writeError(w, http.StatusBadRequest, "platform must be one of linux, windows, android, android_tv, ios, tvos")
			return
		}

		device, token, err := newDevice(req.Name, domain.Platform(req.Platform))
		if err != nil {
			logging.Errorf("api: generate device token: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := insertDevice(r.Context(), db, device); err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, createDeviceResponse{Device: device, Token: token})
	}
}

func listDevices(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var devices []domain.Device
		err := db.Query(r.Context(), &devices,
			"SELECT "+deviceColumns+" FROM device WHERE tenant_id = ? ORDER BY created_at",
			DefaultTenantID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, devices)
	}
}

func deleteDevice(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var device domain.Device
		err := db.Query(r.Context(), &device,
			"SELECT "+deviceColumns+" FROM device WHERE tenant_id = ? AND id = ?",
			DefaultTenantID, id,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM device WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
			handleDBError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- RequireAuth middleware ---

// deviceOrSessionGET are exact GET paths a device token or an app_user
// session may both read.
var deviceOrSessionGET = map[string]bool{
	"/epg":      true,
	"/epg/meta": true,
	"/channels": true,
	// Which plugins exist, and whether this backend has one installed and on. A
	// playback client needs it to know that a channel it was given can actually be
	// played here: it decides what to show from its guide, and the guide names the
	// plugin per entry. Read-only and no secret; installing and removing stay
	// session-only.
	"/plugins": true,
	// The plugin sources and the settings their plugins declare: a playback
	// client's Settings screen shows them and can change them through
	// PUT /plugin-sources/{id}/settings, which exposes exactly the members each
	// plugin declared (see plugin_settings.go). Every other field of a source
	// stays on the session-only authoring routes.
	"/plugin-sources": true,
}

// isPluginSettingsPath matches PUT /plugin-sources/{id}/settings.
func isPluginSettingsPath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/plugin-sources/")
	if !ok {
		return false
	}
	id, tail, ok := strings.Cut(rest, "/")
	return ok && id != "" && tail == "settings"
}

// isOpenPath reports whether the request needs no authentication at all.
func isOpenPath(r *http.Request) bool {
	if r.URL.Path == "/healthz" || r.URL.Path == "/version" {
		return true
	}
	if r.Method == http.MethodPost && r.URL.Path == "/app-users" {
		return true
	}
	if r.Method == http.MethodPost && r.URL.Path == "/sessions" {
		return true
	}
	// The token in the body is the credential (see password_reset.go).
	if r.Method == http.MethodPost && r.URL.Path == "/password-resets/consume" {
		return true
	}
	// Device pairing: an unconfigured client has no credentials yet. POST
	// /pair mints the PIN/poll secret, /pair/poll is credentialed by the
	// poll secret, /pin/approve by the human's app_user e-mail/password in
	// the body, and GET /pin serves the human-facing approval form — all in
	// pairing.go and pin_page.go.
	if r.Method == http.MethodPost && (r.URL.Path == "/pair" || r.URL.Path == "/pair/poll" || r.URL.Path == "/pin/approve") {
		return true
	}
	if r.Method == http.MethodGet && r.URL.Path == "/pin" {
		return true
	}
	// The admin SPA's static shell (HTML/JS/CSS) has to be public — the
	// browser can't even fetch the login screen's bundle otherwise. Real
	// authentication still happens on every API call the SPA makes
	// (/sessions, /channels, ...), which keep going through the checks below
	// as before; only this static shell is exempt.
	// "/admin" itself is open too so ServeMux can redirect it to "/admin/"
	// instead of RequireAuth answering 401 first.
	if r.Method == http.MethodGet && (r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/")) {
		return true
	}
	return false
}

// isDeviceOrSessionPath reports whether either an app_user session or a
// device token may authenticate this request — always a GET, and always
// data a "dumb", not-logged-in playback client needs to resolve/play.
func isDeviceOrSessionPath(r *http.Request) bool {
	if r.Method == http.MethodPut && isPluginSettingsPath(r.URL.Path) {
		// The one write a paired playback device may make: the settings a plugin
		// declares for its own sources, and nothing else about the source.
		return true
	}
	if r.Method != http.MethodGet {
		return false
	}
	if deviceOrSessionGET[r.URL.Path] {
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/channels/") {
		return true
	}
	// A watermark image: the playback client downloads it with its device
	// token (the id is unguessable and changes on every upload).
	if strings.HasPrefix(r.URL.Path, "/watermarks/") {
		return true
	}
	// A channel logo, for the same reason: the client draws it in the guide and
	// the OSD, and the id is unguessable and changes on every upload.
	if strings.HasPrefix(r.URL.Path, "/channel-logos/") {
		return true
	}
	// GET /catalog-items/{id} (lookup) is device-or-session; GET
	// /catalog-items (search/browse, no trailing segment) is session-only,
	// handled by falling through to the default case below.
	if strings.HasPrefix(r.URL.Path, "/catalog-items/") {
		return true
	}
	return false
}

// RequireAuth wraps next so that every request is authenticated as either an
// app_user session or a device, according to the policy above, before
// reaching the wrapped handler. It's applied once, around the whole mux
// (main.go and the test harness), not per-route — none of the registerXxx
// handlers know auth exists.
func RequireAuth(db storage.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isOpenPath(r) {
			next.ServeHTTP(w, r)
			return
		}

		token, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		tokenHash := hashToken(token)

		if isDeviceOrSessionPath(r) {
			if ctx, ok := authenticateSession(r.Context(), db, tokenHash); ok {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if ctx, ok := authenticateDevice(r.Context(), db, tokenHash); ok {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Everything else (/media-sources*, /blocks*, /filler-pools*,
		// /filler-rules*, /schedule-entries*, GET /catalog-items exact,
		// /devices*, GET /me, DELETE /sessions, and any write method not
		// covered above) requires a session — matched by exclusion rather
		// than an explicit prefix list, since it's the default case.
		if ctx, ok := authenticateSession(r.Context(), db, tokenHash); ok {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized")
	})
}

// bearerToken extracts the raw token from "Authorization: Bearer <token>".
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(h, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}

type sessionRow struct {
	UserID    string
	ExpiresAt int64
}

// authenticateSession looks up tokenHash in session, checking expiry, and
// returns a context carrying the matched user_id and the token hash (needed
// by DELETE /sessions to know which row to remove).
func authenticateSession(ctx context.Context, db storage.DB, tokenHash string) (context.Context, bool) {
	var s sessionRow
	err := db.Query(ctx, &s,
		"SELECT user_id, expires_at FROM session WHERE token_hash = ?",
		tokenHash,
	)
	if err != nil {
		return ctx, false
	}
	if s.ExpiresAt <= time.Now().UnixMilli() {
		return ctx, false
	}
	ctx = context.WithValue(ctx, ctxUserID, s.UserID)
	ctx = context.WithValue(ctx, ctxTokenHash, tokenHash)
	return ctx, true
}

// authenticateDevice looks up tokenHash in device and, on a match, bumps
// last_seen_at best-effort — a failure to record that doesn't block the
// request.
func authenticateDevice(ctx context.Context, db storage.DB, tokenHash string) (context.Context, bool) {
	var d domain.Device
	err := db.Query(ctx, &d,
		"SELECT "+deviceColumns+" FROM device WHERE api_token_hash = ?",
		tokenHash,
	)
	if err != nil {
		return ctx, false
	}
	if err := db.Exec(ctx, "UPDATE device SET last_seen_at = ? WHERE id = ?", time.Now().UnixMilli(), d.ID); err != nil {
		logging.Errorf("api: update device last_seen_at: %v", err)
	}
	ctx = context.WithValue(ctx, ctxDeviceID, d.ID)
	return ctx, true
}
