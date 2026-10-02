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
	// PlaybackOrder is how a dynamic block walks its matches: OrderTitle (the
	// default, and what every block did before the setting existed: all
	// matches by title, looped) or OrderInterleaved. Meaningless for a manual
	// block, whose items carry their own orders.
	PlaybackOrder string `json:"playback_order"`
	// InterleaveSpec is the stored JSON of Interleave, set iff PlaybackOrder is
	// OrderInterleaved; PlaybackSeed makes its shuffle/random series order
	// reproducible. Both internal: clients read Interleave.
	InterleaveSpec *string `json:"-"`
	PlaybackSeed   int64   `json:"-"`
	// Interleave is the decoded InterleaveSpec, filled by FillInterleave; nil
	// unless the block is interleaved.
	Interleave *Interleave `json:"interleave"`
	CreatedAt  int64       `json:"created_at"`
	UpdatedAt  int64       `json:"updated_at"`
}

// FillInterleave decodes InterleaveSpec into Interleave and normalizes an
// empty PlaybackOrder to OrderTitle (a row stored before the setting existed).
func (b *Block) FillInterleave() {
	if b.PlaybackOrder == "" {
		b.PlaybackOrder = OrderTitle
	}
	b.Interleave = nil
	if b.InterleaveSpec == nil {
		return
	}
	var il Interleave
	if err := json.Unmarshal([]byte(*b.InterleaveSpec), &il); err == nil {
		il = il.Normalized()
		b.Interleave = &il
	}
}

// BlockItem types. A block item is one catalog item, a series, or a criteria
// (a condition tree expanded into some of its matches).
const (
	BlockItemCatalog  = "catalog"
	BlockItemSeries   = "series"
	BlockItemCriteria = "criteria"
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
	// OrderTitle plays by title, accent- and case-insensitive. Criteria items
	// only; a series has season and episode order instead.
	OrderTitle = "title"
	// OrderInterleaved is round-robin across series: per round, every series
	// (an item without one counts as a one-item series) airs Interleave.PerRound
	// episodes in turn, and rounds repeat. Criteria items and dynamic blocks only.
	OrderInterleaved = "interleaved"
)

// What an interleaved walk does once every series has been fully played.
const (
	// OnExhaustionRestart: every series restarts from its first episode and the
	// rounds begin again (a series that ran out earlier waits until then).
	OnExhaustionRestart = "restart"
	// OnExhaustionDrop: an exhausted series leaves the rounds and the others
	// continue until all are done; then the block's own repeat behavior applies
	// (a dynamic block loops, a manual block ends the item's visit there).
	OnExhaustionDrop = "drop"
)

// Interleave configures OrderInterleaved. PerRound is the number of episodes
// a series airs per turn (at least 1). OnExhaustion is OnExhaustionRestart (the
// default) or OnExhaustionDrop. SeriesOrder is how the series are sequenced in
// a round: title (default), release_date (earliest premiere of the series),
// random (fresh every round) or shuffle (fixed within a playback cycle).
type Interleave struct {
	PerRound     int    `json:"per_round"`
	OnExhaustion string `json:"on_exhaustion"`
	SeriesOrder  string `json:"series_order"`
}

// InterleaveInput is Interleave as a client sends it: every field optional,
// per_round a pointer so an absent value differs from an invalid 0.
type InterleaveInput struct {
	PerRound     *int   `json:"per_round"`
	OnExhaustion string `json:"on_exhaustion"`
	SeriesOrder  string `json:"series_order"`
}

// Normalized fills the defaults of unset fields: one episode per round,
// restart on exhaustion, series by title.
func (i Interleave) Normalized() Interleave {
	if i.PerRound < 1 {
		i.PerRound = 1
	}
	if i.OnExhaustion == "" {
		i.OnExhaustion = OnExhaustionRestart
	}
	if i.SeriesOrder == "" {
		i.SeriesOrder = OrderTitle
	}
	return i
}

// ValidSeriesOrder reports whether o can sequence the series of a round.
func ValidSeriesOrder(o string) bool {
	switch o {
	case OrderTitle, OrderReleaseDate, OrderRandom, OrderShuffle:
		return true
	}
	return false
}

// ValidCriteriaOrder reports whether o is a playback order a criteria item
// accepts: by title, by release date, random, shuffle without repeat, or
// interleaved by series.
func ValidCriteriaOrder(o string) bool {
	switch o {
	case OrderTitle, OrderReleaseDate, OrderRandom, OrderShuffle, OrderInterleaved:
		return true
	}
	return false
}

// ValidPlaybackOrder reports whether o is a known series playback order.
func ValidPlaybackOrder(o string) bool {
	switch o {
	case OrderOriginal, OrderRandom, OrderShuffle, OrderReleaseDate:
		return true
	}
	return false
}

// How much of a series one visit of the block rotation airs.
const (
	// AmountCount airs Count episodes, then the rotation moves on.
	AmountCount = "count"
	// AmountSlotEnd airs episodes until the end of the schedule slot (the next
	// schedule occurrence or the end of the window), so items after the series
	// never air. It is what every series item did before amounts existed.
	AmountSlotEnd = "slot_end"
	// AmountSeriesEnd airs the rest of the playback cycle in progress, then
	// the rotation moves on; the next visit starts a new cycle.
	AmountSeriesEnd = "series_end"
)

