package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerScheduleTools(s *mcp.Server, db storage.DB, mat *materializer.Materializer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_schedule",
		Description: "Create a reusable, named Schedule (a group of recurrence rules) — assign it to " +
			"a channel via assign_channel_schedule, and to more than one if they should air the same " +
			"programming grid. Mirrors POST /schedules.",
	}, createScheduleHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_schedule_item",
		Description: "Add one recurrence rule (a block airing either weekly, via days_of_week (one or more weekdays) + " +
			"start_time_local, or once, via start_at epoch millis) to a Schedule. Resets the " +
			"EPG grid of every channel that Schedule is assigned to (same as reset-epg). Mirrors POST " +
			"/schedules/{id}/items.",
	}, createScheduleItemHandler(db, mat))
}

type createScheduleInput struct {
	Name        string  `json:"name" jsonschema:"schedule display name (e.g. 'Weekday grid')"`
	Description *string `json:"description,omitempty"`
}

func createScheduleHandler(db storage.DB) mcp.ToolHandlerFor[createScheduleInput, domain.Schedule] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createScheduleInput) (*mcp.CallToolResult, domain.Schedule, error) {
		if in.Name == "" {
			return nil, domain.Schedule{}, errors.New("name is required")
		}
		now := nowMillis()
		sch := domain.Schedule{
			ID:          uuid.NewString(),
			TenantID:    defaultTenantID,
			Name:        in.Name,
			Description: in.Description,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		err := db.Exec(ctx,
			"INSERT INTO schedule (id, tenant_id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			sch.ID, sch.TenantID, sch.Name, sch.Description, sch.CreatedAt, sch.UpdatedAt,
		)
		if err != nil {
			return nil, domain.Schedule{}, err
		}
		return nil, sch, nil
	}
}

type createScheduleItemInput struct {
	ScheduleID string `json:"schedule_id" jsonschema:"the Schedule this item belongs to"`
	// Exactly one target: BlockID (an authored list) or CollectionID (a rule
	// aired as it is, with PlaybackOrder).
	BlockID        string  `json:"block_id,omitempty" jsonschema:"the block that airs; mutually exclusive with collection_id"`
	CollectionID   string  `json:"collection_id,omitempty" jsonschema:"the collection that airs, with no block; mutually exclusive with block_id. Its matches air in playback_order"`
	PlaybackOrder  string  `json:"playback_order,omitempty" jsonschema:"how a collection item walks its matches: title, release_date, random, shuffle or interleaved; required with collection_id and invalid without it"`
	RecurrenceType string  `json:"recurrence_type" jsonschema:"one of: weekly, once, continuous (no time fields; fills whatever timed items leave, resuming its block rotation; at most one per schedule)"`
	DaysOfWeek     []int   `json:"days_of_week,omitempty" jsonschema:"weekdays 0 (Sunday)-6, at least one and no duplicates; required and only valid for recurrence_type=weekly (e.g. [4,5] for Thursday and Friday)"`
	StartTimeLocal *string `json:"start_time_local,omitempty" jsonschema:"local time of day as HH:MM; required and only valid for recurrence_type=weekly"`
	StartAt        *int64  `json:"start_at,omitempty" jsonschema:"epoch millis; required and only valid for recurrence_type=once"`
	Active         *bool   `json:"active,omitempty" jsonschema:"whether the item is active; defaults to true when omitted"`
}

