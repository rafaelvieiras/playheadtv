package domain

type RecurrenceType string

const (
	RecurrenceWeekly RecurrenceType = "weekly"
	RecurrenceOnce   RecurrenceType = "once"
	// RecurrenceContinuous has no time at all: the item fills every instant
	// not claimed by a timed item, resuming its block rotation where it left
	// off. At most one per Schedule (enforced by the API).
	RecurrenceContinuous RecurrenceType = "continuous"
)

// Schedule is a reusable, named group of recurrence rules (its ScheduleItems)
// — the "Schedule" half of ErsatzTV's Schedule/Playout split (issue #7). It
// carries no channel of its own: Channel.ScheduleID is what assigns it, and
// the same Schedule can drive more than one channel at once, each one still
// materializing (and tracking) its own epg_entry rows independently — the
// channel itself plays the "Playout" role here, there's no separate entity
// for it.
type Schedule struct {
	ID          string  `json:"id"`
	TenantID    string  `json:"tenant_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

// ScheduleItem is one recurrence rule inside a Schedule — authoring input,
// never read directly by a client. The EPG materializer expands every active
// item of the Schedule assigned to a channel (together with its
// Block/BlockItems and any FillerRules in scope) into flat EpgEntry rows
// ahead of time.
type ScheduleItem struct {
	ID             string         `json:"id"`
	TenantID       string         `json:"tenant_id"`
	ScheduleID     string         `json:"schedule_id"`
	BlockID        string         `json:"block_id"`
	Recurrence     RecurrenceType `json:"recurrence_type"`
	DayOfWeek      *int           `json:"day_of_week"`      // 0 (Sunday)..6, set iff Recurrence == RecurrenceWeekly
	StartTimeLocal *string        `json:"start_time_local"` // "HH:MM", set iff Recurrence == RecurrenceWeekly
	StartAt        *int64         `json:"start_at"`         // epoch millis, set iff Recurrence == RecurrenceOnce
	Active         bool           `json:"active"`
	CreatedAt      int64          `json:"created_at"`
	UpdatedAt      int64          `json:"updated_at"`
}

type EpgEntryKind string

const (
	EpgProgram  EpgEntryKind = "program"
	EpgPreRoll  EpgEntryKind = "preroll"
	EpgMidRoll  EpgEntryKind = "midroll"
	EpgPostRoll EpgEntryKind = "postroll"
	EpgTail     EpgEntryKind = "tail"
	EpgFallback EpgEntryKind = "fallback"
)

// EpgEntry is the materialized, flattened timeline — the only table GET /epg
// reads. Regenerated asynchronously on writes to scheduling tables, and
// rolled forward on a timer so a window stays materialized ahead of "now".
type EpgEntry struct {
	ID                   string       `json:"id"`
	TenantID             string       `json:"tenant_id"`
	ChannelID            string       `json:"channel_id"`
	Kind                 EpgEntryKind `json:"kind"`
	CatalogItemID        string       `json:"catalog_item_id"`
	StartAt              int64        `json:"start_at"`
	EndAt                int64        `json:"end_at"`
	SourceScheduleItemID *string      `json:"source_schedule_item_id"` // nil for fallback entries
	GeneratedAt          int64        `json:"generated_at"`
	// RotationNext is the persisted rotation position of a continuous
	// schedule item: the block-item index its next program takes. Internal
	// bookkeeping, never served to clients; nil for every other entry.
	RotationNext *int `json:"-"`
}

// EpgRevision is the one-row-per-tenant version marker GET /epg/meta serves,
// so clients can poll for staleness without paying for the full EPG payload.
type EpgRevision struct {
	TenantID   string `json:"tenant_id"`
	Revision   int64  `json:"revision"`
	ValidUntil int64  `json:"valid_until"` // end of the materialized window
	UpdatedAt  int64  `json:"updated_at"`
}
