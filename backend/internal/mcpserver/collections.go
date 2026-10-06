package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Collection tools mirror internal/api/collections.go (see the package
// comment for why the small validation is duplicated).
func registerCollectionTools(s *mcp.Server, db storage.DB, mat *materializer.Materializer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_collection",
		Description: "Create a collection: a named, reusable catalog query (a versioned condition tree, see preview_criteria) " +
			"that dynamic blocks, dynamic filler pools and criteria block items reference by collection_id. A collection holds " +
			"only the query; playback order, amount and interleave settings stay on whatever uses it. Mirrors POST /collections.",
	}, createCollectionHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_collection",
		Description: "Replace a collection's name, description and query (a full replace like PUT). Editing a shared collection " +
			"changes every block, pool and criteria item that uses it: check get_collection_usage first. Dynamic blocks and pools " +
			"are re-resolved and the EPG regenerated. Mirrors PUT /collections/{id}.",
	}, updateCollectionHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_collection",
		Description: "Delete a collection. Refused while any block, filler pool or criteria item uses it; the error lists them " +
			"(point them at another collection first). DESTRUCTIVE: requires the argument confirm=true; without it the call is rejected and nothing changes. Mirrors DELETE /collections/{id}.",
	}, deleteCollectionHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_collections",
		Description: "List every collection with its query and how many blocks, pools and criteria items use it. Mirrors GET /collections.",
	}, listCollectionsHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_collection",
		Description: "Read one collection. Mirrors GET /collections/{id}.",
	}, getCollectionHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_collection_usage",
		Description: "List the blocks, filler pools and criteria block items that use a collection. Mirrors GET /collections/{id}/usage.",
	}, collectionUsageHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "apply_criteria_review_proposal",
		Description: "Resolve one entry of list_criteria_migration_review: create (or reuse, for an identical tree) a collection " +
			"holding the proposed query, or the query given, and point the block, pool or criteria item at it. owner_type is " +
			"block, filler_pool or block_item. Mirrors POST /criteria-migration-review/apply.",
	}, applyReviewHandler(db, mat))
}

type collectionIDInput struct {
	CollectionID string `json:"collection_id" jsonschema:"the collection"`
}

type deleteCollectionInput struct {
	CollectionID string `json:"collection_id" jsonschema:"the collection"`
	Confirmation
}

type createCollectionInput struct {
	Name        string         `json:"name" jsonschema:"collection display name"`
	Description *string        `json:"description,omitempty" jsonschema:"optional free-text description"`
	Query       map[string]any `json:"query" jsonschema:"the condition tree, see preview_criteria"`
}

func parseCollectionInput(name string, query map[string]any) (string, *catalogsync.Query, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, errors.New("name is required")
	}
	raw, err := json.Marshal(query)
	if err != nil || query == nil {
		return "", nil, errors.New("query must be a JSON object")
	}
	q, err := catalogsync.ParseQuery(raw)
	if err != nil {
		return "", nil, errors.New("query: " + err.Error())
	}
	return name, q, nil
}

func createCollectionHandler(db storage.DB) mcp.ToolHandlerFor[createCollectionInput, domain.Collection] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createCollectionInput) (*mcp.CallToolResult, domain.Collection, error) {
		name, q, err := parseCollectionInput(in.Name, in.Query)
		if err != nil {
			return nil, domain.Collection{}, err
		}
		id := uuid.NewString()
		now := nowMillis()
		if err := db.Exec(ctx,
			"INSERT INTO collection (id, tenant_id, name, description, query, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			id, defaultTenantID, name, in.Description, q.JSON(), now, now,
		); err != nil {
			return nil, domain.Collection{}, err
		}
		c, err := catalogsync.GetCollection(ctx, db, defaultTenantID, id)
		return nil, c, err
	}
}

type updateCollectionInput struct {
	CollectionID string `json:"collection_id" jsonschema:"the collection to replace"`
	createCollectionInput
}

func updateCollectionHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[updateCollectionInput, domain.Collection] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in updateCollectionInput) (*mcp.CallToolResult, domain.Collection, error) {
		prev, err := catalogsync.GetCollection(ctx, db, defaultTenantID, in.CollectionID)
		if err != nil {
			return nil, domain.Collection{}, err
		}
		name, q, err := parseCollectionInput(in.Name, in.Query)
		if err != nil {
			return nil, domain.Collection{}, err
		}
		if err := db.Exec(ctx,
			"UPDATE collection SET name = ?, description = ?, query = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			name, in.Description, q.JSON(), nowMillis(), defaultTenantID, prev.ID,
		); err != nil {
			return nil, domain.Collection{}, err
		}
		if q.JSON() != prev.Query {
			if err := catalogsync.ResolveCollection(ctx, db, defaultTenantID, prev.ID); err != nil {
				return nil, domain.Collection{}, err
			}
			if prev.UsageCount > 0 {
				mat.Trigger()
			}
		}
		c, err := catalogsync.GetCollection(ctx, db, defaultTenantID, prev.ID)
		return nil, c, err
	}
}

type deleteCollectionOutput struct {
	Deleted bool `json:"deleted"`
}

