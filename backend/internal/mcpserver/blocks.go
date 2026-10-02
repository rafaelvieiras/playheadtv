package mcpserver

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// blockColumns/blockItemColumns mirror internal/api/blocks.go's constants of
// the same name — duplicated rather than imported, see mcpserver.go's
// package comment.
const blockColumns = "id, tenant_id, name, description, criteria, created_at, updated_at"
const blockItemColumns = "id, block_id, position, item_type, COALESCE(catalog_item_id, '') AS catalog_item_id, trim_duration_ms, spec"

func registerBlockTools(s *mcp.Server, db storage.DB, mat *materializer.Materializer) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_block",
		Description: "Create a new (initially empty) content block — a named, ordered playlist of catalog items. Mirrors POST /blocks.",
	}, createBlockHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_block_item",
		Description: "Append an item to the end of a manual block's playlist: a catalog item (catalog_item_id, optional " +
			"trim_duration_ms) or a whole series (series: media_source_id plus series_external_id or series_title, and " +
			"playback_order original|random|shuffle|release_date, default original; no trim). A series item airs its " +
			"episodes in that order until the next scheduled occurrence or the end of the window, so items after it in " +
			"the block never get on the air. Use search_series to find a series. Reorder with update_block_item. " +
			"Triggers EPG regeneration. Mirrors POST /blocks/{id}/items.",
	}, addBlockItemHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_block_item",
		Description: "Remove one item (catalog or series) from a manual block. Positions are not renumbered. Triggers EPG regeneration. Mirrors DELETE /blocks/{id}/items/{item_id}.",
	}, deleteBlockItemHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name: "search_series",
		Description: "List the series in the catalog (episodes grouped by media source and series) with episode count " +
			"and total duration, optionally filtered by series title (case- and accent-insensitive) or media source. " +
			"Mirrors GET /series.",
	}, searchSeriesHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_block_items",
		Description: "List a block's items in playback order (by position), so a build_block session can verify its own result. Mirrors GET /blocks/{id}/items.",
	}, listBlockItemsHandler(db))
}

func fetchBlockExists(ctx context.Context, db storage.DB, id string) error {
	var b domain.Block
	return db.Query(ctx, &b,
		"SELECT "+blockColumns+" FROM block WHERE tenant_id = ? AND id = ?",
		defaultTenantID, id,
	)
}

type createBlockInput struct {
	Name        string  `json:"name" jsonschema:"block display name"`
	Description *string `json:"description,omitempty" jsonschema:"optional free-text description"`
}

func createBlockHandler(db storage.DB) mcp.ToolHandlerFor[createBlockInput, domain.Block] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createBlockInput) (*mcp.CallToolResult, domain.Block, error) {
		if in.Name == "" {
			return nil, domain.Block{}, errors.New("name is required")
		}
		now := nowMillis()
		b := domain.Block{
			ID:          uuid.NewString(),
			TenantID:    defaultTenantID,
			Name:        in.Name,
			Description: in.Description,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		err := db.Exec(ctx,
			"INSERT INTO block (id, tenant_id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			b.ID, b.TenantID, b.Name, b.Description, b.CreatedAt, b.UpdatedAt,
		)
		if err != nil {
			return nil, domain.Block{}, err
		}
		return nil, b, nil
	}
}

type addBlockSeriesInput struct {
	MediaSourceID    string `json:"media_source_id" jsonschema:"media source the series belongs to"`
	SeriesExternalID string `json:"series_external_id,omitempty" jsonschema:"the series' id in its media source (preferred)"`
	SeriesTitle      string `json:"series_title,omitempty" jsonschema:"series title, used when there is no series_external_id"`
	PlaybackOrder    string `json:"playback_order,omitempty" jsonschema:"original (season, episode), random, shuffle (every episode once before repeats) or release_date; default original"`
}

type addBlockItemInput struct {
	BlockID        string               `json:"block_id" jsonschema:"the block to append to"`
	CatalogItemID  string               `json:"catalog_item_id,omitempty" jsonschema:"the catalog item to append (omit when adding a series)"`
	TrimDurationMs *int64               `json:"trim_duration_ms,omitempty" jsonschema:"optional trimmed duration in milliseconds, overriding the catalog item's full duration; not valid for a series"`
	Series         *addBlockSeriesInput `json:"series,omitempty" jsonschema:"add a whole series instead of a catalog item"`
}

func addBlockItemHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[addBlockItemInput, domain.BlockItem] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in addBlockItemInput) (*mcp.CallToolResult, domain.BlockItem, error) {
		if in.BlockID == "" {
			return nil, domain.BlockItem{}, errors.New("block_id is required")
		}
		var block domain.Block
		if err := db.Query(ctx, &block,
			"SELECT "+blockColumns+" FROM block WHERE tenant_id = ? AND id = ?", defaultTenantID, in.BlockID,
		); err != nil {
			return nil, domain.BlockItem{}, err
		}
		if block.Criteria != nil {
			return nil, domain.BlockItem{}, errors.New("this block is dynamic; items are managed automatically from its criteria")
		}

		itemType := domain.BlockItemCatalog
		var specJSON *string
		if in.Series != nil {
			itemType = domain.BlockItemSeries
			if in.CatalogItemID != "" {
				return nil, domain.BlockItem{}, errors.New("catalog_item_id is not valid for a series item")
			}
			if in.TrimDurationMs != nil {
				return nil, domain.BlockItem{}, errors.New("trim_duration_ms is not valid for a series item; add a trimmed episode as its own item before the series")
			}
			spec, err := series.NewSpec(in.Series.MediaSourceID, in.Series.SeriesExternalID, in.Series.SeriesTitle, in.Series.PlaybackOrder, nil)
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			found, err := series.Exists(ctx, db, defaultTenantID, spec)
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			if !found {
				return nil, domain.BlockItem{}, errors.New("no episodes of that series in the catalog")
			}
			encoded, err := series.Encode(spec)
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			specJSON = &encoded
		} else if in.CatalogItemID == "" {
			return nil, domain.BlockItem{}, errors.New("catalog_item_id or series is required")
		}

		var maxPosition []int
		if err := db.Query(ctx, &maxPosition,
			"SELECT COALESCE(MAX(position), -1) FROM block_item WHERE block_id = ?", in.BlockID,
		); err != nil {
			return nil, domain.BlockItem{}, err
		}
		position := 0
		if len(maxPosition) == 1 {
			position = maxPosition[0] + 1
		}

		item := domain.BlockItem{
			ID:             uuid.NewString(),
			BlockID:        in.BlockID,
			Position:       position,
			ItemType:       itemType,
			CatalogItemID:  in.CatalogItemID,
			TrimDurationMs: in.TrimDurationMs,
			Spec:           specJSON,
		}
		var catalogItemID *string
		if itemType == domain.BlockItemCatalog {
			catalogItemID = &item.CatalogItemID
		}
		err := db.Exec(ctx,
			"INSERT INTO block_item (id, block_id, position, item_type, catalog_item_id, trim_duration_ms, spec) VALUES (?, ?, ?, ?, ?, ?, ?)",
			item.ID, item.BlockID, item.Position, item.ItemType, catalogItemID, item.TrimDurationMs, item.Spec,
		)
		if err != nil {
			return nil, domain.BlockItem{}, err
		}
		items := []domain.BlockItem{item}
		if err := series.Decorate(ctx, db, defaultTenantID, items); err != nil {
			return nil, domain.BlockItem{}, err
		}
		mat.Trigger()
		return nil, items[0], nil
	}
}

type deleteBlockItemInput struct {
	BlockID string `json:"block_id" jsonschema:"the block the item belongs to"`
	ItemID  string `json:"item_id" jsonschema:"the block item to remove"`
}

type deleteBlockItemOutput struct {
	Deleted bool `json:"deleted"`
}

func deleteBlockItemHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[deleteBlockItemInput, deleteBlockItemOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in deleteBlockItemInput) (*mcp.CallToolResult, deleteBlockItemOutput, error) {
		if in.BlockID == "" || in.ItemID == "" {
			return nil, deleteBlockItemOutput{}, errors.New("block_id and item_id are required")
		}
		var block domain.Block
		if err := db.Query(ctx, &block,
			"SELECT "+blockColumns+" FROM block WHERE tenant_id = ? AND id = ?", defaultTenantID, in.BlockID,
		); err != nil {
			return nil, deleteBlockItemOutput{}, err
		}
		if block.Criteria != nil {
			return nil, deleteBlockItemOutput{}, errors.New("this block is dynamic; items are managed automatically from its criteria")
		}
		var item domain.BlockItem
		if err := db.Query(ctx, &item,
			"SELECT "+blockItemColumns+" FROM block_item WHERE block_id = ? AND id = ?", in.BlockID, in.ItemID,
		); err != nil {
			return nil, deleteBlockItemOutput{}, err
		}
		if err := db.Exec(ctx, "DELETE FROM block_item WHERE block_id = ? AND id = ?", in.BlockID, in.ItemID); err != nil {
			return nil, deleteBlockItemOutput{}, err
		}
		mat.Trigger()
		return nil, deleteBlockItemOutput{Deleted: true}, nil
	}
}

type searchSeriesInput struct {
	Query         string `json:"query,omitempty" jsonschema:"series title to search (case- and accent-insensitive substring)"`
	MediaSourceID string `json:"media_source_id,omitempty" jsonschema:"only series of this media source"`
	Limit         int    `json:"limit,omitempty" jsonschema:"max results, default 50, capped at 200"`
}

type searchSeriesOutput struct {
	Series []series.Summary `json:"series"`
}

func searchSeriesHandler(db storage.DB) mcp.ToolHandlerFor[searchSeriesInput, searchSeriesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in searchSeriesInput) (*mcp.CallToolResult, searchSeriesOutput, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}
		out, err := series.List(ctx, db, defaultTenantID, in.Query, in.MediaSourceID, limit, 0)
		if err != nil {
			return nil, searchSeriesOutput{}, err
		}
		if out == nil {
			out = []series.Summary{}
		}
		return nil, searchSeriesOutput{Series: out}, nil
	}
}

type listBlockItemsInput struct {
	BlockID string `json:"block_id" jsonschema:"the block to list items for"`
}

type listBlockItemsOutput struct {
	Items []domain.BlockItem `json:"items"`
}

func listBlockItemsHandler(db storage.DB) mcp.ToolHandlerFor[listBlockItemsInput, listBlockItemsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listBlockItemsInput) (*mcp.CallToolResult, listBlockItemsOutput, error) {
		if in.BlockID == "" {
			return nil, listBlockItemsOutput{}, errors.New("block_id is required")
		}
		if err := fetchBlockExists(ctx, db, in.BlockID); err != nil {
			return nil, listBlockItemsOutput{}, err
		}
		var items []domain.BlockItem
		err := db.Query(ctx, &items,
			"SELECT "+blockItemColumns+" FROM block_item WHERE block_id = ? ORDER BY position",
			in.BlockID,
		)
		if err != nil {
			return nil, listBlockItemsOutput{}, err
		}
		if err := series.Decorate(ctx, db, defaultTenantID, items); err != nil {
			return nil, listBlockItemsOutput{}, err
		}
		return nil, listBlockItemsOutput{Items: items}, nil
	}
}
