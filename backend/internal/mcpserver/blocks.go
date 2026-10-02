package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/blockitem"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// blockColumns/blockItemColumns mirror internal/api/blocks.go's constants of
// the same name — duplicated rather than imported, see mcpserver.go's
// package comment.
const blockColumns = "id, tenant_id, name, description, criteria, playback_order, interleave_spec, playback_seed, created_at, updated_at"
const blockItemColumns = "id, block_id, position, item_type, COALESCE(catalog_item_id, '') AS catalog_item_id, trim_duration_ms, spec"

func registerBlockTools(s *mcp.Server, db storage.DB, mat *materializer.Materializer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_block",
		Description: "Create a new content block — a named, ordered playlist of catalog items (initially empty). With criteria " +
			"(a condition tree as in preview_criteria) the block is dynamic: its items are every current match, owned by the " +
			"system, aired in a loop by title. Only for a dynamic block, playback_order interleaved airs the matches " +
			"round-robin across series instead (interleave: per_round episodes per series per round, default 1; " +
			"on_exhaustion restart (default: when every series has been fully played they all start over) or drop " +
			"(an exhausted series leaves the rounds, the others continue; then the block loops); series_order title " +
			"(default), release_date, random or shuffle sequences the series within a round). Items without a series " +
			"count as one-item series. Mirrors POST /blocks.",
	}, createBlockHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_block",
		Description: "Replace a block's name, description, criteria and playback settings (a full replace like PUT: omitting " +
			"criteria makes the block manual, omitting playback_order means title). See create_block for the fields. " +
			"Changing the interleaved order or its per_round or series_order restarts the block's cursor. " +
			"Mirrors PUT /blocks/{id}.",
	}, updateBlockHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_block_item",
		Description: "Append an item to the end of a manual block's playlist: a catalog item (catalog_item_id, optional " +
			"trim_duration_ms) or a whole series (series: media_source_id plus series_external_id or series_title, and " +
			"playback_order original|random|shuffle|release_date, default original; amount {mode, count}: per visit of " +
			"the block rotation air count episodes (mode count, the default, 1 episode), until the end of the " +
			"schedule slot (slot_end; items after it never air) or the rest of the playback cycle (series_end); no trim). " +
			"The series cursor is shared across visits. Use search_series to find a series. Or a criteria item " +
			"(criteria: query = a condition tree as in preview_criteria, playback_order title|release_date|random|shuffle|interleaved, " +
			"default title (interleaved: round-robin across series, with interleave {per_round, on_exhaustion restart|drop, " +
			"series_order}, see create_block); amount, required: {mode count, count N} to air N matches per visit or {mode all} for every " +
			"match once; no trim): the block plays that many matches of the live query, then moves on. " +
			"Reorder with update_block_item. " +
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

type blockInterleaveInput struct {
	PerRound     *int   `json:"per_round,omitempty" jsonschema:"episodes a series airs per round (positive integer), default 1"`
	OnExhaustion string `json:"on_exhaustion,omitempty" jsonschema:"restart (default: once every series has been fully played they all start over) or drop (an exhausted series leaves the rounds; when all are done the block repeats as it normally does)"`
	SeriesOrder  string `json:"series_order,omitempty" jsonschema:"how the series are sequenced within a round: title (default), release_date (earliest premiere), random (fresh every round) or shuffle (fixed within a playback cycle)"`
}

func (i *blockInterleaveInput) domain() *domain.InterleaveInput {
	if i == nil {
		return nil
	}
	return &domain.InterleaveInput{PerRound: i.PerRound, OnExhaustion: i.OnExhaustion, SeriesOrder: i.SeriesOrder}
}

type createBlockInput struct {
	Name          string                `json:"name" jsonschema:"block display name"`
	Description   *string               `json:"description,omitempty" jsonschema:"optional free-text description"`
	Criteria      map[string]any        `json:"criteria,omitempty" jsonschema:"makes the block dynamic: a condition tree, see preview_criteria"`
	PlaybackOrder string                `json:"playback_order,omitempty" jsonschema:"dynamic blocks only: title (default, every match by title, looped) or interleaved (round-robin across series)"`
	Interleave    *blockInterleaveInput `json:"interleave,omitempty" jsonschema:"parameters of the interleaved order; only with playback_order interleaved"`
}

