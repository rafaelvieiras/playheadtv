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
		Name: "create_schedule_entry",
		Description: "Schedule a block to air on a channel, either weekly (day_of_week + " +
			"start_time_local) or once (start_at, epoch millis). Triggers EPG regeneration. Mirrors " +
			"POST /schedule-entries.",
	}, createScheduleEntryHandler(db, mat))
}

type createScheduleEntryInput struct {
	ChannelID      string  `json:"channel_id" jsonschema:"the channel this entry schedules onto"`
	BlockID        string  `json:"block_id" jsonschema:"the block that airs"`
	RecurrenceType string  `json:"recurrence_type" jsonschema:"one of: weekly, once"`
	DayOfWeek      *int    `json:"day_of_week,omitempty" jsonschema:"0 (Sunday)-6; required and only valid for recurrence_type=weekly"`
	StartTimeLocal *string `json:"start_time_local,omitempty" jsonschema:"local time of day as HH:MM; required and only valid for recurrence_type=weekly"`
	StartAt        *int64  `json:"start_at,omitempty" jsonschema:"epoch millis; required and only valid for recurrence_type=once"`
	Active         *bool   `json:"active,omitempty" jsonschema:"whether the entry is active; defaults to true when omitted"`
}

// validateCreateScheduleEntryInput mirrors
// internal/api/schedules.go's validateScheduleEntryRequest exactly: weekly
// requires day_of_week+start_time_local and forbids start_at; once requires
// start_at and forbids the other two.
func validateCreateScheduleEntryInput(in createScheduleEntryInput) error {
	if in.ChannelID == "" {
		return errors.New("channel_id is required")
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

func createScheduleEntryHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[createScheduleEntryInput, domain.ScheduleEntry] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createScheduleEntryInput) (*mcp.CallToolResult, domain.ScheduleEntry, error) {
		if err := validateCreateScheduleEntryInput(in); err != nil {
			return nil, domain.ScheduleEntry{}, err
		}
		active := true
		if in.Active != nil {
			active = *in.Active
		}
		now := nowMillis()
		se := domain.ScheduleEntry{
			ID:             uuid.NewString(),
			TenantID:       defaultTenantID,
			ChannelID:      in.ChannelID,
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
			`INSERT INTO schedule_entry (id, tenant_id, channel_id, block_id, recurrence_type, day_of_week, start_time_local, start_at, active, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			se.ID, se.TenantID, se.ChannelID, se.BlockID, string(se.Recurrence), se.DayOfWeek, se.StartTimeLocal,
			se.StartAt, se.Active, se.CreatedAt, se.UpdatedAt,
		)
		if err != nil {
			return nil, domain.ScheduleEntry{}, err
		}
		mat.Trigger()
		return nil, se, nil
	}
}
