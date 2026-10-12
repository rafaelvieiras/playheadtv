// Password recovery: an operator with access to the server runs
// `server reset-password` (cmd/server), which calls CreatePasswordReset and
// prints a single-use token; the person then chooses the new password on the
// web UI page, which posts it to POST /password-resets/consume. No e-mail or
// other infrastructure is involved, and nothing here lets a caller without the
// token learn anything about accounts or tokens.
package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Lifetime of a reset token: the default for `server reset-password`, and the
// bounds it accepts for --ttl.
const (
	DefaultResetTTL = 15 * time.Minute
	MinResetTTL     = time.Minute
	MaxResetTTL     = time.Hour
)

// Abuse limits of POST /password-resets/consume. A wrong token burns the
// pending token after maxResetFailures attempts (the operator runs the command
// again); independently, each client address and each presented token value is
// limited to a number of attempts per window.
const (
	maxResetFailures   = 5
	resetWindow        = 15 * time.Minute
	resetPerAddrLimit  = 10
	resetPerTokenLimit = 5
)

// PasswordReset is what CreatePasswordReset hands back to the command: the raw
// token exists only here, the database holds its hash.
type PasswordReset struct {
	Email     string
	Token     string
	ExpiresAt time.Time
}

// CreatePasswordReset stores a new single-use reset token for a user of the
// default tenant, replacing any pending one for that user. email may be empty
// when exactly one user exists; with several it is required. An unknown email
// or a ttl outside [MinResetTTL, MaxResetTTL] is an error (the caller is the
// operator at a terminal, not an anonymous client, so errors can be specific).
func CreatePasswordReset(ctx context.Context, db storage.DB, email string, ttl time.Duration) (PasswordReset, error) {
	if ttl < MinResetTTL || ttl > MaxResetTTL {
		return PasswordReset{}, fmt.Errorf("ttl must be between %s and %s", MinResetTTL, MaxResetTTL)
	}
	var users []domain.User
	if err := db.Query(ctx, &users,
		"SELECT id, tenant_id, email, password_hash, created_at FROM app_user WHERE tenant_id = ? ORDER BY created_at",
		DefaultTenantID,
	); err != nil {
		return PasswordReset{}, err
	}
	var user *domain.User
	switch {
	case len(users) == 0:
		return PasswordReset{}, errors.New("no user exists yet; create one with POST /app-users")
	case email == "" && len(users) == 1:
		user = &users[0]
	case email == "":
		return PasswordReset{}, errors.New("several users exist; pass --email")
	default:
		for i := range users {
			if users[i].Email == email {
				user = &users[i]
			}
		}
		if user == nil {
			return PasswordReset{}, errors.New("no user with that email")
		}
	}

	token, err := generateToken()
	if err != nil {
		return PasswordReset{}, err
	}
	now := time.Now()
	expires := now.Add(ttl)
	if err := db.Exec(ctx,
		`INSERT INTO password_reset (user_id, tenant_id, token_hash, created_at, expires_at, failed_attempts)
		 VALUES (?, ?, ?, ?, ?, 0)
		 ON CONFLICT(user_id) DO UPDATE SET token_hash = excluded.token_hash,
		   created_at = excluded.created_at, expires_at = excluded.expires_at, failed_attempts = 0`,
		user.ID, DefaultTenantID, hashToken(token), now.UnixMilli(), expires.UnixMilli(),
	); err != nil {
		return PasswordReset{}, err
	}
	return PasswordReset{Email: user.Email, Token: token, ExpiresAt: expires}, nil
}

// attemptLimiter is a fixed-window counter per key, in memory (a restart
// forgets it, which only matters to an attacker who can restart the server).
type attemptLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	hits   map[string]*windowCount
}

type windowCount struct {
	start time.Time
	n     int
}

func newAttemptLimiter(limit int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{limit: limit, window: window, now: time.Now, hits: map[string]*windowCount{}}
}

