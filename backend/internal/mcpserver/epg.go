package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// epgEntryColumns mirrors internal/api/epg.go's epgEntryColumns constant —
// duplicated rather than imported, see mcpserver.go's package comment.
const epgEntryColumns = "id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, source_schedule_item_id, generated_at"

func registerEPGTools(s *mcp.Server, db storage.DB) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "preview_epg",
		Description: "Read the materialized EPG entries in a time window (epoch millis), for one " +
			"channel or all of them, to verify a schedule/block/filler change actually produced the " +
			"expected programming. Only reads epg_entry as it stands (no on-the-fly schedule " +
			"expansion) — the same real materializer the REST API and web UI read from. Mirrors GET /epg.",
	}, previewEPGHandler(db))
}

type previewEPGInput struct {
	ChannelID string `json:"channel_id,omitempty" jsonschema:"restrict to one channel; omit for every channel"`
	Start     int64  `json:"start" jsonschema:"window start, epoch millis"`
	End       int64  `json:"end" jsonschema:"window end, epoch millis"`
}

type previewEPGOutput struct {
	Entries []domain.EpgEntry `json:"entries"`
}

func previewEPGHandler(db storage.DB) mcp.ToolHandlerFor[previewEPGInput, previewEPGOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in previewEPGInput) (*mcp.CallToolResult, previewEPGOutput, error) {
		query := "SELECT " + epgEntryColumns + " FROM epg_entry WHERE tenant_id = ?"
		args := []any{defaultTenantID}

		if in.ChannelID != "" {
			query += " AND channel_id = ?"
			args = append(args, in.ChannelID)
		}
		query += " AND end_at >= ? AND start_at <= ?"
		args = append(args, in.Start, in.End)
		query += " ORDER BY channel_id, start_at"

		var entries []domain.EpgEntry
		if err := db.Query(ctx, &entries, query, args...); err != nil {
			return nil, previewEPGOutput{}, err
		}
		return nil, previewEPGOutput{Entries: entries}, nil
	}
}