// SeriesAmount is how much a series item airs per visit. Count is only set
// for AmountCount and is then at least 1.
type SeriesAmount struct {
	Mode  string `json:"mode"`
	Count int    `json:"count,omitempty"`
}

// SeriesAmountInput is the amount as a client sends it. Count is a pointer so
// an absent count differs from an invalid 0.
type SeriesAmountInput struct {
	Mode  string `json:"mode"`
	Count *int   `json:"count"`
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
	// Amount is how much one visit airs. A spec stored before amounts existed
	// has none and means AmountSlotEnd (see EffectiveAmount).
	Amount *SeriesAmount `json:"amount,omitempty"`
	// Interleave configures OrderInterleaved. Only the materializer's walk of
	// a criteria item or dynamic block sets it; never stored on a series item.
	Interleave *Interleave `json:"interleave,omitempty"`
}

// EffectiveAmount is the spec's amount, with the legacy "no amount stored"
// mapped explicitly to AmountSlotEnd.
func (s SeriesSpec) EffectiveAmount() SeriesAmount {
	if s.Amount == nil || s.Amount.Mode == "" {
		return SeriesAmount{Mode: AmountSlotEnd}
	}
	a := *s.Amount
	if a.Mode == AmountCount && a.Count < 1 {
		a.Count = 1
	}
	return a
}

// SeriesItemView is the client-facing shape of a series block item: the
// identity and order, plus what the series currently expands to.
type SeriesItemView struct {
	MediaSourceID    string          `json:"media_source_id"`
	SeriesExternalID string          `json:"series_external_id,omitempty"`
	SeriesTitle      string          `json:"series_title,omitempty"`
	PlaybackOrder    string          `json:"playback_order"`
	Amount           SeriesAmount    `json:"amount"`
	Slice            json.RawMessage `json:"slice"`
	EpisodeCount     int             `json:"episode_count"`
	TotalDurationMs  int64           `json:"total_duration_ms"`
}

// Amounts of a criteria item per visit.
const (
	// CriteriaAmountCount airs Count items of the query, then the rotation
	// moves on. Count is required (at least 1).
	CriteriaAmountCount = "count"
	// CriteriaAmountAll airs every match once (the rest of the playback cycle
	// in progress), then the rotation moves on. It must be chosen explicitly.
	CriteriaAmountAll = "all"
)

// CriteriaAmount is how much a criteria item airs per visit. Count is only
// set for CriteriaAmountCount.
type CriteriaAmount struct {
	Mode  string `json:"mode"`
	Count int    `json:"count,omitempty"`
}

// CriteriaSpec is the stored configuration of a criteria block item
// (block_item.spec): the condition tree (as stored by catalogsync.Query), how
// its matches are ordered, and how much one visit airs. Seed makes the
// shuffle and random orders reproducible and is never shown to clients.
type CriteriaSpec struct {
	Query         json.RawMessage `json:"query"`
	PlaybackOrder string          `json:"playback_order"`
	Seed          int64           `json:"seed"`
	Amount        CriteriaAmount  `json:"amount"`
	// Interleave is set iff PlaybackOrder is OrderInterleaved.
	Interleave *Interleave `json:"interleave,omitempty"`
}

// CriteriaItemView is the client-facing shape of a criteria block item: the
// query and order, plus what the query currently matches (items with a
// positive duration, the ones that can be scheduled).
type CriteriaItemView struct {
	// Query is the condition tree as a decoded JSON value (not a RawMessage,
	// so the MCP output schema accepts it).
	Query           any            `json:"query"`
	PlaybackOrder   string         `json:"playback_order"`
	Amount          CriteriaAmount `json:"amount"`
	Interleave      *Interleave    `json:"interleave,omitempty"`
	MatchCount      int            `json:"match_count"`
	TotalDurationMs int64          `json:"total_duration_ms"`
}

// BlockItem is one entry of a block. ItemType selects what it is:
// BlockItemCatalog (CatalogItemID set, TrimDurationMs optional),
// BlockItemSeries (Spec holds the SeriesSpec JSON, Series is its view, filled
// on read) or BlockItemCriteria (Spec holds the CriteriaSpec JSON, Criteria is
// its view). An empty ItemType means a catalog item.
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
	Spec     *string           `json:"-"`
	Series   *SeriesItemView   `json:"series,omitempty"`
	Criteria *CriteriaItemView `json:"criteria,omitempty"`
}

// IsSeries reports whether the item is a series item.
func (b BlockItem) IsSeries() bool { return b.ItemType == BlockItemSeries }

// IsCriteria reports whether the item is a criteria item.
func (b BlockItem) IsCriteria() bool { return b.ItemType == BlockItemCriteria }

// IsWalked reports whether the item expands into many catalog items that the
// materializer walks with a persistent cursor (series and criteria items),
// as opposed to one fixed catalog item.
func (b BlockItem) IsWalked() bool { return b.IsSeries() || b.IsCriteria() }
