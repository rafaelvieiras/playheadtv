package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Same defaults/limits as internal/api/catalog_items.go.
const (
	defaultCatalogItemLimit = 50
	maxCatalogItemLimit     = 200
)

func registerCatalogTools(s *mcp.Server, db storage.DB) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_catalog",
		Description: "Search/browse synced catalog items (movies, episodes, etc.) by title or series title (case- and accent-insensitive), to find " +
			"catalog_item_id values for building blocks and filler pools. Mirrors GET /catalog-items.",
	}, searchCatalogHandler(db))
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_catalog_item",
		Description: "Get one catalog item by id with everything search_catalog returns plus cast, directors, production locations and " +
			"chapters. Mirrors GET /catalog-items/{id}.",
	}, getCatalogItemHandler(db))
}

type getCatalogItemInput struct {
	ID string `json:"id" jsonschema:"catalog_item id"`
}

type getCatalogItemOutput struct {
	Item domain.CatalogItem `json:"item"`
}

func getCatalogItemHandler(db storage.DB) mcp.ToolHandlerFor[getCatalogItemInput, getCatalogItemOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in getCatalogItemInput) (*mcp.CallToolResult, getCatalogItemOutput, error) {
		var rows []domain.CatalogItem
		if err := db.Query(ctx, &rows,
			"SELECT "+catalogsync.DetailColumns+" FROM catalog_item WHERE tenant_id = ? AND id = ?",
			defaultTenantID, in.ID,
		); err != nil {
			return nil, getCatalogItemOutput{}, err
		}
		if len(rows) == 0 {
			return nil, getCatalogItemOutput{}, fmt.Errorf("catalog item %q not found", in.ID)
		}
		if err := catalogsync.LoadRelations(ctx, db, defaultTenantID, rows, true); err != nil {
			return nil, getCatalogItemOutput{}, err
		}
		return nil, getCatalogItemOutput{Item: rows[0]}, nil
	}
}

type searchCatalogInput struct {
	Query  string `json:"query,omitempty" jsonschema:"substring to match against catalog item titles and series titles, ignoring case and accents; omit to browse all"`
	Limit  *int   `json:"limit,omitempty" jsonschema:"max results to return, 1-200 (default 50)"`
	Offset *int   `json:"offset,omitempty" jsonschema:"results to skip, for pagination (default 0)"`
}

type searchCatalogOutput struct {
	Items []domain.CatalogItem `json:"items"`
}

func searchCatalogHandler(db storage.DB) mcp.ToolHandlerFor[searchCatalogInput, searchCatalogOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in searchCatalogInput) (*mcp.CallToolResult, searchCatalogOutput, error) {
		limit := defaultCatalogItemLimit
		if in.Limit != nil && *in.Limit > 0 {
			limit = *in.Limit
		}
		if limit > maxCatalogItemLimit {
			limit = maxCatalogItemLimit
		}
		offset := 0
		if in.Offset != nil && *in.Offset > 0 {
			offset = *in.Offset
		}

		query := "SELECT " + catalogsync.ListColumns + " FROM catalog_item WHERE tenant_id = ?"
		args := []any{defaultTenantID}
		if in.Query != "" {
			clause, arg := catalogsync.SearchClause(in.Query)
			query += clause
			args = append(args, arg)
		}
		query += " ORDER BY title LIMIT ? OFFSET ?"
		args = append(args, limit, offset)

		var items []domain.CatalogItem
		if err := db.Query(ctx, &items, query, args...); err != nil {
			return nil, searchCatalogOutput{}, err
		}
		if err := catalogsync.LoadRelations(ctx, db, defaultTenantID, items, false); err != nil {
			return nil, searchCatalogOutput{}, err
		}
		return nil, searchCatalogOutput{Items: items}, nil
	}
}
