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

	block := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Mixed"})
	item := callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{
		"block_id": block.ID,
		"criteria": map[string]any{"query": comedy, "amount": map[string]any{"mode": "count", "count": 2}},
	})
	if item.ItemType != "criteria" || item.Criteria == nil || item.Criteria.MatchCount != 1 || item.Criteria.PlaybackOrder != "title" ||
		item.Criteria.Amount.Mode != "count" || item.Criteria.Amount.Count != 2 || item.CatalogItemID != "" {
		t.Fatalf("add_block_item = %+v / %+v", item, item.Criteria)
	}

	// Rejections: amount is required, all matches must be explicit, series
	// orders and a legacy query are invalid, a catalog id does not mix in.
	bad := []map[string]any{
		{"block_id": block.ID, "criteria": map[string]any{"query": comedy}},
		{"block_id": block.ID, "criteria": map[string]any{"query": comedy, "amount": map[string]any{}}},
		{"block_id": block.ID, "criteria": map[string]any{"query": comedy, "amount": map[string]any{"mode": "count"}}},
		{"block_id": block.ID, "criteria": map[string]any{"query": comedy, "amount": map[string]any{"mode": "slot_end"}}},
		{"block_id": block.ID, "criteria": map[string]any{"query": comedy, "playback_order": "original", "amount": map[string]any{"mode": "all"}}},
		{"block_id": block.ID, "criteria": map[string]any{"query": map[string]any{"kind": "movie", "genres": []string{"Comedy"}}, "amount": map[string]any{"mode": "all"}}},
		{"block_id": block.ID, "catalog_item_id": "x", "criteria": map[string]any{"query": comedy, "amount": map[string]any{"mode": "all"}}},
		{"block_id": block.ID, "trim_duration_ms": 5, "criteria": map[string]any{"query": comedy, "amount": map[string]any{"mode": "all"}}},
	}
	for i, args := range bad {
		callToolExpectError(t, ctx, session, "add_block_item", args)
		_ = i
	}

	upd := callTool[domain.BlockItem](t, ctx, session, "update_block_item", map[string]any{
		"block_id": block.ID, "item_id": item.ID,
		"amount": map[string]any{"mode": "all"}, "playback_order": "shuffle",
		"query": map[string]any{"version": 1, "all": []any{map[string]any{"field": "genre", "op": "equals", "value": "Comedy"}}},
	})
	if upd.Criteria == nil || upd.Criteria.Amount.Mode != "all" || upd.Criteria.PlaybackOrder != "shuffle" || upd.Criteria.MatchCount != 2 {
		t.Fatalf("update_block_item = %+v", upd.Criteria)
	}
	for _, args := range []map[string]any{
		{"block_id": block.ID, "item_id": item.ID, "playback_order": "original"},
		{"block_id": block.ID, "item_id": item.ID, "trim_duration_ms": 5},
		{"block_id": block.ID, "item_id": item.ID, "query": map[string]any{"version": 1, "all": []any{}}},
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
