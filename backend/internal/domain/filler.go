package domain

// FillerPool is a named collection of filler content (commercials, bumpers,
// station IDs) that one or more FillerRules draw from. A pool is manual
// (items added/removed one at a time via POST/DELETE /filler-pools/{id}/items)
// or dynamic: it references a Collection and its filler_pool_item rows are
// fully owned by catalogsync.ResolveDynamicPools, recomputed after every
// catalog sync.
type FillerPool struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	// CollectionID/CollectionName/Dynamic: see Block.
	CollectionID   *string `json:"collection_id"`
	CollectionName *string `json:"collection_name"`
	Dynamic        bool    `json:"dynamic"`
	// Criteria is the DEPRECATED pre-collection query column (see Block.Criteria).
	Criteria  *string `json:"-"`
	CreatedAt int64   `json:"created_at"`
}

type FillerPoolItem struct {
	ID            string `json:"id"`
	FillerPoolID  string `json:"filler_pool_id"`
	CatalogItemID string `json:"catalog_item_id"`
	Weight        int    `json:"weight"` // used by SelectionWeighted
}

// FillerScope selects whether a FillerRule applies to a whole Channel (every
// Block airing on it) or to a single Block (only when that Block airs).
type FillerScope string

const (
	ScopeChannel FillerScope = "channel"
	ScopeBlock   FillerScope = "block"
)

// FillerKind follows ErsatzTV's naming: content played before a block
// (PreRoll), between items within it (MidRoll), after it (PostRoll), padding
// to make a block's timeline land exactly on the next schedule_entry (Tail),
// or what airs when nothing is scheduled at all (Fallback).
type FillerKind string

const (
	FillerPreRoll  FillerKind = "preroll"
	FillerMidRoll  FillerKind = "midroll"
	FillerPostRoll FillerKind = "postroll"
	FillerTail     FillerKind = "tail"
	FillerFallback FillerKind = "fallback"
)

type SelectionStrategy string

const (
	SelectionSequential SelectionStrategy = "sequential"
	SelectionRandom     SelectionStrategy = "random"
	SelectionWeighted   SelectionStrategy = "weighted"
)

type FillerRule struct {
	ID           string            `json:"id"`
	TenantID     string            `json:"tenant_id"`
	Scope        FillerScope       `json:"scope_type"`
	ScopeID      string            `json:"scope_id"` // Channel.ID or Block.ID, per Scope
	Kind         FillerKind        `json:"kind"`
	FillerPoolID string            `json:"filler_pool_id"`
	Selection    SelectionStrategy `json:"selection_strategy"`
	// Position orders multiple rules sharing the same Scope+Kind (e.g. two
	// midroll rules pulling from different pools).
	Position  int   `json:"position"`
	CreatedAt int64 `json:"created_at"`
}
