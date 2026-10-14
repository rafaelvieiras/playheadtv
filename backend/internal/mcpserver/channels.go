package mcpserver

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/api"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// channelColumns mirrors internal/api/channels.go's channelColumns constant
// — duplicated rather than imported, see mcpserver.go's package comment.
const channelColumns = "id, tenant_id, number, name, guide_art_url, timezone, schedule_id, base_schedule_id, created_at, updated_at"

func registerChannelTools(s *mcp.Server, db storage.DB, mat *materializer.Materializer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_channel",
		Description: "Create a new linear TV channel (the numbered entry that appears in the " +
			"program guide). Mirrors POST /channels.",
	}, createChannelHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_channels",
		Description: "List every channel for this tenant, ordered by channel number. Mirrors GET /channels.",
	}, listChannelsHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "assign_channel_schedule",
		Description: "Assign a Schedule to a channel (or clear it, by omitting schedule_id), driving " +
			"what that channel airs — issue #7's Schedule/Playout split. Resets the channel's EPG grid immediately (same as reset-epg). " +
			"Mirrors the schedule_id field of PUT /channels/{id}.",
	}, assignChannelScheduleHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name: "assign_channel_base_schedule",
		Description: "Assign a base Schedule to a channel (or clear it, by omitting base_schedule_id). The base " +
			"schedule plays its own programming, with its own preroll/midroll/postroll/tail filler, in every gap the " +
			"channel's pinned schedule (assign_channel_schedule) leaves uncovered, instead of the continuous item and plain " +
			"fallback filler; a pinned occurrence always wins and the base continues at the position it would have had anyway. " +
			"It must be a different schedule than the pinned one. Resets the channel's EPG grid immediately. " +
			"Mirrors the base_schedule_id field of PUT /channels/{id}.",
	}, assignChannelBaseScheduleHandler(db, mat))
}

type createChannelInput struct {
	Number      int     `json:"number" jsonschema:"channel number shown in the guide (e.g. 1, 2, 101)"`
	Name        string  `json:"name" jsonschema:"channel display name"`
	GuideArtURL *string `json:"guide_art_url,omitempty" jsonschema:"optional guide artwork/logo URL"`
	Timezone    string  `json:"timezone,omitempty" jsonschema:"IANA timezone name (e.g. America/Sao_Paulo); defaults to UTC when omitted"`
}

func createChannelHandler(db storage.DB) mcp.ToolHandlerFor[createChannelInput, domain.Channel] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createChannelInput) (*mcp.CallToolResult, domain.Channel, error) {
		if in.Name == "" {
			return nil, domain.Channel{}, errors.New("name is required")
		}
		tz := in.Timezone
		if tz == "" {
			tz = "UTC"
		}

		now := nowMillis()
		ch := domain.Channel{
			ID:          uuid.NewString(),
			TenantID:    defaultTenantID,
			Number:      in.Number,
			Name:        in.Name,
			GuideArtURL: in.GuideArtURL,
			Timezone:    tz,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		err := db.Exec(ctx,
			`INSERT INTO channel (id, tenant_id, number, name, guide_art_url, timezone, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ch.ID, ch.TenantID, ch.Number, ch.Name, ch.GuideArtURL, ch.Timezone, ch.CreatedAt, ch.UpdatedAt,
		)
		if err != nil {
			return nil, domain.Channel{}, err
		}
		return nil, ch, nil
	}
}

type listChannelsInput struct{}

type listChannelsOutput struct {
	Channels []domain.Channel `json:"channels"`
}

func listChannelsHandler(db storage.DB) mcp.ToolHandlerFor[listChannelsInput, listChannelsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ listChannelsInput) (*mcp.CallToolResult, listChannelsOutput, error) {
		var channels []domain.Channel
		err := db.Query(ctx, &channels,
			"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? ORDER BY number",
			defaultTenantID,
		)
		if err != nil {
			return nil, listChannelsOutput{}, err
		}
		return nil, listChannelsOutput{Channels: channels}, nil
	}
}

type assignChannelScheduleInput struct {
	ChannelID  string  `json:"channel_id" jsonschema:"the channel to assign a Schedule to"`
	ScheduleID *string `json:"schedule_id,omitempty" jsonschema:"the Schedule to assign; omit/null to clear it"`
}

func assignChannelScheduleHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[assignChannelScheduleInput, domain.Channel] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in assignChannelScheduleInput) (*mcp.CallToolResult, domain.Channel, error) {
		if in.ChannelID == "" {
			return nil, domain.Channel{}, errors.New("channel_id is required")
		}
		var current domain.Channel
		if err := db.Query(ctx, &current,
			"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? AND id = ?",
			defaultTenantID, in.ChannelID,
		); err != nil {
			return nil, domain.Channel{}, err
		}
		if err := api.ValidateChannelSchedules(ctx, db, in.ScheduleID, current.BaseScheduleID); err != nil {
			return nil, domain.Channel{}, err
		}
		err := db.Exec(ctx,
			"UPDATE channel SET schedule_id = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			in.ScheduleID, nowMillis(), defaultTenantID, in.ChannelID,
		)
		if err != nil {
			return nil, domain.Channel{}, err
		}
		var ch domain.Channel
		if err := db.Query(ctx, &ch,
			"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? AND id = ?",
			defaultTenantID, in.ChannelID,
		); err != nil {
			return nil, domain.Channel{}, err
		}
		mat.ResetAfterChannelEdit(ctx, defaultTenantID, ch)
		return nil, ch, nil
	}
}

type assignChannelBaseScheduleInput struct {
	ChannelID      string  `json:"channel_id" jsonschema:"the channel to assign a base Schedule to"`
	BaseScheduleID *string `json:"base_schedule_id,omitempty" jsonschema:"the Schedule to use as the base layer; omit/null to clear it. Must differ from the channel's pinned schedule"`
}

func assignChannelBaseScheduleHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[assignChannelBaseScheduleInput, domain.Channel] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in assignChannelBaseScheduleInput) (*mcp.CallToolResult, domain.Channel, error) {
		if in.ChannelID == "" {
			return nil, domain.Channel{}, errors.New("channel_id is required")
		}
		var current domain.Channel
		if err := db.Query(ctx, &current,
			"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? AND id = ?",
			defaultTenantID, in.ChannelID,
		); err != nil {
			return nil, domain.Channel{}, err
		}
		if err := api.ValidateChannelSchedules(ctx, db, current.ScheduleID, in.BaseScheduleID); err != nil {
			return nil, domain.Channel{}, err
		}
		if err := db.Exec(ctx,
			"UPDATE channel SET base_schedule_id = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			in.BaseScheduleID, nowMillis(), defaultTenantID, in.ChannelID,
		); err != nil {
			return nil, domain.Channel{}, err
		}
		var ch domain.Channel
		if err := db.Query(ctx, &ch,
			"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? AND id = ?",
			defaultTenantID, in.ChannelID,
		); err != nil {
			return nil, domain.Channel{}, err
		}
		mat.ResetAfterChannelEdit(ctx, defaultTenantID, ch)
		return nil, ch, nil
	}
}
