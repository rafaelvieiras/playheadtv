package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// catalogItemColumns mirrors internal/api/catalog_items.go's
// catalogItemColumns constant — duplicated rather than imported, see
// mcpserver.go's package comment.
const catalogItemColumns = "id, tenant_id, media_source_id, external_id, kind, title, series_title, duration_ms, source_modified_at, synced_at"

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

		query := "SELECT " + catalogItemColumns + " FROM catalog_item WHERE tenant_id = ?"
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
		return nil, searchCatalogOutput{Items: items}, nil
	}
}
