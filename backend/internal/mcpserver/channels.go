package mcpserver

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// channelColumns mirrors internal/api/channels.go's channelColumns constant
// — duplicated rather than imported, see mcpserver.go's package comment.
const channelColumns = "id, tenant_id, number, name, guide_art_url, timezone, created_at, updated_at"

func registerChannelTools(s *mcp.Server, db storage.DB) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_channel",
		Description: "Create a new linear TV channel (the numbered entry that appears in the " +
			"program guide). Mirrors POST /channels.",
	}, createChannelHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_channels",
		Description: "List every channel for this tenant, ordered by channel number. Mirrors GET /channels.",
	}, listChannelsHandler(db))
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
