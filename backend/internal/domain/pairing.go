package domain

// PairingStatus is where a temporary-PIN pairing is in its short life:
// "pending" until a human approves the PIN, "approved" until the polling
// client collects its device token (the row is deleted at that moment, so
// "approved" is never observed after delivery).
type PairingStatus string

const (
	PairingPending  PairingStatus = "pending"
	PairingApproved PairingStatus = "approved"
)

// Pairing is one in-flight pairing request. PinHash and PollSecretHash are
// SHA-256 hex digests of values the client received from POST /pair; the raw
// values are never persisted (and the raw poll secret is never returned
// again). The struct's field names map to the pairing table's columns by
// snake_case (see internal/storage/scan.go), so keep them in step with
// 0026_pairing.sql.
type Pairing struct {
	ID             string        `json:"id"`
	TenantID       string        `json:"tenant_id"`
	PinHash        string        `json:"-"`
	PollSecretHash string        `json:"-"`
	Name           string        `json:"name"`
	Platform       Platform      `json:"platform"`
	Status         PairingStatus `json:"status"`
	ApprovedBy     *string       `json:"approved_by"` // app_user.id once approved
	ExpiresAt      int64         `json:"expires_at"`
	FailedAttempts int64         `json:"failed_attempts"`
	CreatedAt      int64         `json:"created_at"`
}