// validateCreateScheduleItemInput mirrors internal/api/schedules.go's
// validateScheduleItemRequest exactly: weekly requires
// days_of_week+start_time_local and forbids start_at; once requires start_at
// and forbids the other two.
func validateCreateScheduleItemInput(in createScheduleItemInput) error {
	if in.ScheduleID == "" {
		return errors.New("schedule_id is required")
	}
	switch {
	case in.BlockID == "" && in.CollectionID == "":
		return errors.New("one of block_id or collection_id is required")
	case in.BlockID != "" && in.CollectionID != "":
		return errors.New("block_id and collection_id are mutually exclusive")
	case in.CollectionID != "" && in.PlaybackOrder == "":
		return errors.New("playback_order is required for a collection item")
	case in.CollectionID == "" && in.PlaybackOrder != "":
		return errors.New("playback_order is only valid for a collection item")
	case in.PlaybackOrder != "" && !domain.ValidCriteriaOrder(in.PlaybackOrder):
		return errors.New("playback_order must be one of title, release_date, random, shuffle, interleaved")
	}
	switch domain.RecurrenceType(in.RecurrenceType) {
	case domain.RecurrenceWeekly:
		if _, err := domain.NormalizeWeekdays(in.DaysOfWeek); err != nil {
			return err
		}
		if in.StartTimeLocal == nil {
			return errors.New("start_time_local is required for weekly recurrence")
		}
		if _, err := time.Parse("15:04", *in.StartTimeLocal); err != nil {
			return errors.New("start_time_local must be HH:MM")
		}
		if in.StartAt != nil {
			return errors.New("start_at must not be set for weekly recurrence")
		}
	case domain.RecurrenceOnce:
		if in.StartAt == nil {
			return errors.New("start_at is required for once recurrence")
		}
		if len(in.DaysOfWeek) > 0 || in.StartTimeLocal != nil {
			return errors.New("days_of_week/start_time_local must not be set for once recurrence")
		}
	case domain.RecurrenceContinuous:
		if len(in.DaysOfWeek) > 0 || in.StartTimeLocal != nil || in.StartAt != nil {
			return errors.New("days_of_week/start_time_local/start_at must not be set for continuous recurrence")
		}
	default:
		return errors.New("recurrence_type must be one of weekly, once, continuous")
	}
	return nil
}

func createScheduleItemHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[createScheduleItemInput, domain.ScheduleItem] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createScheduleItemInput) (*mcp.CallToolResult, domain.ScheduleItem, error) {
		if err := validateCreateScheduleItemInput(in); err != nil {
			return nil, domain.ScheduleItem{}, err
		}
		if domain.RecurrenceType(in.RecurrenceType) == domain.RecurrenceContinuous {
			var ids []string
			if err := db.Query(ctx, &ids,
				"SELECT id FROM schedule_item WHERE tenant_id = ? AND schedule_id = ? AND recurrence_type = 'continuous'",
				defaultTenantID, in.ScheduleID,
			); err != nil {
				return nil, domain.ScheduleItem{}, err
			}
			if len(ids) > 0 {
				return nil, domain.ScheduleItem{}, errors.New("a schedule can have at most one continuous item")
			}
		}
		if in.CollectionID != "" {
			if _, err := catalogsync.LoadCollectionQuery(ctx, db, defaultTenantID, in.CollectionID); err != nil {
				return nil, domain.ScheduleItem{}, errors.New("collection_id does not match a collection")
			}
		}
		days := domain.Weekdays{}
		if domain.RecurrenceType(in.RecurrenceType) == domain.RecurrenceWeekly {
			days, _ = domain.NormalizeWeekdays(in.DaysOfWeek) // validated above
		}
		active := true
		if in.Active != nil {
			active = *in.Active
		}
		now := nowMillis()
		si := domain.ScheduleItem{
			ID:             uuid.NewString(),
			TenantID:       defaultTenantID,
			ScheduleID:     in.ScheduleID,
			BlockID:        in.BlockID,
			CollectionID:   in.CollectionID,
			PlaybackOrder:  in.PlaybackOrder,
			Recurrence:     domain.RecurrenceType(in.RecurrenceType),
			DaysOfWeek:     days,
			StartTimeLocal: in.StartTimeLocal,
			StartAt:        in.StartAt,
			Active:         active,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		err := db.Exec(ctx,
			`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, collection_id, playback_order, recurrence_type, days_of_week, start_time_local, start_at, active, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			si.ID, si.TenantID, si.ScheduleID, nullableString(si.BlockID), nullableString(si.CollectionID),
			nullableString(si.PlaybackOrder), string(si.Recurrence), si.DaysOfWeek, si.StartTimeLocal,
			si.StartAt, si.Active, si.CreatedAt, si.UpdatedAt,
		)
		if err != nil {
			return nil, domain.ScheduleItem{}, err
		}
		mat.ResetAfterScheduleEdit(ctx, defaultTenantID, si.ScheduleID)
		return nil, si, nil
	}
}

// nullableString is the DB value of an optional text column: nil when empty, so
// the mutual-exclusion CHECK of schedule_item sees a real NULL.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
