package mcpserver_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// TestMCPInterleavedOrder drives the interleaved order over real MCP: dynamic
// blocks through create_block/update_block and criteria items through
// add_block_item/update_block_item, with their validation.
func TestMCPInterleavedOrder(t *testing.T) {
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

	query := map[string]any{"version": 1, "all": []any{map[string]any{"field": "kind", "op": "equals", "value": "episode"}}}

	col := callTool[domain.Collection](t, ctx, session, "create_collection", map[string]any{"name": "Episodes", "query": query})

	// A dynamic block: default is title, interleaved takes parameters.
	plain := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Plain", "collection_id": col.ID})
	if plain.CollectionID == nil || !plain.Dynamic || plain.PlaybackOrder != "title" || plain.Interleave != nil {
		t.Fatalf("plain dynamic block = %+v", plain)
	}
	block := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{
		"name": "Cartoons", "collection_id": col.ID, "playback_order": "interleaved",
		"interleave": map[string]any{"per_round": 2, "on_exhaustion": "drop", "series_order": "release_date"},
	})
	if block.PlaybackOrder != "interleaved" || block.Interleave == nil || *block.Interleave != (domain.Interleave{PerRound: 2, OnExhaustion: "drop", SeriesOrder: "release_date"}) {
		t.Fatalf("interleaved block = %+v / %+v", block, block.Interleave)
	}
	// Defaults.
	def := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Defaults", "collection_id": col.ID, "playback_order": "interleaved"})
	if *def.Interleave != (domain.Interleave{PerRound: 1, OnExhaustion: "restart", SeriesOrder: "title"}) {
		t.Fatalf("defaults = %+v", def.Interleave)
	}
	// A manual block stays what create_block always made.
	manual := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Manual"})
	if manual.CollectionID != nil || manual.Dynamic || manual.PlaybackOrder != "title" {
		t.Fatalf("manual = %+v", manual)
	}

	for i, args := range []map[string]any{
		{"name": "x", "playback_order": "interleaved"}, // manual block
		{"name": "x", "collection_id": col.ID, "playback_order": "shuffle"},
		{"name": "x", "collection_id": "nope"},
		{"name": "x", "collection_id": col.ID, "interleave": map[string]any{"per_round": 2}},
		{"name": "x", "collection_id": col.ID, "playback_order": "interleaved", "interleave": map[string]any{"per_round": 0}},
		{"name": "x", "collection_id": col.ID, "playback_order": "interleaved", "interleave": map[string]any{"on_exhaustion": "loop"}},
		{"name": "x", "collection_id": col.ID, "playback_order": "interleaved", "interleave": map[string]any{"series_order": "original"}},
		{"name": "x", "criteria": query}, // the removed inline query
	} {
		if msg := callToolExpectError(t, ctx, session, "create_block", args); msg == "" {
			t.Errorf("create_block case %d: empty error", i)
		}
	}

	// update_block is a full replace, like PUT /blocks/{id}.
	upd := callTool[domain.Block](t, ctx, session, "update_block", map[string]any{
		"block_id": block.ID, "name": "Cartoons", "collection_id": col.ID, "playback_order": "interleaved",
		"interleave": map[string]any{"per_round": 1},
	})
	if upd.Interleave == nil || upd.Interleave.PerRound != 1 || upd.Interleave.OnExhaustion != "restart" {
		t.Fatalf("update_block = %+v", upd.Interleave)
	}
	back := callTool[domain.Block](t, ctx, session, "update_block", map[string]any{"block_id": block.ID, "name": "Cartoons", "collection_id": col.ID})
	if back.PlaybackOrder != "title" || back.Interleave != nil {
		t.Fatalf("back to title = %+v", back)
	}
	callToolExpectError(t, ctx, session, "update_block", map[string]any{"block_id": "nope", "name": "x"})
	callToolExpectError(t, ctx, session, "update_block", map[string]any{"block_id": block.ID, "name": "x", "playback_order": "interleaved"})

	// Criteria items of a manual block.
	item := callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{
		"block_id": manual.ID,
		"criteria": map[string]any{
			"collection_id": col.ID, "playback_order": "interleaved", "amount": map[string]any{"mode": "all"},
			"interleave": map[string]any{"per_round": 3, "on_exhaustion": "drop"},
		},
	})
	if item.Criteria == nil || item.Criteria.PlaybackOrder != "interleaved" || item.Criteria.Interleave == nil ||
		item.Criteria.Interleave.PerRound != 3 || item.Criteria.Interleave.OnExhaustion != "drop" || item.Criteria.Interleave.SeriesOrder != "title" {
		t.Fatalf("add_block_item = %+v", item.Criteria)
	}
	for _, args := range []map[string]any{
		{"block_id": manual.ID, "criteria": map[string]any{"collection_id": col.ID, "playback_order": "interleaved", "amount": map[string]any{"mode": "all"}, "interleave": map[string]any{"per_round": 0}}},
		{"block_id": manual.ID, "criteria": map[string]any{"collection_id": col.ID, "playback_order": "title", "amount": map[string]any{"mode": "all"}, "interleave": map[string]any{"per_round": 2}}},
		{"block_id": manual.ID, "series": map[string]any{"media_source_id": "x", "series_title": "y", "playback_order": "interleaved"}},
	} {
		callToolExpectError(t, ctx, session, "add_block_item", args)
	}
	got := callTool[domain.BlockItem](t, ctx, session, "update_block_item", map[string]any{
		"block_id": manual.ID, "item_id": item.ID, "interleave": map[string]any{"per_round": 5},
	})
	if got.Criteria.Interleave.PerRound != 5 || got.Criteria.Interleave.OnExhaustion != "drop" {
		t.Fatalf("update_block_item = %+v", got.Criteria.Interleave)
	}
	if msg := callToolExpectError(t, ctx, session, "update_block_item", map[string]any{
		"block_id": manual.ID, "item_id": item.ID, "interleave": map[string]any{"series_order": "x"},
	}); !strings.Contains(msg, "series_order") {
		t.Errorf("error = %q", msg)
	}
	title := callTool[domain.BlockItem](t, ctx, session, "update_block_item", map[string]any{"block_id": manual.ID, "item_id": item.ID, "playback_order": "title"})
	if title.Criteria.Interleave != nil {
		t.Fatalf("title item kept interleave: %+v", title.Criteria)
	}
}
