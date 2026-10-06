package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// TestMCPCriteriaBlockItems drives the criteria block item over real MCP:
// preview_criteria with a tree, add_block_item with a criteria item and its
// validation, update_block_item for the query, order and amount, and the
// criteria migration review tool.
func TestMCPCriteriaBlockItems(t *testing.T) {
	ts := newHarness(t)
	token := bootstrapSession(t, ts)
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: &bearerTransport{token: token}},
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()

	source := callTool[domain.MediaSource](t, ctx, session, "register_media_source", map[string]any{"type": "local_folder", "name": "Lib"})
	doJSON(t, ts, "POST", "/media-sources/"+source.ID+"/catalog-sync", token, map[string]any{
		"items": []map[string]any{
			{"external_id": "m1", "kind": "movie", "title": "Laugh", "duration_ms": 60_000, "genres": "Comedy", "production_year": 1985},
			{"external_id": "m2", "kind": "movie", "title": "Scream", "duration_ms": 60_000, "genres": "Horror", "production_year": 1986},
			{"external_id": "m3", "kind": "movie", "title": "Giggle", "duration_ms": 60_000, "genres": "Comedy", "production_year": 1995},
		},
	})
	comedy := map[string]any{"version": 1, "all": []any{
		map[string]any{"field": "genre", "op": "equals", "value": "comedy"},
		map[string]any{"not": map[string]any{"field": "year", "op": "gte", "value": 1990}},
	}}

	prev := callTool[struct {
		MatchCount int `json:"match_count"`
	}](t, ctx, session, "preview_criteria", map[string]any{"query": comedy})
	if prev.MatchCount != 1 {
		t.Errorf("preview_criteria = %+v, want only the 80s comedy", prev)
	}

	col := callTool[domain.Collection](t, ctx, session, "create_collection", map[string]any{"name": "80s comedies", "query": comedy})
	block := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Mixed"})
	item := callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{
		"block_id": block.ID,
		"criteria": map[string]any{"collection_id": col.ID, "amount": map[string]any{"mode": "count", "count": 2}},
	})
	if item.ItemType != "criteria" || item.Criteria == nil || item.Criteria.MatchCount != 1 || item.Criteria.PlaybackOrder != "title" ||
		item.Criteria.Amount.Mode != "count" || item.Criteria.Amount.Count != 2 || item.CatalogItemID != "" {
		t.Fatalf("add_block_item = %+v / %+v", item, item.Criteria)
	}

	// Rejections: amount is required, all matches must be explicit, series
	// orders and a legacy query are invalid, a catalog id does not mix in.
	bad := []map[string]any{
		{"block_id": block.ID, "criteria": map[string]any{"query": comedy}},
		{"block_id": block.ID, "criteria": map[string]any{"collection_id": col.ID, "amount": map[string]any{}}},
		{"block_id": block.ID, "criteria": map[string]any{"collection_id": col.ID, "amount": map[string]any{"mode": "count"}}},
		{"block_id": block.ID, "criteria": map[string]any{"collection_id": col.ID, "amount": map[string]any{"mode": "slot_end"}}},
		{"block_id": block.ID, "criteria": map[string]any{"collection_id": col.ID, "playback_order": "original", "amount": map[string]any{"mode": "all"}}},
		{"block_id": block.ID, "criteria": map[string]any{"collection_id": "nope", "amount": map[string]any{"mode": "all"}}},
		{"block_id": block.ID, "criteria": map[string]any{"query": comedy, "amount": map[string]any{"mode": "all"}}}, // the removed embedded query
		{"block_id": block.ID, "catalog_item_id": "x", "criteria": map[string]any{"collection_id": col.ID, "amount": map[string]any{"mode": "all"}}},
		{"block_id": block.ID, "trim_duration_ms": 5, "criteria": map[string]any{"collection_id": col.ID, "amount": map[string]any{"mode": "all"}}},
	}
	for i, args := range bad {
		callToolExpectError(t, ctx, session, "add_block_item", args)
		_ = i
	}

	allComedy := callTool[domain.Collection](t, ctx, session, "create_collection", map[string]any{"name": "All comedies",
		"query": map[string]any{"version": 1, "all": []any{map[string]any{"field": "genre", "op": "equals", "value": "Comedy"}}}})
	upd := callTool[domain.BlockItem](t, ctx, session, "update_block_item", map[string]any{
		"block_id": block.ID, "item_id": item.ID,
		"amount": map[string]any{"mode": "all"}, "playback_order": "shuffle",
		"collection_id": allComedy.ID,
	})
	if upd.Criteria == nil || upd.Criteria.Amount.Mode != "all" || upd.Criteria.PlaybackOrder != "shuffle" || upd.Criteria.MatchCount != 2 {
		t.Fatalf("update_block_item = %+v", upd.Criteria)
	}
	for _, args := range []map[string]any{
		{"block_id": block.ID, "item_id": item.ID, "playback_order": "original"},
		{"block_id": block.ID, "item_id": item.ID, "trim_duration_ms": 5},
		{"block_id": block.ID, "item_id": item.ID, "collection_id": "nope"},
		{"block_id": block.ID, "item_id": item.ID, "query": comedy}, // the removed embedded query
		{"block_id": block.ID, "item_id": item.ID, "amount": map[string]any{"mode": "count", "count": 0}},
	} {
		callToolExpectError(t, ctx, session, "update_block_item", args)
	}

	listed := callTool[struct {
		Items []domain.BlockItem `json:"items"`
	}](t, ctx, session, "list_block_items", map[string]any{"block_id": block.ID})
	if len(listed.Items) != 1 || listed.Items[0].Criteria == nil || listed.Items[0].Criteria.Amount.Mode != "all" {
		t.Errorf("list_block_items = %+v", listed.Items)
	}
	if strings.Contains(string(mustJSON(t, listed)), "seed") {
		t.Error("the seed was served")
	}

	// preview by collection id, and the tool for the collection rules.
	if p := callTool[struct {
		MatchCount int `json:"match_count"`
	}](t, ctx, session, "preview_criteria", map[string]any{"collection_id": allComedy.ID}); p.MatchCount != 2 {
		t.Errorf("preview_criteria by collection = %+v", p)
	}
	callToolExpectError(t, ctx, session, "preview_criteria", map[string]any{"collection_id": allComedy.ID, "query": comedy})
	callToolExpectError(t, ctx, session, "preview_criteria", map[string]any{"collection_id": "nope"})

	review := callTool[struct {
		Items []map[string]any `json:"items"`
	}](t, ctx, session, "list_criteria_migration_review", map[string]any{})
	if review.Items == nil || len(review.Items) != 0 {
		t.Errorf("list_criteria_migration_review = %+v, want an empty list", review.Items)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestMCPCollectionTools covers the collection CRUD, usage, the refusal to
// delete a collection in use and the review-apply tool.
func TestMCPCollectionTools(t *testing.T) {
	ts := newHarness(t)
	token := bootstrapSession(t, ts)
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: &bearerTransport{token: token}},
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()

	query := map[string]any{"version": 1, "all": []any{map[string]any{"field": "kind", "op": "equals", "value": "movie"}}}
	other := map[string]any{"version": 1, "all": []any{map[string]any{"field": "kind", "op": "equals", "value": "episode"}}}
	col := callTool[domain.Collection](t, ctx, session, "create_collection", map[string]any{"name": "Movies", "description": "all of them", "query": query})
	if col.ID == "" || col.Name != "Movies" || col.UsageCount != 0 || col.Description == nil {
		t.Fatalf("create_collection = %+v", col)
	}
	for _, args := range []map[string]any{
		{"name": "", "query": query},
		{"name": "x"},
		{"name": "x", "query": map[string]any{"version": 1, "all": []any{}}},
		{"name": "x", "query": map[string]any{"kind": "movie"}},
	} {
		callToolExpectError(t, ctx, session, "create_collection", args)
	}

	listed := callTool[struct {
		Collections []domain.Collection `json:"collections"`
	}](t, ctx, session, "list_collections", map[string]any{})
	if len(listed.Collections) != 1 || listed.Collections[0].ID != col.ID {
		t.Fatalf("list_collections = %+v", listed)
	}

	block := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Dyn", "collection_id": col.ID})
	pool := callTool[domain.FillerPool](t, ctx, session, "create_filler_pool", map[string]any{"name": "Pool", "collection_id": col.ID})
	if block.CollectionName == nil || *block.CollectionName != "Movies" || !pool.Dynamic || pool.CollectionID == nil {
		t.Fatalf("block = %+v pool = %+v", block, pool)
	}
	callToolExpectError(t, ctx, session, "create_filler_pool", map[string]any{"name": "Bad", "collection_id": "nope"})

	usage := callTool[domain.CollectionUsage](t, ctx, session, "get_collection_usage", map[string]any{"collection_id": col.ID})
	if len(usage.Blocks) != 1 || usage.Blocks[0].ID != block.ID || len(usage.FillerPools) != 1 || len(usage.BlockItems) != 0 {
		t.Errorf("get_collection_usage = %+v", usage)
	}
	if msg := callToolExpectError(t, ctx, session, "delete_collection", map[string]any{"collection_id": col.ID, "confirm": true}); !strings.Contains(msg, "Dyn") || !strings.Contains(msg, "Pool") {
		t.Errorf("delete in use error = %q, want it to list the users", msg)
	}

	upd := callTool[domain.Collection](t, ctx, session, "update_collection", map[string]any{"collection_id": col.ID, "name": "Episodes", "query": other})
	if upd.Name != "Episodes" || upd.UsageCount != 2 || !strings.Contains(string(mustJSON(t, upd.Tree)), "episode") {
		t.Errorf("update_collection = %+v", upd)
	}
	got := callTool[domain.Collection](t, ctx, session, "get_collection", map[string]any{"collection_id": col.ID})
	if got.Name != "Episodes" {
		t.Errorf("get_collection = %+v", got)
	}
	callToolExpectError(t, ctx, session, "get_collection", map[string]any{"collection_id": "nope"})

	callTool[domain.Block](t, ctx, session, "update_block", map[string]any{"block_id": block.ID, "name": "Dyn"})        // manual again
	callToolExpectError(t, ctx, session, "delete_collection", map[string]any{"collection_id": col.ID, "confirm": true}) // the pool still uses it
	callToolExpectError(t, ctx, session, "apply_criteria_review_proposal", map[string]any{"owner_type": "block", "owner_id": block.ID})
}
