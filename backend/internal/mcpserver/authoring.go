package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Tools added for the authoring UI (block/pool item editing, criteria preview,
// grid dry-run). Each mirrors the REST handler named in its description,
// duplicating only the small validation the handler does (see the package
// comment for why).
func registerAuthoringTools(s *mcp.Server, db storage.DB, mat *materializer.Materializer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "update_block_item",
		Description: "Reorder (position) and/or trim (trim_duration_ms) one item of a manual block; " +
			"positions stay contiguous. Dynamic blocks are rejected. Triggers EPG regeneration. " +
			"Mirrors PUT /blocks/{id}/items/{item_id}.",
	}, updateBlockItemHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_filler_pool_item",
		Description: "Set the selection weight (positive integer) of one item of a manual filler pool. " +
			"Dynamic pools are rejected. Triggers EPG regeneration. Mirrors PUT /filler-pools/{id}/items/{item_id}.",
	}, updateFillerPoolItemHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name: "preview_criteria",
		Description: "Report what a block/pool criteria would match right now (match count, total duration, " +
			"up to 20 sample items) without persisting anything. Mirrors POST /catalog-items/preview-criteria.",
	}, previewCriteriaHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "preview_channel_grid_reset",
		Description: "Dry-run of resetting a channel's grid: the EPG entries it would get if reset now " +
			"(stored configuration, no freeze margin). Writes nothing and does not bump the EPG revision. " +
			"Mirrors GET /channels/{id}/epg-preview.",
	}, previewChannelGridResetHandler(db, mat))
}

type updateBlockItemInput struct {
	BlockID        string `json:"block_id" jsonschema:"the block the item belongs to"`
	ItemID         string `json:"item_id" jsonschema:"the block item to edit"`
	Position       *int   `json:"position,omitempty" jsonschema:"new 0-based position; the item is moved there"`
	TrimDurationMs *int64 `json:"trim_duration_ms,omitempty" jsonschema:"new trimmed duration in milliseconds (positive)"`
}

func updateBlockItemHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[updateBlockItemInput, domain.BlockItem] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in updateBlockItemInput) (*mcp.CallToolResult, domain.BlockItem, error) {
		if in.BlockID == "" || in.ItemID == "" {
			return nil, domain.BlockItem{}, errors.New("block_id and item_id are required")
		}
		if in.Position == nil && in.TrimDurationMs == nil {
			return nil, domain.BlockItem{}, errors.New("position or trim_duration_ms is required")
		}
		if in.TrimDurationMs != nil && *in.TrimDurationMs <= 0 {
			return nil, domain.BlockItem{}, errors.New("trim_duration_ms must be positive")
		}
		var criteria []*string
		if err := db.Query(ctx, &criteria, "SELECT criteria FROM block WHERE tenant_id = ? AND id = ?", defaultTenantID, in.BlockID); err != nil {
			return nil, domain.BlockItem{}, err
		}
		if len(criteria) == 0 {
			return nil, domain.BlockItem{}, errors.New("block not found")
		}
		if criteria[0] != nil {
			return nil, domain.BlockItem{}, errors.New("this block is dynamic; items are managed automatically from its criteria")
		}
		var items []domain.BlockItem
		if err := db.Query(ctx, &items, "SELECT "+blockItemColumns+" FROM block_item WHERE block_id = ? ORDER BY position", in.BlockID); err != nil {
			return nil, domain.BlockItem{}, err
		}
		found := false
		for _, it := range items {
			found = found || it.ID == in.ItemID
		}
		if !found {
			return nil, domain.BlockItem{}, errors.New("block item not found")
		}
		if in.Position != nil && (*in.Position < 0 || *in.Position >= len(items)) {
			return nil, domain.BlockItem{}, errors.New("position must be between 0 and the number of items minus one")
		}
		if in.TrimDurationMs != nil {
			if err := db.Exec(ctx, "UPDATE block_item SET trim_duration_ms = ? WHERE block_id = ? AND id = ?", *in.TrimDurationMs, in.BlockID, in.ItemID); err != nil {
				return nil, domain.BlockItem{}, err
			}
		}
		if in.Position != nil {
			ids := make([]string, 0, len(items))
			for _, it := range items {
				if it.ID != in.ItemID {
					ids = append(ids, it.ID)
				}
			}
			ids = append(ids[:*in.Position], append([]string{in.ItemID}, ids[*in.Position:]...)...)
			// UNIQUE (block_id, position): park every row on a distinct negative value first.
			if err := db.Exec(ctx, "UPDATE block_item SET position = -position - 1 WHERE block_id = ?", in.BlockID); err != nil {
				return nil, domain.BlockItem{}, err
			}
			query := "UPDATE block_item SET position = CASE id"
			args := make([]any, 0, 2*len(ids)+1)
			for i, id := range ids {
				query += " WHEN ? THEN ?"
				args = append(args, id, i)
			}
			query += " END WHERE block_id = ?"
			args = append(args, in.BlockID)
			if err := db.Exec(ctx, query, args...); err != nil {
				return nil, domain.BlockItem{}, err
			}
		}
		var item domain.BlockItem
		if err := db.Query(ctx, &item, "SELECT "+blockItemColumns+" FROM block_item WHERE block_id = ? AND id = ?", in.BlockID, in.ItemID); err != nil {
			return nil, domain.BlockItem{}, err
		}
		mat.Trigger()
		return nil, item, nil
	}
}

