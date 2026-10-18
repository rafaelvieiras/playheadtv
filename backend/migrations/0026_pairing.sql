-- Temporary-PIN device pairing ("quick connect"): an unconfigured client asks
-- for a pairing (POST /pair), shows the PIN to a human, and polls with its
-- poll secret until the human approves the PIN in a browser (POST
-- /pin/approve). Only hashes are stored: the raw PIN and the raw poll secret
-- exist only in the response to POST /pair (and the PIN on the screen).
-- The raw device token is never stored here either — /pair/poll creates the
-- device row and deletes this pairing row in one transaction, delivering the
-- token exactly once. expires_at bounds how long a row can be approved, and a
-- row is deleted on use, on expiry, and after too many failed approvals.
CREATE TABLE pairing (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    pin_hash          TEXT NOT NULL,
    poll_secret_hash  TEXT NOT NULL,
    name              TEXT NOT NULL,
    platform          TEXT NOT NULL, -- same values as device.platform
    status            TEXT NOT NULL CHECK (status IN ('pending', 'approved')),
    approved_by       TEXT,          -- app_user.id once approved, null while pending
    expires_at        INTEGER NOT NULL,
    failed_attempts   INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_pairing_pin ON pairing(tenant_id, pin_hash);
CREATE UNIQUE INDEX idx_pairing_poll_secret ON pairing(poll_secret_hash);
CREATE INDEX idx_pairing_tenant ON pairing(tenant_id);