// allow records an attempt for key and reports whether it is within the limit.
func (l *attemptLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.hits) > 4096 { // bound memory under a flood of distinct keys
		for k, c := range l.hits {
			if now.Sub(c.start) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	c, ok := l.hits[key]
	if !ok || now.Sub(c.start) >= l.window {
		c = &windowCount{start: now}
		l.hits[key] = c
	}
	c.n++
	return c.n <= l.limit
}

// clientAddr is the peer address of the connection. Forwarding headers are not
// trusted (any client could forge them); behind a reverse proxy every request
// shares the proxy's address, and the per-token and per-reset limits still hold.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type consumeResetRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

type consumeResetResponse struct {
	Status           string `json:"status"`
	SessionsRevoked  bool   `json:"sessions_revoked"`
	DeviceTokensKept bool   `json:"device_tokens_kept"`
	Message          string `json:"message"`
}

type resetRow struct {
	UserID         string
	TokenHash      string
	ExpiresAt      int64
	FailedAttempts int64
}

// consumePasswordReset is POST /password-resets/consume (unauthenticated, like
// login). Every failure of the token itself, whether unknown, expired, used or
// burned, answers the same 400 so the response is no oracle. The token is never
// logged.
func consumePasswordReset(db storage.DB) http.HandlerFunc {
	byAddr := newAttemptLimiter(resetPerAddrLimit, resetWindow)
	byToken := newAttemptLimiter(resetPerTokenLimit, resetWindow)
	return func(w http.ResponseWriter, r *http.Request) {
		var req consumeResetRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Token == "" || req.NewPassword == "" {
			writeError(w, http.StatusBadRequest, "token and new_password are required")
			return
		}
		if len(req.NewPassword) < minPasswordLength {
			writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		presented := hashToken(req.Token)
		okAddr := byAddr.allow(clientAddr(r))
		okToken := byToken.allow(presented)
		if !okAddr || !okToken {
			w.Header().Set("Retry-After", strconv.Itoa(int(resetWindow.Seconds())))
			writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
			return
		}

		// Hash before looking at the token so a valid token costs the same
		// time as an invalid one.
		passwordHash, err := hashPassword(req.NewPassword)
		if err != nil {
			logging.Errorf("api: hash password: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		matched := false
		err = db.Transact(r.Context(), func(tx storage.DB) error {
			var rows []resetRow
			if err := tx.Query(r.Context(), &rows,
				"SELECT user_id, token_hash, expires_at, failed_attempts FROM password_reset WHERE tenant_id = ?",
				DefaultTenantID,
			); err != nil {
				return err
			}
			now := time.Now().UnixMilli()
			var match *resetRow
			for i := range rows { // no early exit: every pending row is compared
				same := subtle.ConstantTimeCompare([]byte(rows[i].TokenHash), []byte(presented)) == 1
				if same && rows[i].ExpiresAt > now {
					match = &rows[i]
				}
			}
			if match != nil {
				matched = true
				if err := tx.Exec(r.Context(),
					"UPDATE app_user SET password_hash = ? WHERE tenant_id = ? AND id = ?",
					passwordHash, DefaultTenantID, match.UserID,
				); err != nil {
					return err
				}
				if err := tx.Exec(r.Context(), "DELETE FROM session WHERE user_id = ?", match.UserID); err != nil {
					return err
				}
				return tx.Exec(r.Context(), "DELETE FROM password_reset WHERE user_id = ?", match.UserID)
			}
			// A wrong token counts against the pending ones; expired and
			// exhausted rows are dropped.
			for _, row := range rows {
				if row.ExpiresAt <= now || row.FailedAttempts+1 >= maxResetFailures {
					if err := tx.Exec(r.Context(), "DELETE FROM password_reset WHERE user_id = ?", row.UserID); err != nil {
						return err
					}
					continue
				}
				if err := tx.Exec(r.Context(),
					"UPDATE password_reset SET failed_attempts = failed_attempts + 1 WHERE user_id = ?", row.UserID,
				); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			handleDBError(w, err)
			return
		}
		if !matched {
			writeError(w, http.StatusBadRequest, "invalid or expired reset token")
			return
		}
		writeJSON(w, http.StatusOK, consumeResetResponse{
			Status:           "ok",
			SessionsRevoked:  true,
			DeviceTokensKept: true,
			Message:          "Password changed. Every signed-in session was ended; device tokens were kept, so paired playback clients keep working.",
		})
	}
}
