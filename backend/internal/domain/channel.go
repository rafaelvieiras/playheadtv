package domain

import "encoding/json"

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

// BlockItem types. A block item is either one catalog item or a series.
const (
	BlockItemCatalog = "catalog"
	BlockItemSeries  = "series"
)

// Series playback orders: how a series item walks its episodes.
const (
	// OrderOriginal plays by season, then episode number (specials, season
	// 0, first), and starts over after the last episode.
	OrderOriginal = "original"
	// OrderRandom picks any episode each time; repeats are possible.
	OrderRandom = "random"
	// OrderShuffle plays every episode once, in a shuffled order, before any
	// is repeated; each cycle is a fresh shuffle.
	OrderShuffle = "shuffle"
	// OrderReleaseDate plays by premiere date (episodes without one last).
	OrderReleaseDate = "release_date"
)

// ValidPlaybackOrder reports whether o is a known series playback order.
func ValidPlaybackOrder(o string) bool {
	switch o {
	case OrderOriginal, OrderRandom, OrderShuffle, OrderReleaseDate:
		return true
	}
	return false
}

// SeriesSpec is the stored, type-specific configuration of a series block
// item (block_item.spec). A series is identified by its media source plus
// SeriesExternalID when set, else SeriesTitle. Seed makes the shuffle and
// random orders reproducible; it is never shown to clients. Slice is
// reserved for slicing a series (first N, a season range, ...) and must stay
// empty until that exists.
type SeriesSpec struct {
	MediaSourceID    string          `json:"media_source_id"`
	SeriesExternalID string          `json:"series_external_id,omitempty"`
	SeriesTitle      string          `json:"series_title,omitempty"`
	PlaybackOrder    string          `json:"playback_order"`
	Seed             int64           `json:"seed"`
	Slice            json.RawMessage `json:"slice,omitempty"`
}

// SeriesItemView is the client-facing shape of a series block item: the
// identity and order, plus what the series currently expands to.
type SeriesItemView struct {
	MediaSourceID    string          `json:"media_source_id"`
	SeriesExternalID string          `json:"series_external_id,omitempty"`
	SeriesTitle      string          `json:"series_title,omitempty"`
	PlaybackOrder    string          `json:"playback_order"`
	Slice            json.RawMessage `json:"slice"`
	EpisodeCount     int             `json:"episode_count"`
	TotalDurationMs  int64           `json:"total_duration_ms"`
}

// BlockItem is one entry of a block. ItemType selects what it is:
// BlockItemCatalog (CatalogItemID set, TrimDurationMs optional) or
// BlockItemSeries (Spec holds the SeriesSpec JSON, Series is its view, filled
// on read). An empty ItemType means a catalog item.
type BlockItem struct {
	ID       string `json:"id"`
	BlockID  string `json:"block_id"`
	Position int    `json:"position"`
	ItemType string `json:"item_type"`
	// CatalogItemID is empty for a series item.
	CatalogItemID string `json:"catalog_item_id,omitempty"`
	// TrimDurationMs optionally overrides CatalogItem.DurationMs (e.g. the
	// artificial trims used in the client crossfade spike). Catalog items only.
	TrimDurationMs *int64 `json:"trim_duration_ms"`
	// Spec is the raw type-specific JSON; internal.
	Spec   *string         `json:"-"`
	Series *SeriesItemView `json:"series,omitempty"`
}

// IsSeries reports whether the item is a series item.
func (b BlockItem) IsSeries() bool { return b.ItemType == BlockItemSeries }
