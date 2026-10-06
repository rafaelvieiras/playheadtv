package mcpserver_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// TestMCPSeriesBlockItems drives the series block item surface over real MCP:
// search_series, add_block_item with a series, list_block_items, the order
// change and trim rules of update_block_item, and delete_block_item.
func TestMCPSeriesBlockItems(t *testing.T) {
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
			{"external_id": "m1", "kind": "movie", "title": "A Movie", "duration_ms": 60_000},
			// Accented series title is deliberate test data for accent-insensitive search.
			{"external_id": "e1", "kind": "episode", "title": "One", "series_title": "Série Teste", "duration_ms": 60_000, "season_number": 1, "episode_number": 1},
			{"external_id": "e2", "kind": "episode", "title": "Two", "series_title": "Série Teste", "duration_ms": 120_000, "season_number": 1, "episode_number": 2},
		},
	})

	type seriesOut struct {
		Series []struct {
			MediaSourceID   string `json:"media_source_id"`
			SeriesTitle     string `json:"series_title"`
			EpisodeCount    int    `json:"episode_count"`
			TotalDurationMs int64  `json:"total_duration_ms"`
		} `json:"series"`
	}
	found := callTool[seriesOut](t, ctx, session, "search_series", map[string]any{"query": "SERIE"})
	if len(found.Series) != 1 || found.Series[0].SeriesTitle != "Série Teste" || found.Series[0].EpisodeCount != 2 || found.Series[0].TotalDurationMs != 180_000 || found.Series[0].MediaSourceID != source.ID {
		t.Fatalf("search_series = %+v", found.Series)
	}
	if none := callTool[seriesOut](t, ctx, session, "search_series", map[string]any{"query": "movie"}); len(none.Series) != 0 {
		t.Errorf("search_series matched a movie: %+v", none.Series)
	}

	// The REST API creates blocks; MCP's create_block exists too.
	block := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Series block"})
	item := callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{
		"block_id": block.ID,
		"series":   map[string]any{"media_source_id": source.ID, "series_title": "Série Teste", "playback_order": "shuffle"},
	})
	if item.ItemType != "series" || item.Series == nil || item.Series.PlaybackOrder != "shuffle" || item.Series.EpisodeCount != 2 || item.Series.TotalDurationMs != 180_000 || item.CatalogItemID != "" {
		t.Fatalf("add_block_item(series) = %+v / %+v", item, item.Series)
	}

	if item.Series.Amount.Mode != "count" || item.Series.Amount.Count != 1 {
		t.Fatalf("default amount = %+v, want one episode", item.Series.Amount)
	}
	counted := callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{
		"block_id": block.ID,
		"series":   map[string]any{"media_source_id": source.ID, "series_title": "Série Teste", "amount": map[string]any{"mode": "count", "count": 3}},
	})
	if counted.Series.Amount.Mode != "count" || counted.Series.Amount.Count != 3 {
		t.Fatalf("add_block_item(amount count 3) = %+v", counted.Series.Amount)
	}
	toEnd := callTool[domain.BlockItem](t, ctx, session, "update_block_item", map[string]any{
		"block_id": block.ID, "item_id": counted.ID, "amount": map[string]any{"mode": "series_end"},
	})
	if toEnd.Series.Amount.Mode != "series_end" || toEnd.Series.Amount.Count != 0 {
		t.Fatalf("update_block_item(amount) = %+v", toEnd.Series.Amount)
	}
	for name, args := range map[string]map[string]any{
		"add zero count":   {"block_id": block.ID, "series": map[string]any{"media_source_id": source.ID, "series_title": "Série Teste", "amount": map[string]any{"mode": "count", "count": 0}}},
		"add unknown mode": {"block_id": block.ID, "series": map[string]any{"media_source_id": source.ID, "series_title": "Série Teste", "amount": map[string]any{"mode": "x"}}},
		"add count w/ end": {"block_id": block.ID, "series": map[string]any{"media_source_id": source.ID, "series_title": "Série Teste", "amount": map[string]any{"mode": "slot_end", "count": 2}}},
	} {
		if msg := callToolExpectError(t, ctx, session, "add_block_item", args); msg == "" {
			t.Errorf("%s: empty error", name)
		}
	}
	callToolExpectError(t, ctx, session, "update_block_item", map[string]any{
		"block_id": block.ID, "item_id": counted.ID, "amount": map[string]any{"mode": "slot_end", "count": 2},
	})
	callToolExpectError(t, ctx, session, "update_block_item", map[string]any{
		"block_id": block.ID, "item_id": counted.ID, "amount": map[string]any{"mode": "count", "count": -1},
	})
	callTool[struct {
		Deleted bool `json:"deleted"`
	}](t, ctx, session, "delete_block_item", map[string]any{"block_id": block.ID, "item_id": counted.ID, "confirm": true})

	listed := callTool[struct {
		Items []domain.BlockItem `json:"items"`
	}](t, ctx, session, "list_block_items", map[string]any{"block_id": block.ID})
	if len(listed.Items) != 1 || listed.Items[0].Series == nil || listed.Items[0].Series.EpisodeCount != 2 {
		t.Fatalf("list_block_items = %+v", listed.Items)
	}

	changed := callTool[domain.BlockItem](t, ctx, session, "update_block_item", map[string]any{
		"block_id": block.ID, "item_id": item.ID, "playback_order": "release_date",
	})
	if changed.Series == nil || changed.Series.PlaybackOrder != "release_date" {
		t.Fatalf("update_block_item(playback_order) = %+v", changed.Series)
	}

	for name, args := range map[string]map[string]any{
		"trim on series":    {"block_id": block.ID, "item_id": item.ID, "trim_duration_ms": 1000},
		"bad order":         {"block_id": block.ID, "item_id": item.ID, "playback_order": "x"},
		"nothing to change": {"block_id": block.ID, "item_id": item.ID},
		"unknown item":      {"block_id": block.ID, "item_id": "nope", "playback_order": "shuffle"},
	} {
		callToolExpectError(t, ctx, session, "update_block_item", args)
		_ = name
	}

	for name, args := range map[string]map[string]any{
		"series with trim":    {"block_id": block.ID, "trim_duration_ms": 5, "series": map[string]any{"media_source_id": source.ID, "series_title": "Série Teste"}},
		"series with catalog": {"block_id": block.ID, "catalog_item_id": "x", "series": map[string]any{"media_source_id": source.ID, "series_title": "Série Teste"}},
		"unknown series":      {"block_id": block.ID, "series": map[string]any{"media_source_id": source.ID, "series_title": "Nope"}},
		"bad order":           {"block_id": block.ID, "series": map[string]any{"media_source_id": source.ID, "series_title": "Série Teste", "playback_order": "x"}},
		"neither":             {"block_id": block.ID},
	} {
		if msg := callToolExpectError(t, ctx, session, "add_block_item", args); msg == "" {
			t.Errorf("%s: empty error", name)
		}
	}
	callToolExpectError(t, ctx, session, "update_block_item", map[string]any{
		"block_id": block.ID, "item_id": item.ID, "position": 5,
	})

	out := callTool[struct {
		Deleted bool `json:"deleted"`
	}](t, ctx, session, "delete_block_item", map[string]any{"block_id": block.ID, "item_id": item.ID, "confirm": true})
	if !out.Deleted {
		t.Error("delete_block_item did not report deleted")
	}
	after := callTool[struct {
		Items []domain.BlockItem `json:"items"`
	}](t, ctx, session, "list_block_items", map[string]any{"block_id": block.ID})
	if len(after.Items) != 0 {
		t.Errorf("items after delete = %+v", after.Items)
	}
	callToolExpectError(t, ctx, session, "delete_block_item", map[string]any{"block_id": block.ID, "item_id": item.ID, "confirm": true})
}