// parseBlockCriteria validates the optional criteria of a block write and
// returns the canonical condition tree JSON to store (nil: manual block).
func parseBlockCriteria(c map[string]any) (*string, error) {
	if c == nil {
		return nil, nil
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, errors.New("criteria must be a JSON object")
	}
	q, err := catalogsync.ParseQuery(raw)
	if err != nil {
		return nil, errors.New("criteria: " + err.Error())
	}
	s := q.JSON()
	return &s, nil
}

func createBlockHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[createBlockInput, domain.Block] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createBlockInput) (*mcp.CallToolResult, domain.Block, error) {
		if in.Name == "" {
			return nil, domain.Block{}, errors.New("name is required")
		}
		criteria, err := parseBlockCriteria(in.Criteria)
		if err != nil {
			return nil, domain.Block{}, err
		}
		playback, _, err := blockitem.NewBlockPlayback(criteria != nil, in.PlaybackOrder, in.Interleave.domain(), nil)
		if err != nil {
			return nil, domain.Block{}, err
		}
		now := nowMillis()
		b := domain.Block{
			ID:             uuid.NewString(),
			TenantID:       defaultTenantID,
			Name:           in.Name,
			Description:    in.Description,
			Criteria:       criteria,
			PlaybackOrder:  playback.Order,
			InterleaveSpec: playback.InterleaveSpec,
			PlaybackSeed:   playback.Seed,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		b.FillInterleave()
		err = db.Exec(ctx,
			"INSERT INTO block (id, tenant_id, name, description, criteria, playback_order, interleave_spec, playback_seed, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			b.ID, b.TenantID, b.Name, b.Description, b.Criteria, b.PlaybackOrder, b.InterleaveSpec, b.PlaybackSeed, b.CreatedAt, b.UpdatedAt,
		)
		if err != nil {
			return nil, domain.Block{}, err
		}
		if b.Criteria != nil {
			if err := catalogsync.ResolveDynamicBlocks(ctx, db, defaultTenantID); err != nil {
				return nil, domain.Block{}, err
			}
			mat.Trigger()
		}
		return nil, b, nil
	}
}

type updateBlockInput struct {
	BlockID string `json:"block_id" jsonschema:"the block to replace"`
	createBlockInput
}

func updateBlockHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[updateBlockInput, domain.Block] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in updateBlockInput) (*mcp.CallToolResult, domain.Block, error) {
		if in.BlockID == "" || in.Name == "" {
			return nil, domain.Block{}, errors.New("block_id and name are required")
		}
		var prev domain.Block
		if err := db.Query(ctx, &prev, "SELECT "+blockColumns+" FROM block WHERE tenant_id = ? AND id = ?", defaultTenantID, in.BlockID); err != nil {
			return nil, domain.Block{}, err
		}
		prev.FillInterleave()
		criteria, err := parseBlockCriteria(in.Criteria)
		if err != nil {
			return nil, domain.Block{}, err
		}
		playback, restart, err := blockitem.NewBlockPlayback(criteria != nil, in.PlaybackOrder, in.Interleave.domain(), &prev)
		if err != nil {
			return nil, domain.Block{}, err
		}
		if err := db.Exec(ctx,
			"UPDATE block SET name = ?, description = ?, criteria = ?, playback_order = ?, interleave_spec = ?, playback_seed = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			in.Name, in.Description, criteria, playback.Order, playback.InterleaveSpec, playback.Seed, nowMillis(), defaultTenantID, in.BlockID,
		); err != nil {
			return nil, domain.Block{}, err
		}
		if err := catalogsync.ClearCriteriaReview(ctx, db, defaultTenantID, catalogsync.OwnerBlock, in.BlockID); err != nil {
			return nil, domain.Block{}, err
		}
		if restart {
			if err := blockitem.RestartBlockCursor(ctx, db, defaultTenantID, in.BlockID); err != nil {
				return nil, domain.Block{}, err
			}
		}
		if criteria != nil {
			if err := catalogsync.ResolveDynamicBlocks(ctx, db, defaultTenantID); err != nil {
				return nil, domain.Block{}, err
			}
		}
		mat.Trigger()
		var b domain.Block
		if err := db.Query(ctx, &b, "SELECT "+blockColumns+" FROM block WHERE tenant_id = ? AND id = ?", defaultTenantID, in.BlockID); err != nil {
			return nil, domain.Block{}, err
		}
		b.FillInterleave()
		return nil, b, nil
	}
}

