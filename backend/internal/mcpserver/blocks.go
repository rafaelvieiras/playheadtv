package mcpserver

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// blockColumns/blockItemColumns mirror internal/api/blocks.go's constants of
// the same name — duplicated rather than imported, see mcpserver.go's
// package comment.
const blockColumns = "id, tenant_id, name, description, created_at, updated_at"
const blockItemColumns = "id, block_id, position, catalog_item_id, trim_duration_ms"

func registerBlockTools(s *mcp.Server, db storage.DB, mat *materializer.Materializer) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_block",
		Description: "Create a new (initially empty) content block — a named, ordered playlist of catalog items. Mirrors POST /blocks.",
	}, createBlockHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_block_item",
		Description: "Append a catalog item to the end of a block's playlist (position is always " +
			"MAX(position)+1 — there is no reordering in this v1). Triggers EPG regeneration. Mirrors " +
			"POST /blocks/{id}/items.",
	}, addBlockItemHandler(db, mat))

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

type addBlockItemInput struct {
	BlockID        string `json:"block_id" jsonschema:"the block to append to"`
	CatalogItemID  string `json:"catalog_item_id" jsonschema:"the catalog item to append"`
	TrimDurationMs *int64 `json:"trim_duration_ms,omitempty" jsonschema:"optional trimmed duration in milliseconds, overriding the catalog item's full duration"`
}

func addBlockItemHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[addBlockItemInput, domain.BlockItem] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in addBlockItemInput) (*mcp.CallToolResult, domain.BlockItem, error) {
		if in.BlockID == "" {
			return nil, domain.BlockItem{}, errors.New("block_id is required")
		}
		if in.CatalogItemID == "" {
			return nil, domain.BlockItem{}, errors.New("catalog_item_id is required")
		}
		if err := fetchBlockExists(ctx, db, in.BlockID); err != nil {
			return nil, domain.BlockItem{}, err
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
			CatalogItemID:  in.CatalogItemID,
			TrimDurationMs: in.TrimDurationMs,
		}
		err := db.Exec(ctx,
			"INSERT INTO block_item (id, block_id, position, catalog_item_id, trim_duration_ms) VALUES (?, ?, ?, ?, ?)",
			item.ID, item.BlockID, item.Position, item.CatalogItemID, item.TrimDurationMs,
		)
		if err != nil {
			return nil, domain.BlockItem{}, err
		}
		mat.Trigger()
		return nil, item, nil
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
		return nil, listBlockItemsOutput{Items: items}, nil
	}
}
