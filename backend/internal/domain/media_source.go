package domain

// SourceType is deliberately generic: only Jellyfin ships in v1, but the
// schema and this type already accommodate Plex and local file libraries so
// adding them later doesn't touch anything downstream (catalog_item,
// block_item, filler_pool_item all reference CatalogItem.ID, never a
// source's native ID directly).
type SourceType string

const (
	SourceJellyfin    SourceType = "jellyfin"
	SourcePlex        SourceType = "plex"         // not implemented in v1
	SourceLocalFolder SourceType = "local_folder" // not implemented in v1
)

// SyncMode is decided automatically by a reachability probe when the source
// is registered. Pull: the backend queries the source directly. Push: the
// backend can't reach it (e.g. Jellyfin exposed only over a mesh VPN), so a
// Device syncs catalog metadata to the backend instead. See
// docs/ARCHITECTURE.md "Media sources".
type SyncMode string

const (
	SyncPull SyncMode = "pull"
	SyncPush SyncMode = "push"
)

type MediaSource struct {
	ID       string     `json:"id"`
	TenantID string     `json:"tenant_id"`
	Type     SourceType `json:"type"`
	Name     string     `json:"name"`
	BaseURL  *string    `json:"base_url"` // nil for local_folder
	// CredentialCipher holds the encrypted API key/token; never logged, never
	// serialized back to a client as plaintext.
	CredentialCipher []byte   `json:"-"`
	SyncMode         SyncMode `json:"sync_mode"`
	LastProbeAt      *int64   `json:"last_probe_at"`
	LastProbeOK      *bool    `json:"last_probe_ok"`
	LastSyncedAt     *int64   `json:"last_synced_at"` // last successful sync, push or pull
	CreatedAt        int64    `json:"created_at"`
	UpdatedAt        int64    `json:"updated_at"`
}

type CatalogItemKind string

const (
	CatalogMovie   CatalogItemKind = "movie"
	CatalogEpisode CatalogItemKind = "episode"
	CatalogOther   CatalogItemKind = "other"
)

// CatalogItem is the source-agnostic cache row every scheduling table
// references by ID — never by a source's native ID.
type CatalogItem struct {
	ID               string          `json:"id"`
	TenantID         string          `json:"tenant_id"`
	MediaSourceID    string          `json:"media_source_id"`
	ExternalID       string          `json:"external_id"` // native ID in the source (e.g. Jellyfin item GUID)
	Kind             CatalogItemKind `json:"kind"`
	Title            string          `json:"title"`
	SeriesTitle      *string         `json:"series_title"` // set only when Kind == CatalogEpisode
	DurationMs       int64           `json:"duration_ms"`
	SourceModifiedAt *int64          `json:"source_modified_at"` // source's own last-modified marker; the push flow diffs by it, pull just records it (full re-list every sync)
	Genres           string          `json:"genres"`             // comma-separated, in the source's language, not normalized
	ProductionYear   *int64          `json:"production_year"`
	SyncedAt         int64           `json:"synced_at"`
}
