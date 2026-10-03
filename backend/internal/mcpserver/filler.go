package mcpserver

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// fillerPoolColumns/fillerRuleColumns mirror internal/api/filler.go's
// constants of the same name — duplicated rather than imported, see
// mcpserver.go's package comment. fillerRuleColumns keeps the same
// scope_type/selection_strategy -> Scope/Selection column aliasing (see
// AGENTS.md's scan.go gotcha).
const fillerPoolColumns = "id, tenant_id, name, collection_id, " +
	"(SELECT c.name FROM collection c WHERE c.tenant_id = filler_pool.tenant_id AND c.id = filler_pool.collection_id) AS collection_name, " +
	"(collection_id IS NOT NULL OR criteria IS NOT NULL) AS dynamic, created_at"
const fillerRuleColumns = "id, tenant_id, scope_type AS scope, scope_id, kind, filler_pool_id, selection_strategy AS selection, position, created_at"

// manage_filler covers only the minimal useful surface for v1 — no
// update/delete on filler_pool/filler_rule, see docs/ROADMAP.md.
func registerFillerTools(s *mcp.Server, db storage.DB, mat *materializer.Materializer) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_filler_pool",
		Description: "Create a new filler pool — a named group of catalog items filler rules draw from. Manual (initially empty) by default; with collection_id (see create_collection) it is dynamic: its items are every current match of the collection, owned by the system. Mirrors POST /filler-pools.",
	}, createFillerPoolHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_filler_pool_item",
		Description: "Add a catalog item to a filler pool with a selection weight (used by the " +
			"weighted selection strategy). Triggers EPG regeneration. Mirrors POST /filler-pools/{id}/items.",
	}, addFillerPoolItemHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_filler_rule",
		Description: "Attach a filler rule to a channel or block: when/where (kind: preroll, " +
			"midroll, postroll, tail, fallback) filler from a pool gets inserted, and how it's picked " +
			"(selection_strategy). Triggers EPG regeneration. Mirrors POST /filler-rules.",
	}, createFillerRuleHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_filler_pools",
		Description: "List every filler pool for this tenant. Mirrors GET /filler-pools.",
	}, listFillerPoolsHandler(db))
}

type createFillerPoolInput struct {
	Name         string `json:"name" jsonschema:"filler pool display name"`
	CollectionID string `json:"collection_id,omitempty" jsonschema:"makes the pool dynamic: the id of a collection (see create_collection); omit for a manual pool"`
}

func createFillerPoolHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[createFillerPoolInput, domain.FillerPool] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createFillerPoolInput) (*mcp.CallToolResult, domain.FillerPool, error) {
		if in.Name == "" {
			return nil, domain.FillerPool{}, errors.New("name is required")
		}
		collection, err := blockCollection(ctx, db, in.CollectionID)
		if err != nil {
			return nil, domain.FillerPool{}, err
		}
		id := uuid.NewString()
		if err := db.Exec(ctx,
			"INSERT INTO filler_pool (id, tenant_id, name, collection_id, created_at) VALUES (?, ?, ?, ?, ?)",
			id, defaultTenantID, in.Name, collection, nowMillis(),
		); err != nil {
			return nil, domain.FillerPool{}, err
		}
		if collection != nil {
			if err := catalogsync.ResolveDynamicPools(ctx, db, defaultTenantID); err != nil {
				return nil, domain.FillerPool{}, err
			}
			mat.Trigger()
		}
		var p domain.FillerPool
		if err := db.Query(ctx, &p, "SELECT "+fillerPoolColumns+" FROM filler_pool WHERE tenant_id = ? AND id = ?", defaultTenantID, id); err != nil {
			return nil, domain.FillerPool{}, err
		}
		return nil, p, nil
	}
}

func fetchFillerPoolExists(ctx context.Context, db storage.DB, id string) error {
	var p domain.FillerPool
	return db.Query(ctx, &p,
		"SELECT "+fillerPoolColumns+" FROM filler_pool WHERE tenant_id = ? AND id = ?",
		defaultTenantID, id,
	)
}

type addFillerPoolItemInput struct {
	FillerPoolID  string `json:"filler_pool_id" jsonschema:"the filler pool to add to"`
	CatalogItemID string `json:"catalog_item_id" jsonschema:"the catalog item to add"`
	Weight        int    `json:"weight,omitempty" jsonschema:"selection weight for the weighted strategy; defaults to 1 when omitted"`
}

func addFillerPoolItemHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[addFillerPoolItemInput, domain.FillerPoolItem] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in addFillerPoolItemInput) (*mcp.CallToolResult, domain.FillerPoolItem, error) {
		if in.FillerPoolID == "" {
			return nil, domain.FillerPoolItem{}, errors.New("filler_pool_id is required")
		}
		if in.CatalogItemID == "" {
			return nil, domain.FillerPoolItem{}, errors.New("catalog_item_id is required")
		}
		if err := fetchFillerPoolExists(ctx, db, in.FillerPoolID); err != nil {
			return nil, domain.FillerPoolItem{}, err
		}
		weight := in.Weight
		if weight == 0 {
			weight = 1 // matches the schema's DEFAULT 1 when omitted
		}
		item := domain.FillerPoolItem{
			ID:            uuid.NewString(),
			FillerPoolID:  in.FillerPoolID,
			CatalogItemID: in.CatalogItemID,
			Weight:        weight,
		}
		err := db.Exec(ctx,
			"INSERT INTO filler_pool_item (id, filler_pool_id, catalog_item_id, weight) VALUES (?, ?, ?, ?)",
			item.ID, item.FillerPoolID, item.CatalogItemID, item.Weight,
		)
		if err != nil {
			return nil, domain.FillerPoolItem{}, err
		}
		mat.Trigger()
		return nil, item, nil
	}
}

type createFillerRuleInput struct {
	ScopeType         string `json:"scope_type" jsonschema:"one of: channel, block"`
	ScopeID           string `json:"scope_id" jsonschema:"the Channel.ID or Block.ID this rule applies to, per scope_type"`
	Kind              string `json:"kind" jsonschema:"one of: preroll, midroll, postroll, tail, fallback"`
	FillerPoolID      string `json:"filler_pool_id" jsonschema:"the filler pool this rule draws from"`
	SelectionStrategy string `json:"selection_strategy" jsonschema:"one of: sequential, random, weighted"`
	Position          int    `json:"position,omitempty" jsonschema:"ordering among multiple rules of the same kind/scope"`
}

func validateFillerRuleInput(in createFillerRuleInput) error {
	switch domain.FillerScope(in.ScopeType) {
	case domain.ScopeChannel, domain.ScopeBlock:
	default:
		return errors.New("scope_type must be one of channel, block")
	}
	if in.ScopeID == "" {
		return errors.New("scope_id is required")
	}
	switch domain.FillerKind(in.Kind) {
	case domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll, domain.FillerTail, domain.FillerFallback:
	default:
		return errors.New("kind must be one of preroll, midroll, postroll, tail, fallback")
	}
	if in.FillerPoolID == "" {
		return errors.New("filler_pool_id is required")
	}
	switch domain.SelectionStrategy(in.SelectionStrategy) {
	case domain.SelectionSequential, domain.SelectionRandom, domain.SelectionWeighted:
	default:
		return errors.New("selection_strategy must be one of sequential, random, weighted")
	}
	return nil
}

func createFillerRuleHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[createFillerRuleInput, domain.FillerRule] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createFillerRuleInput) (*mcp.CallToolResult, domain.FillerRule, error) {
		if err := validateFillerRuleInput(in); err != nil {
			return nil, domain.FillerRule{}, err
		}
		fr := domain.FillerRule{
			ID:           uuid.NewString(),
			TenantID:     defaultTenantID,
			Scope:        domain.FillerScope(in.ScopeType),
			ScopeID:      in.ScopeID,
			Kind:         domain.FillerKind(in.Kind),
			FillerPoolID: in.FillerPoolID,
			Selection:    domain.SelectionStrategy(in.SelectionStrategy),
			Position:     in.Position,
			CreatedAt:    nowMillis(),
		}
		err := db.Exec(ctx,
			`INSERT INTO filler_rule (id, tenant_id, scope_type, scope_id, kind, filler_pool_id, selection_strategy, position, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			fr.ID, fr.TenantID, string(fr.Scope), fr.ScopeID, string(fr.Kind), fr.FillerPoolID,
			string(fr.Selection), fr.Position, fr.CreatedAt,
		)
		if err != nil {
			return nil, domain.FillerRule{}, err
		}
		mat.Trigger()
		return nil, fr, nil
	}
}

type listFillerPoolsInput struct{}

type listFillerPoolsOutput struct {
	Pools []domain.FillerPool `json:"pools"`
}

func listFillerPoolsHandler(db storage.DB) mcp.ToolHandlerFor[listFillerPoolsInput, listFillerPoolsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ listFillerPoolsInput) (*mcp.CallToolResult, listFillerPoolsOutput, error) {
		var pools []domain.FillerPool
		err := db.Query(ctx, &pools,
			"SELECT "+fillerPoolColumns+" FROM filler_pool WHERE tenant_id = ? ORDER BY name",
			defaultTenantID,
		)
		if err != nil {
			return nil, listFillerPoolsOutput{}, err
		}
		return nil, listFillerPoolsOutput{Pools: pools}, nil
	}
}
