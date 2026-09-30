package domain

type Channel struct {
	ID          string  `json:"id"`
	TenantID    string  `json:"tenant_id"`
	Number      int     `json:"number"`
	Name        string  `json:"name"`
	GuideArtURL *string `json:"guide_art_url"`
	// Timezone is an IANA name, used to resolve ScheduleItem.StartTimeLocal
	// into absolute instants during EPG materialization.
	Timezone string `json:"timezone"`
	// ScheduleID is which Schedule (if any) drives this channel — the same
	// Schedule may also be assigned to other channels (issue #7's
	// Schedule/Playout split), each materializing its own epg_entry rows
	// independently. Nil for a channel with no schedule (e.g. one relying
	// only on a channel-scoped fallback filler rule).
	ScheduleID *string `json:"schedule_id"`
	CreatedAt  int64   `json:"created_at"`
	UpdatedAt  int64   `json:"updated_at"`
}

// Block is a reusable programming template (e.g. "Sitcom Night"): an ordered
// list of catalog items, aired on one or more channels via a Schedule's
// ScheduleItems.
// Criteria is nil for a manually managed block (today's behavior — items
// added/removed one at a time via POST/DELETE /blocks/{id}/items) or set for
// a dynamic block, whose block_item rows are instead fully owned by
// catalogsync.ResolveDynamicBlocks and recomputed after every catalog sync.
type Block struct {
	ID          string  `json:"id"`
	TenantID    string  `json:"tenant_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Criteria    *string `json:"criteria"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

type BlockItem struct {
	ID            string `json:"id"`
	BlockID       string `json:"block_id"`
	Position      int    `json:"position"`
	CatalogItemID string `json:"catalog_item_id"`
	// TrimDurationMs optionally overrides CatalogItem.DurationMs (e.g. the
	// artificial trims used in the client crossfade spike).
	TrimDurationMs *int64 `json:"trim_duration_ms"`
}