type updateFillerPoolItemInput struct {
	FillerPoolID string `json:"filler_pool_id" jsonschema:"the pool the item belongs to"`
	ItemID       string `json:"item_id" jsonschema:"the filler pool item to edit"`
	Weight       int    `json:"weight" jsonschema:"new selection weight, a positive integer"`
}

func updateFillerPoolItemHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[updateFillerPoolItemInput, domain.FillerPoolItem] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in updateFillerPoolItemInput) (*mcp.CallToolResult, domain.FillerPoolItem, error) {
		if in.FillerPoolID == "" || in.ItemID == "" {
			return nil, domain.FillerPoolItem{}, errors.New("filler_pool_id and item_id are required")
		}
		if in.Weight <= 0 {
			return nil, domain.FillerPoolItem{}, errors.New("weight must be a positive integer")
		}
		var criteria []*string
		if err := db.Query(ctx, &criteria, "SELECT criteria FROM filler_pool WHERE tenant_id = ? AND id = ?", defaultTenantID, in.FillerPoolID); err != nil {
			return nil, domain.FillerPoolItem{}, err
		}
		if len(criteria) == 0 {
			return nil, domain.FillerPoolItem{}, errors.New("filler pool not found")
		}
		if criteria[0] != nil {
			return nil, domain.FillerPoolItem{}, errors.New("this pool is dynamic; items are managed automatically from its criteria")
		}
		var item domain.FillerPoolItem
		if err := db.Query(ctx, &item, "SELECT id, filler_pool_id, catalog_item_id, weight FROM filler_pool_item WHERE filler_pool_id = ? AND id = ?", in.FillerPoolID, in.ItemID); err != nil {
			return nil, domain.FillerPoolItem{}, fmt.Errorf("filler pool item: %w", err)
		}
		if err := db.Exec(ctx, "UPDATE filler_pool_item SET weight = ? WHERE filler_pool_id = ? AND id = ?", in.Weight, in.FillerPoolID, in.ItemID); err != nil {
			return nil, domain.FillerPoolItem{}, err
		}
		item.Weight = in.Weight
		mat.Trigger()
		return nil, item, nil
	}
}

type previewCriteriaInput struct {
	Kind     string   `json:"kind" jsonschema:"one of: movie, episode"`
	Genres   []string `json:"genres,omitempty" jsonschema:"match any of these genre substrings"`
	YearFrom *int     `json:"year_from,omitempty" jsonschema:"inclusive lower production year"`
	YearTo   *int     `json:"year_to,omitempty" jsonschema:"inclusive upper production year"`
}

func previewCriteriaHandler(db storage.DB) mcp.ToolHandlerFor[previewCriteriaInput, catalogsync.CriteriaPreview] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in previewCriteriaInput) (*mcp.CallToolResult, catalogsync.CriteriaPreview, error) {
		switch domain.CatalogItemKind(in.Kind) {
		case domain.CatalogMovie, domain.CatalogEpisode:
		default:
			return nil, catalogsync.CriteriaPreview{}, errors.New("criteria.kind must be one of movie, episode")
		}
		if len(in.Genres) == 0 && in.YearFrom == nil && in.YearTo == nil {
			return nil, catalogsync.CriteriaPreview{}, errors.New("criteria requires at least one of genres, year_from, year_to")
		}
		res, err := catalogsync.PreviewCriteria(ctx, db, defaultTenantID, in.Kind, in.Genres, in.YearFrom, in.YearTo, 20)
		return nil, res, err
	}
}

type previewChannelGridResetInput struct {
	ChannelID string `json:"channel_id" jsonschema:"the channel to dry-run a grid reset for"`
	Start     int64  `json:"start,omitempty" jsonschema:"optional window start, epoch millis"`
	End       int64  `json:"end,omitempty" jsonschema:"optional window end, epoch millis"`
}

func previewChannelGridResetHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[previewChannelGridResetInput, previewEPGOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in previewChannelGridResetInput) (*mcp.CallToolResult, previewEPGOutput, error) {
		if in.ChannelID == "" {
			return nil, previewEPGOutput{}, errors.New("channel_id is required")
		}
		var ch domain.Channel
		if err := db.Query(ctx, &ch, "SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? AND id = ?", defaultTenantID, in.ChannelID); err != nil {
			return nil, previewEPGOutput{}, fmt.Errorf("channel: %w", err)
		}
		entries, err := mat.PreviewReset(ctx, defaultTenantID, ch, in.Start, in.End)
		if err != nil {
			return nil, previewEPGOutput{}, err
		}
		return nil, previewEPGOutput{Entries: entries}, nil
	}
}
