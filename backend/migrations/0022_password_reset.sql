-- Pending password reset of an app_user, created by `server reset-password`
-- and consumed by POST /password-resets/consume. Only the SHA-256 of the token
-- is stored. One row per user (the primary key): a new run replaces the
-- pending token. The row is deleted on use, on expiry and after too many
-- failed attempts.
CREATE TABLE password_reset (
    user_id         TEXT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    tenant_id       TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    token_hash      TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL,
    failed_attempts INTEGER NOT NULL DEFAULT 0
);
