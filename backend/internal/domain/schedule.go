package domain

type RecurrenceType string

const (
	RecurrenceWeekly RecurrenceType = "weekly"
	RecurrenceOnce   RecurrenceType = "once"
)

// ScheduleEntry is authoring input, never read directly by a client — the
// EPG materializer expands it (together with its Block/BlockItems and any
// FillerRules in scope) into flat EpgEntry rows ahead of time.
type ScheduleEntry struct {
	ID             string         `json:"id"`
	TenantID       string         `json:"tenant_id"`
	ChannelID      string         `json:"channel_id"`
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
	ID                    string       `json:"id"`
	TenantID              string       `json:"tenant_id"`
	ChannelID             string       `json:"channel_id"`
	Kind                  EpgEntryKind `json:"kind"`
	CatalogItemID         string       `json:"catalog_item_id"`
	StartAt               int64        `json:"start_at"`
	EndAt                 int64        `json:"end_at"`
	SourceScheduleEntryID *string      `json:"source_schedule_entry_id"` // nil for fallback entries
	GeneratedAt           int64        `json:"generated_at"`
}

// EpgRevision is the one-row-per-tenant version marker GET /epg/meta serves,
// so clients can poll for staleness without paying for the full EPG payload.
type EpgRevision struct {
	TenantID   string `json:"tenant_id"`
	Revision   int64  `json:"revision"`
	ValidUntil int64  `json:"valid_until"` // end of the materialized window
	UpdatedAt  int64  `json:"updated_at"`
}
