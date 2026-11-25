package domain

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Weekdays is the set of weekdays (0 = Sunday .. 6 = Saturday) a weekly
// schedule item airs on. It is stored as one comma-separated TEXT column
// ("4,5") and always serialized as a JSON array, never null.
type Weekdays []int

// NormalizeWeekdays validates a list of weekdays from a request (at least one,
// each 0-6, no duplicates) and returns it sorted ascending.
func NormalizeWeekdays(days []int) (Weekdays, error) {
	if len(days) == 0 {
		return nil, errors.New("days_of_week (a list of 0-6) is required for weekly recurrence")
	}
	seen := make(map[int]bool, len(days))
	out := make(Weekdays, 0, len(days))
	for _, d := range days {
		if d < 0 || d > 6 {
			return nil, errors.New("days_of_week values must be between 0 (Sunday) and 6 (Saturday)")
		}
		if seen[d] {
			return nil, errors.New("days_of_week must not contain duplicates")
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Ints(out)
	return out, nil
}

// Contains reports whether d is one of the weekdays.
func (w Weekdays) Contains(d int) bool {
	for _, x := range w {
		if x == d {
			return true
		}
	}
	return false
}

// Value implements driver.Valuer: "" for no days, otherwise "4,5".
func (w Weekdays) Value() (driver.Value, error) {
	parts := make([]string, len(w))
	for i, d := range w {
		parts[i] = strconv.Itoa(d)
	}
	return strings.Join(parts, ","), nil
}

// Scan implements sql.Scanner for the comma-separated column.
func (w *Weekdays) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		*w = nil
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("domain: cannot scan %T into Weekdays", src)
	}
	out := Weekdays{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		d, err := strconv.Atoi(part)
		if err != nil {
			return fmt.Errorf("domain: bad weekday %q: %w", part, err)
		}
		out = append(out, d)
	}
	*w = out
	return nil
}

// MarshalJSON always emits an array ([] for a non-weekly item).
func (w Weekdays) MarshalJSON() ([]byte, error) {
	if len(w) == 0 {
		return []byte("[]"), nil
	}
	parts := make([]string, len(w))
	for i, d := range w {
		parts[i] = strconv.Itoa(d)
	}
	return []byte("[" + strings.Join(parts, ",") + "]"), nil
}

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
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	ScheduleID string `json:"schedule_id"`
	// BlockID and CollectionID are the item's target, mutually exclusive: a
	// block (an authored list of items) or a collection aired as the rule it
	// is. Exactly one is set.
	BlockID      string `json:"block_id,omitempty"`
	CollectionID string `json:"collection_id,omitempty"`
	// CollectionName is the collection's current name, filled on read (a
	// correlated subquery, see api.scheduleItemColumns) so a client can label
	// the item without a second lookup. Nil when the item has no collection
	// target.
	CollectionName *string `json:"collection_name,omitempty"`
	// PlaybackOrder is how a collection item walks its matches (the orders a
	// criteria block item accepts: title, release_date, random, shuffle,
	// interleaved). Set iff CollectionID is set.
	PlaybackOrder  string         `json:"playback_order,omitempty"`
	Recurrence     RecurrenceType `json:"recurrence_type"`
	DaysOfWeek     Weekdays       `json:"days_of_week"`     // 0 (Sunday)..6, at least one iff Recurrence == RecurrenceWeekly, empty otherwise
	StartTimeLocal *string        `json:"start_time_local"` // "HH:MM", set iff Recurrence == RecurrenceWeekly
	StartAt        *int64         `json:"start_at"`         // epoch millis, set iff Recurrence == RecurrenceOnce
	Active         bool           `json:"active"`
	CreatedAt      int64          `json:"created_at"`
	UpdatedAt      int64          `json:"updated_at"`
}

// HasCollection reports whether the item airs a collection instead of a block.
func (s ScheduleItem) HasCollection() bool { return s.CollectionID != "" }

// ScheduleCollectionKey is the key the materializer files a collection
// schedule item's walk under: the item is walked as a single criteria item
// whose id is the schedule item's own (see scheduleItemRunKey), so the ids of a
// block and of a schedule item can never collide in one channel's run map.
const scheduleCollectionKeyPrefix = "schedule_item:"

// ScheduleCollectionKey returns the block-shaped key of a collection schedule
// item, used where the materializer expects a block id.
func ScheduleCollectionKey(scheduleItemID string) string {
	return scheduleCollectionKeyPrefix + scheduleItemID
}

// RunKey is what the materializer keys the item's walk by: its block id, or the
// synthetic key of a collection item (see ScheduleCollectionKey).
func (s ScheduleItem) RunKey() string {
	if s.HasCollection() {
		return ScheduleCollectionKey(s.ID)
	}
	return s.BlockID
}

// IsScheduleCollectionKey reports whether a materializer key belongs to a
// collection schedule item rather than to a block.
func IsScheduleCollectionKey(key string) bool {
	return len(key) > len(scheduleCollectionKeyPrefix) && key[:len(scheduleCollectionKeyPrefix)] == scheduleCollectionKeyPrefix
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
	// ItemOffsetMs is where in the catalog item the entry starts playing; it
	// plays for EndAt-StartAt from there. Zero (omitted from JSON) for every
	// entry except the later segments of a split item.
	ItemOffsetMs int64 `json:"item_offset_ms,omitempty"`
	// SplitGroup ties the segments of one airing of a split item together: the
	// same opaque value on each segment (program entries only, never on the
	// breaks between them). Nil (omitted) for an entry that is not a segment.
	SplitGroup *string `json:"split_group,omitempty"`
	// RotationNext is the persisted rotation position of a continuous
	// schedule item: the block-item index its next program takes. Internal
	// bookkeeping, never served to clients; nil for every other entry.
	RotationNext *int `json:"-"`
	// SourceBlockItemID and SeriesCycle are set on entries a series block
	// item produced: the item, and the cycle of its playback order the
	// episode belongs to. The materializer rebuilds a series cursor from them.
	// Internal bookkeeping, never served to clients.
	SourceBlockItemID *string `json:"-"`
	SeriesCycle       *int    `json:"-"`
	// SeriesRound is the round of an interleaved walk the entry belongs to
	// (nil for every other entry), rebuilt like SeriesCycle.
	SeriesRound *int `json:"-"`
}

// EpgRevision is the one-row-per-tenant version marker GET /epg/meta serves,
// so clients can poll for staleness without paying for the full EPG payload.
type EpgRevision struct {
	TenantID   string `json:"tenant_id"`
	Revision   int64  `json:"revision"`
	ValidUntil int64  `json:"valid_until"` // end of the materialized window
	UpdatedAt  int64  `json:"updated_at"`
}
