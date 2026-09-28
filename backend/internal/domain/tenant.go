package domain

// Tier distinguishes a self-hosted deployment (single implicit tenant, free)
// from a cloud-sync deployment (multi-tenant, paid). Both run the same code
// and schema — see docs/ARCHITECTURE.md.
type Tier string

const (
	TierSelfHosted Tier = "self_hosted"
	TierCloudSync  Tier = "cloud_sync"
)

type Tenant struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Tier      Tier   `json:"tier"`
	CreatedAt int64  `json:"created_at"` // unix millis
}

type User struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenant_id"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	CreatedAt    int64  `json:"created_at"`
}

type Platform string

const (
	PlatformLinux     Platform = "linux"
	PlatformWindows   Platform = "windows"
	PlatformAndroid   Platform = "android"
	PlatformAndroidTV Platform = "android_tv"
	PlatformIOS       Platform = "ios"
	PlatformTVOS      Platform = "tvos"
)

// Device is a playback client. It doubles as the identity behind push
// catalog syncs when a MediaSource is unreachable from the backend directly.
type Device struct {
	ID           string   `json:"id"`
	TenantID     string   `json:"tenant_id"`
	Name         string   `json:"name"`
	Platform     Platform `json:"platform"`
	APITokenHash string   `json:"-"`
	LastSeenAt   *int64   `json:"last_seen_at"`
	CreatedAt    int64    `json:"created_at"`
}