func deleteCollectionHandler(db storage.DB) mcp.ToolHandlerFor[deleteCollectionInput, deleteCollectionOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in deleteCollectionInput) (*mcp.CallToolResult, deleteCollectionOutput, error) {
		if err := requireConfirm("delete_collection", in); err != nil {
			return nil, deleteCollectionOutput{}, err
		}
		c, err := catalogsync.GetCollection(ctx, db, defaultTenantID, in.CollectionID)
		if err != nil {
			return nil, deleteCollectionOutput{}, err
		}
		var blockErr error
		err = db.Transact(ctx, func(tx storage.DB) error {
			u, err := catalogsync.CollectionUsage(ctx, tx, defaultTenantID, c.ID)
			if err != nil {
				return err
			}
			if u.Count() > 0 {
				blockErr = errors.New("this collection is in use and cannot be deleted: " + usageText(u))
				return nil
			}
			return tx.Exec(ctx, "DELETE FROM collection WHERE tenant_id = ? AND id = ?", defaultTenantID, c.ID)
		})
		if err != nil {
			return nil, deleteCollectionOutput{}, err
		}
		if blockErr != nil {
			return nil, deleteCollectionOutput{}, blockErr
		}
		return nil, deleteCollectionOutput{Deleted: true}, nil
	}
}

func usageText(u domain.CollectionUsage) string {
	var parts []string
	for _, b := range u.Blocks {
		parts = append(parts, "block \""+b.Name+"\" ("+b.ID+")")
	}
	for _, p := range u.FillerPools {
		parts = append(parts, "filler pool \""+p.Name+"\" ("+p.ID+")")
	}
	for _, i := range u.BlockItems {
		parts = append(parts, "criteria item "+i.ID+" of block \""+i.BlockName+"\"")
	}
	return strings.Join(parts, "; ")
}

type listCollectionsInput struct{}

type listCollectionsOutput struct {
	Collections []domain.Collection `json:"collections"`
}

func listCollectionsHandler(db storage.DB) mcp.ToolHandlerFor[listCollectionsInput, listCollectionsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ listCollectionsInput) (*mcp.CallToolResult, listCollectionsOutput, error) {
		cs := []domain.Collection{}
		if err := db.Query(ctx, &cs, "SELECT "+catalogsync.CollectionColumns+" FROM collection WHERE tenant_id = ? ORDER BY name, id", defaultTenantID); err != nil {
			return nil, listCollectionsOutput{}, err
		}
		for i := range cs {
			cs[i].Fill()
		}
		return nil, listCollectionsOutput{Collections: cs}, nil
	}
}

func getCollectionHandler(db storage.DB) mcp.ToolHandlerFor[collectionIDInput, domain.Collection] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in collectionIDInput) (*mcp.CallToolResult, domain.Collection, error) {
		c, err := catalogsync.GetCollection(ctx, db, defaultTenantID, in.CollectionID)
		return nil, c, err
	}
}

func collectionUsageHandler(db storage.DB) mcp.ToolHandlerFor[collectionIDInput, domain.CollectionUsage] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in collectionIDInput) (*mcp.CallToolResult, domain.CollectionUsage, error) {
		c, err := catalogsync.GetCollection(ctx, db, defaultTenantID, in.CollectionID)
		if err != nil {
			return nil, domain.CollectionUsage{}, err
		}
		u, err := catalogsync.CollectionUsage(ctx, db, defaultTenantID, c.ID)
		return nil, u, err
	}
}

type applyReviewInput struct {
	OwnerType string         `json:"owner_type" jsonschema:"block, filler_pool or block_item"`
	OwnerID   string         `json:"owner_id" jsonschema:"the id of the held block, pool or criteria item"`
	Query     map[string]any `json:"query,omitempty" jsonschema:"optional: use this condition tree instead of the proposed one (required when the legacy criteria has no proposal)"`
}

type applyReviewOutput struct {
	CollectionID string            `json:"collection_id"`
	Created      bool              `json:"created"`
	Collection   domain.Collection `json:"collection"`
}

func applyReviewHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[applyReviewInput, applyReviewOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in applyReviewInput) (*mcp.CallToolResult, applyReviewOutput, error) {
		var override json.RawMessage
		if in.Query != nil {
			var err error
			if override, err = json.Marshal(in.Query); err != nil {
				return nil, applyReviewOutput{}, errors.New("query must be a JSON object")
			}
		}
		id, created, err := catalogsync.ApplyReviewProposal(ctx, db, defaultTenantID, in.OwnerType, in.OwnerID, override)
		if err != nil {
			return nil, applyReviewOutput{}, err
		}
		switch in.OwnerType {
		case catalogsync.OwnerFillerPool:
			err = catalogsync.ResolveDynamicPools(ctx, db, defaultTenantID)
		case catalogsync.OwnerBlock:
			err = catalogsync.ResolveDynamicBlocks(ctx, db, defaultTenantID)
		}
		if err != nil {
			return nil, applyReviewOutput{}, err
		}
		mat.Trigger()
		c, err := catalogsync.GetCollection(ctx, db, defaultTenantID, id)
		return nil, applyReviewOutput{CollectionID: id, Created: created, Collection: c}, err
	}
}
