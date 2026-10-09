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
// A block is manual (items added/removed one at a time via POST/DELETE
// /blocks/{id}/items) or dynamic: it references a Collection, and its
// block_item rows are then fully owned by catalogsync.ResolveDynamicBlocks and
// recomputed after every catalog sync.
type Block struct {
	ID          string  `json:"id"`
	TenantID    string  `json:"tenant_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	// CollectionID is the collection a dynamic block plays; CollectionName is
	// read along for display.
	CollectionID   *string `json:"collection_id"`
	CollectionName *string `json:"collection_name"`
	// Dynamic is true for a block that references a collection and also for a
	// legacy row still held for review (collection_id NULL, deprecated
	// Criteria still in use, see criteria_migration_review).
	Dynamic bool `json:"dynamic"`
	// Criteria is the DEPRECATED pre-collection query column: a snapshot kept
	// as the way back from the collection migration, never sent to clients and
	// only read while CollectionID is NULL (a legacy row held for review).
	Criteria *string `json:"-"`
	// PlaybackOrder is how a dynamic block walks its matches: OrderTitle (the
	// default, and what every block did before the setting existed: all
	// matches by title, looped) or OrderInterleaved. Meaningless for a manual
	// block, whose items carry their own orders.
	PlaybackOrder string `json:"playback_order"`
	// ListMode is how a manual block plays its item list: ListModeRepeat (the
	// default: when the list ends it starts again from the first item, until
	// the next timed item or, for a continuous item, indefinitely) or
	// ListModeOnce (the list airs once). Meaningless for a dynamic block,
	// which always loops by its own rules; FillInterleave empties it there so
	// it is not served.
	ListMode string `json:"list_mode,omitempty"`
	// IncludeSpecials lets the episodes of season 0 of a series into what the
	// block selects (dynamic matches, criteria items, series items). Off by
	// default; see series.DropSpecials for what it does and does not touch. A series
	// slice that explicitly names season 0 wins over the default.
	IncludeSpecials bool `json:"include_specials"`
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

// Playback modes of a manual block's item list (Block.ListMode).
const (
	ListModeRepeat = "repeat"
	ListModeOnce   = "once"
)

// FillInterleave decodes InterleaveSpec into Interleave and normalizes an
// empty PlaybackOrder to OrderTitle (a row stored before the setting existed).
// It also drops ListMode from a dynamic block, where it has no meaning.
func (b *Block) FillInterleave() {
	if b.Dynamic {
		b.ListMode = ""
	} else if b.ListMode == "" {
		b.ListMode = ListModeRepeat
	}
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

// What an interleaved walk does when a series runs out of episodes.
const (
	// OnExhaustionRestart: the series restarts from its own first episode, on
	// its own, and keeps taking its turn in the following rounds while longer
	// series continue (short series loop repeatedly; the rotation never
	// degenerates to the longest series alone).
	OnExhaustionRestart = "restart"
	// OnExhaustionDrop: a series that runs out leaves the rounds for good and
	// the others continue; once every series is done the block's own end-of-list
	// behavior applies (a dynamic block loops, starting everything over; a count
	// visit of a criteria item in a manual block ends there).
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
// random orders reproducible; it is never shown to clients. Slice optionally
// narrows the series to a part of its episodes (see SeriesSlice); SliceSeed
// makes the slice's random sample reproducible and is never shown either.
type SeriesSpec struct {
	MediaSourceID    string       `json:"media_source_id"`
	SeriesExternalID string       `json:"series_external_id,omitempty"`
	SeriesTitle      string       `json:"series_title,omitempty"`
	PlaybackOrder    string       `json:"playback_order"`
	Seed             int64        `json:"seed"`
	Slice            *SeriesSlice `json:"slice,omitempty"`
	SliceSeed        int64        `json:"slice_seed,omitempty"`
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

// SeriesSlice narrows a series item to a part of the series' episodes. Every
// field is optional; a slice with none set is no slice. They apply in this
// order, each to what the previous ones left: the season filter (Seasons
// and/or SeasonFrom..SeasonTo, a union), ExcludeSpecials, the episode number
// range (EpisodeFrom..EpisodeTo), SkipFirst/SkipLast, First or Last, Sample.
// Season and episode ranges are inclusive and refer to real numbers (gaps in
// the library's numbering simply do not appear); an episode file covering
// several episodes (episode_number_end) is in the episode range when any of
// its numbers is. Skip, First, Last and Sample count files by position in the
// series' original order (season, episode), so a multi-episode file counts
// once. Episodes without a season number are left out of the season filter,
// and those without an episode number out of the episode range.
type SeriesSlice struct {
	Seasons    []int `json:"seasons,omitempty"`
	SeasonFrom *int  `json:"season_from,omitempty"`
	SeasonTo   *int  `json:"season_to,omitempty"`
	// ExcludeSpecials drops season 0. Superseded by Block.IncludeSpecials
	// (specials are out unless the block lets them in); still accepted, and
	// still honored when the block includes specials.
	ExcludeSpecials bool `json:"exclude_specials,omitempty"`
	EpisodeFrom     *int `json:"episode_from,omitempty"`
	EpisodeTo       *int `json:"episode_to,omitempty"`
	SkipFirst       *int `json:"skip_first,omitempty"`
	SkipLast        *int `json:"skip_last,omitempty"`
	First           *int `json:"first,omitempty"`
	Last            *int `json:"last,omitempty"`
	// Sample keeps that many randomly chosen episodes, in original order.
	Sample *int `json:"sample,omitempty"`
}

// SeriesItemView is the client-facing shape of a series block item: the
// identity and order, plus what the series currently expands to.
type SeriesItemView struct {
	MediaSourceID    string       `json:"media_source_id"`
	SeriesExternalID string       `json:"series_external_id,omitempty"`
	SeriesTitle      string       `json:"series_title,omitempty"`
	PlaybackOrder    string       `json:"playback_order"`
	Amount           SeriesAmount `json:"amount"`
	Slice            *SeriesSlice `json:"slice"`
	EpisodeCount     int          `json:"episode_count"`
	TotalDurationMs  int64        `json:"total_duration_ms"`
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
	// CollectionID is the collection whose query the item plays: the source of
	// truth once set. Query is the DEPRECATED embedded tree of the first
	// release of criteria items: a snapshot kept as the way back from the
	// collection migration, read only while CollectionID is empty (an item held
	// for review). New items never store it.
	CollectionID  string          `json:"collection_id,omitempty"`
	Query         json.RawMessage `json:"query,omitempty"`
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
	// CollectionID/CollectionName identify the collection the item plays (nil
	// only for an item held for review that still uses its embedded query).
	CollectionID   *string `json:"collection_id"`
	CollectionName *string `json:"collection_name"`
	// Query is the effective condition tree as a decoded JSON value (not a
	// RawMessage, so the MCP output schema accepts it): the collection's.
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
