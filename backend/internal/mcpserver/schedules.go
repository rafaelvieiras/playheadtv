package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
		Description: "Add one recurrence rule (a block airing either weekly, via day_of_week + " +
			"start_time_local, or once, via start_at epoch millis) to a Schedule. Triggers EPG " +
			"regeneration for every channel that Schedule is assigned to. Mirrors POST " +
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
	ScheduleID     string  `json:"schedule_id" jsonschema:"the Schedule this item belongs to"`
	BlockID        string  `json:"block_id" jsonschema:"the block that airs"`
	RecurrenceType string  `json:"recurrence_type" jsonschema:"one of: weekly, once"`
	DayOfWeek      *int    `json:"day_of_week,omitempty" jsonschema:"0 (Sunday)-6; required and only valid for recurrence_type=weekly"`
	StartTimeLocal *string `json:"start_time_local,omitempty" jsonschema:"local time of day as HH:MM; required and only valid for recurrence_type=weekly"`
	StartAt        *int64  `json:"start_at,omitempty" jsonschema:"epoch millis; required and only valid for recurrence_type=once"`
	Active         *bool   `json:"active,omitempty" jsonschema:"whether the item is active; defaults to true when omitted"`
}

// validateCreateScheduleItemInput mirrors internal/api/schedules.go's
// validateScheduleItemRequest exactly: weekly requires
// day_of_week+start_time_local and forbids start_at; once requires start_at
// and forbids the other two.
func validateCreateScheduleItemInput(in createScheduleItemInput) error {
	if in.ScheduleID == "" {
		return errors.New("schedule_id is required")
	}
	if in.BlockID == "" {
		return errors.New("block_id is required")
	}
	switch domain.RecurrenceType(in.RecurrenceType) {
	case domain.RecurrenceWeekly:
		if in.DayOfWeek == nil || *in.DayOfWeek < 0 || *in.DayOfWeek > 6 {
			return errors.New("day_of_week (0-6) is required for weekly recurrence")
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
		if in.DayOfWeek != nil || in.StartTimeLocal != nil {
			return errors.New("day_of_week/start_time_local must not be set for once recurrence")
		}
	default:
		return errors.New("recurrence_type must be one of weekly, once")
	}
	return nil
}

func createScheduleItemHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[createScheduleItemInput, domain.ScheduleItem] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createScheduleItemInput) (*mcp.CallToolResult, domain.ScheduleItem, error) {
		if err := validateCreateScheduleItemInput(in); err != nil {
			return nil, domain.ScheduleItem{}, err
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
			Recurrence:     domain.RecurrenceType(in.RecurrenceType),
			DayOfWeek:      in.DayOfWeek,
			StartTimeLocal: in.StartTimeLocal,
			StartAt:        in.StartAt,
			Active:         active,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		err := db.Exec(ctx,
			`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, recurrence_type, day_of_week, start_time_local, start_at, active, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			si.ID, si.TenantID, si.ScheduleID, si.BlockID, string(si.Recurrence), si.DayOfWeek, si.StartTimeLocal,
			si.StartAt, si.Active, si.CreatedAt, si.UpdatedAt,
		)
		if err != nil {
			return nil, domain.ScheduleItem{}, err
		}
		mat.Trigger()
		return nil, si, nil
	}
}
