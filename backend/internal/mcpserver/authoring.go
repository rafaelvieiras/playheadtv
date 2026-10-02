package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/blockitem"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Tools added for the authoring UI (block/pool item editing, criteria preview,
// grid dry-run). Each mirrors the REST handler named in its description,
// duplicating only the small validation the handler does (see the package
// comment for why).
func registerAuthoringTools(s *mcp.Server, db storage.DB, mat *materializer.Materializer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "update_block_item",
		Description: "Reorder (position), trim (trim_duration_ms, catalog items only) and/or change the playback order " +
			"(playback_order, series and criteria items; restarts the item) and/or the amount (amount {mode, count}, series " +
			"and criteria items; the cursor is kept) and/or the query (criteria items; the cursor is kept) of one item of a manual block; " +
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
		Description: "Report what a condition tree (block/pool criteria, or the query of a criteria block item) would " +
			"match right now (match count, total duration, up to 20 sample items) without persisting anything. " +
			"Mirrors POST /catalog-items/preview-criteria.",
	}, previewCriteriaHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_criteria_migration_review",
		Description: "List the dynamic blocks and filler pools whose legacy criteria ({kind, genres, year_from, year_to}) " +
			"could not be converted to a condition tree without changing what they match: each entry has the proposed " +
			"query and the items that would be added or dropped. The legacy criteria keeps working until the block/pool " +
			"is updated with a query. Mirrors GET /criteria-migration-review.",
	}, listCriteriaReviewHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "preview_channel_grid_reset",
		Description: "Dry-run of resetting a channel's grid: the EPG entries it would get if reset now " +
			"(stored configuration, no freeze margin). Writes nothing and does not bump the EPG revision. " +
			"Mirrors GET /channels/{id}/epg-preview.",
	}, previewChannelGridResetHandler(db, mat))
}

type updateBlockItemInput struct {
	BlockID        string                  `json:"block_id" jsonschema:"the block the item belongs to"`
	ItemID         string                  `json:"item_id" jsonschema:"the block item to edit"`
	Position       *int                    `json:"position,omitempty" jsonschema:"new 0-based position; the item is moved there"`
	TrimDurationMs *int64                  `json:"trim_duration_ms,omitempty" jsonschema:"new trimmed duration in milliseconds (positive); catalog items only"`
	PlaybackOrder  *string                 `json:"playback_order,omitempty" jsonschema:"series items: original, random, shuffle or release_date; criteria items: title, release_date, random or shuffle"`
	Amount         *blockSeriesAmountInput `json:"amount,omitempty" jsonschema:"series items: how much to air per visit; mode count (with count, default 1), slot_end or series_end; criteria items: mode count (with count) or all"`
	Query          map[string]any          `json:"query,omitempty" jsonschema:"criteria items only: a new condition tree"`
}

func updateBlockItemHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[updateBlockItemInput, domain.BlockItem] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in updateBlockItemInput) (*mcp.CallToolResult, domain.BlockItem, error) {
		if in.BlockID == "" || in.ItemID == "" {
			return nil, domain.BlockItem{}, errors.New("block_id and item_id are required")
		}
		if in.Position == nil && in.TrimDurationMs == nil && in.PlaybackOrder == nil && in.Amount == nil && in.Query == nil {
			return nil, domain.BlockItem{}, errors.New("position, trim_duration_ms, playback_order, amount or query is required")
		}
		if in.PlaybackOrder != nil && !domain.ValidPlaybackOrder(*in.PlaybackOrder) && !domain.ValidCriteriaOrder(*in.PlaybackOrder) {
			return nil, domain.BlockItem{}, errors.New("playback_order must be one of original, random, shuffle, release_date (series) or title, release_date, random, shuffle (criteria)")
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
		var target *domain.BlockItem
		for i := range items {
			if items[i].ID == in.ItemID {
				target = &items[i]
			}
		}
		if target == nil {
			return nil, domain.BlockItem{}, errors.New("block item not found")
		}
		if in.TrimDurationMs != nil && target.IsWalked() {
			return nil, domain.BlockItem{}, errors.New("trim_duration_ms is not valid for a series or criteria item")
		}
		if in.PlaybackOrder != nil && !target.IsWalked() {
			return nil, domain.BlockItem{}, errors.New("playback_order is only valid for a series or criteria item")
		}
		if in.Position != nil && (*in.Position < 0 || *in.Position >= len(items)) {
			return nil, domain.BlockItem{}, errors.New("position must be between 0 and the number of items minus one")
		}
		if in.Amount != nil && !target.IsWalked() {
			return nil, domain.BlockItem{}, errors.New("amount is only valid for a series or criteria item")
		}
		if in.Query != nil && !target.IsCriteria() {
			return nil, domain.BlockItem{}, errors.New("query is only valid for a criteria item")
		}
		if target.IsCriteria() {
			spec, err := blockitem.ParseCriteriaSpec(target.Spec)
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			var rawQuery json.RawMessage
			if in.Query != nil {
				if rawQuery, err = json.Marshal(in.Query); err != nil {
					return nil, domain.BlockItem{}, errors.New("query must be a JSON object")
				}
			}
			updated, orderChanged, err := blockitem.UpdateCriteriaSpec(spec, rawQuery, in.PlaybackOrder, in.Amount.domain())
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			encoded, err := blockitem.EncodeCriteriaSpec(updated)
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			if err := db.Exec(ctx, "UPDATE block_item SET spec = ? WHERE block_id = ? AND id = ?", encoded, in.BlockID, in.ItemID); err != nil {
				return nil, domain.BlockItem{}, err
			}
			target.Spec = &encoded
			if orderChanged {
				nowMs := nowMillis()
				if err := db.Exec(ctx, "UPDATE series_cursor SET origin_ms = ? WHERE tenant_id = ? AND block_item_id = ?", nowMs, defaultTenantID, in.ItemID); err != nil {
					return nil, domain.BlockItem{}, err
				}
				if err := db.Exec(ctx, "DELETE FROM epg_entry WHERE tenant_id = ? AND source_block_item_id = ? AND end_at > ?", defaultTenantID, in.ItemID, nowMs); err != nil {
					return nil, domain.BlockItem{}, err
				}
			}
		}
		var newAmount *domain.SeriesAmount
		if in.Amount != nil && target.IsSeries() {
			a, err := series.NewAmount(in.Amount.domain())
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			newAmount = &a
		}
		if in.PlaybackOrder != nil && target.IsSeries() && !domain.ValidPlaybackOrder(*in.PlaybackOrder) {
			return nil, domain.BlockItem{}, errors.New("playback_order must be one of original, random, shuffle, release_date")
		}
		if newAmount != nil {
			spec, err := series.ParseSpec(target.Spec)
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			spec.Amount = newAmount
			encoded, err := series.Encode(spec)
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			if err := db.Exec(ctx, "UPDATE block_item SET spec = ? WHERE block_id = ? AND id = ?", encoded, in.BlockID, in.ItemID); err != nil {
				return nil, domain.BlockItem{}, err
			}
			target.Spec = &encoded
		}
		if in.PlaybackOrder != nil && target.IsSeries() {
			spec, err := series.ParseSpec(target.Spec)
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			if spec.PlaybackOrder != *in.PlaybackOrder {
				spec.PlaybackOrder = *in.PlaybackOrder
				encoded, err := series.Encode(spec)
				if err != nil {
					return nil, domain.BlockItem{}, err
				}
				nowMs := nowMillis()
				if err := db.Exec(ctx, "UPDATE block_item SET spec = ? WHERE block_id = ? AND id = ?", encoded, in.BlockID, in.ItemID); err != nil {
					return nil, domain.BlockItem{}, err
				}
				// Restart the cursor under the new order (see PUT /blocks/{id}/items/{item_id}).
				if err := db.Exec(ctx, "UPDATE series_cursor SET origin_ms = ? WHERE tenant_id = ? AND block_item_id = ?", nowMs, defaultTenantID, in.ItemID); err != nil {
					return nil, domain.BlockItem{}, err
				}
				if err := db.Exec(ctx, "DELETE FROM epg_entry WHERE tenant_id = ? AND source_block_item_id = ? AND end_at > ?", defaultTenantID, in.ItemID, nowMs); err != nil {
					return nil, domain.BlockItem{}, err
				}
			}
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
		updated := []domain.BlockItem{item}
		if err := blockitem.Decorate(ctx, db, defaultTenantID, updated); err != nil {
			return nil, domain.BlockItem{}, err
		}
		mat.Trigger()
		return nil, updated[0], nil
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
	Query map[string]any `json:"query" jsonschema:"a versioned condition tree: {version: 1, all|any: [node, ...] | not: node | field/op/value}; fields: genre, studio, tag, audio_language, subtitle_language (equals|in, exact normalized names), title, overview, person (contains), kind (movie|episode), content_rating (equals|in), year, duration (minutes), community_rating, critic_rating, season_number, episode_number (equals|lt|lte|gt|gte|between), premiere_date, date_added (same, YYYY-MM-DD)"`
}

func previewCriteriaHandler(db storage.DB) mcp.ToolHandlerFor[previewCriteriaInput, catalogsync.CriteriaPreview] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in previewCriteriaInput) (*mcp.CallToolResult, catalogsync.CriteriaPreview, error) {
		raw, err := json.Marshal(in.Query)
		if err != nil {
			return nil, catalogsync.CriteriaPreview{}, errors.New("query must be a JSON object")
		}
		q, err := catalogsync.ParseQuery(raw)
		if err != nil {
			return nil, catalogsync.CriteriaPreview{}, fmt.Errorf("criteria: %w", err)
		}
		res, err := catalogsync.PreviewQuery(ctx, db, defaultTenantID, q, 20)
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

type listCriteriaReviewInput struct{}

type listCriteriaReviewOutput struct {
	Items []catalogsync.ReviewItem `json:"items"`
}

func listCriteriaReviewHandler(db storage.DB) mcp.ToolHandlerFor[listCriteriaReviewInput, listCriteriaReviewOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ listCriteriaReviewInput) (*mcp.CallToolResult, listCriteriaReviewOutput, error) {
		items, err := catalogsync.ListCriteriaReview(ctx, db, defaultTenantID)
		return nil, listCriteriaReviewOutput{Items: items}, err
	}
}