type addBlockSeriesInput struct {
	MediaSourceID    string                  `json:"media_source_id" jsonschema:"media source the series belongs to"`
	SeriesExternalID string                  `json:"series_external_id,omitempty" jsonschema:"the series' id in its media source (preferred)"`
	SeriesTitle      string                  `json:"series_title,omitempty" jsonschema:"series title, used when there is no series_external_id"`
	PlaybackOrder    string                  `json:"playback_order,omitempty" jsonschema:"original (season, episode), random, shuffle (every episode once before repeats) or release_date; default original"`
	Amount           *blockSeriesAmountInput `json:"amount,omitempty" jsonschema:"how much to air per visit of the block rotation; default one episode"`
}

type blockSeriesAmountInput struct {
	Mode  string `json:"mode,omitempty" jsonschema:"count (default: air count episodes), slot_end (until the end of the schedule slot; items after it never air) or series_end (the rest of the playback cycle in progress)"`
	Count *int   `json:"count,omitempty" jsonschema:"number of episodes (positive integer); only with mode count, default 1"`
}

func (a *blockSeriesAmountInput) domain() *domain.SeriesAmountInput {
	if a == nil {
		return nil
	}
	return &domain.SeriesAmountInput{Mode: a.Mode, Count: a.Count}
}

type addBlockItemInput struct {
	BlockID        string                 `json:"block_id" jsonschema:"the block to append to"`
	CatalogItemID  string                 `json:"catalog_item_id,omitempty" jsonschema:"the catalog item to append (omit when adding a series)"`
	TrimDurationMs *int64                 `json:"trim_duration_ms,omitempty" jsonschema:"optional trimmed duration in milliseconds, overriding the catalog item's full duration; not valid for a series"`
	Series         *addBlockSeriesInput   `json:"series,omitempty" jsonschema:"add a whole series instead of a catalog item"`
	Criteria       *addBlockCriteriaInput `json:"criteria,omitempty" jsonschema:"add a criteria item (a query expanded into some of its matches) instead of a catalog item"`
}

type addBlockCriteriaInput struct {
	Query         map[string]any          `json:"query" jsonschema:"the condition tree, see preview_criteria"`
	PlaybackOrder string                  `json:"playback_order,omitempty" jsonschema:"title (default), release_date, random, shuffle (every match once before repeats) or interleaved (round-robin across series)"`
	Amount        *blockSeriesAmountInput `json:"amount" jsonschema:"required: mode count with count N, or mode all (every match once)"`
	Interleave    *blockInterleaveInput   `json:"interleave,omitempty" jsonschema:"parameters of the interleaved order; only with playback_order interleaved"`
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
		if in.Series != nil && in.Criteria != nil {
			return nil, domain.BlockItem{}, errors.New("series and criteria cannot both be set")
		}
		if in.Criteria != nil {
			itemType = domain.BlockItemCriteria
			if in.CatalogItemID != "" {
				return nil, domain.BlockItem{}, errors.New("catalog_item_id is not valid for a criteria item")
			}
			if in.TrimDurationMs != nil {
				return nil, domain.BlockItem{}, errors.New("trim_duration_ms is not valid for a criteria item; add a trimmed item as its own item before it")
			}
			rawQuery, err := json.Marshal(in.Criteria.Query)
			if err != nil {
				return nil, domain.BlockItem{}, errors.New("criteria.query must be a JSON object")
			}
			spec, err := blockitem.NewCriteriaSpec(rawQuery, in.Criteria.PlaybackOrder, in.Criteria.Amount.domain(), in.Criteria.Interleave.domain())
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			encoded, err := blockitem.EncodeCriteriaSpec(spec)
			if err != nil {
				return nil, domain.BlockItem{}, err
			}
			specJSON = &encoded
		} else if in.Series != nil {
			itemType = domain.BlockItemSeries
			if in.CatalogItemID != "" {
				return nil, domain.BlockItem{}, errors.New("catalog_item_id is not valid for a series item")
			}
			if in.TrimDurationMs != nil {
				return nil, domain.BlockItem{}, errors.New("trim_duration_ms is not valid for a series item; add a trimmed episode as its own item before the series")
			}
			spec, err := series.NewSpec(in.Series.MediaSourceID, in.Series.SeriesExternalID, in.Series.SeriesTitle, in.Series.PlaybackOrder, in.Series.Amount.domain(), nil)
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
			return nil, domain.BlockItem{}, errors.New("catalog_item_id, series or criteria is required")
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
		if err := blockitem.Decorate(ctx, db, defaultTenantID, items); err != nil {
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
		if err := blockitem.Decorate(ctx, db, defaultTenantID, items); err != nil {
			return nil, listBlockItemsOutput{}, err
		}
		return nil, listBlockItemsOutput{Items: items}, nil
	}
}
