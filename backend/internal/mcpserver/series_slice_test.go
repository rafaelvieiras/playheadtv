package mcpserver_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// TestMCPSeriesSlice drives the slice surface over real MCP:
// preview_series_slice, add_block_item with a slice, update_block_item with a
// slice, and the validation of contradictory slices.
func TestMCPSeriesSlice(t *testing.T) {
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
	var items []map[string]any
	items = append(items, map[string]any{"external_id": "sp", "kind": "episode", "title": "Special", "series_title": "Sliceable", "duration_ms": 60_000, "season_number": 0, "episode_number": 1})
	for s := 1; s <= 2; s++ {
		for e := 1; e <= 4; e++ {
			items = append(items, map[string]any{
				"external_id": "e" + string(rune('0'+s)) + string(rune('0'+e)), "kind": "episode", "title": "Ep",
				"series_title": "Sliceable", "duration_ms": 60_000, "season_number": s, "episode_number": e,
			})
		}
	}
	doJSON(t, ts, "POST", "/media-sources/"+source.ID+"/catalog-sync", token, map[string]any{"items": items})

	type preview struct {
		EpisodeCount       int   `json:"episode_count"`
		TotalDurationMs    int64 `json:"total_duration_ms"`
		SeriesEpisodeCount int   `json:"series_episode_count"`
	}
	p := callTool[preview](t, ctx, session, "preview_series_slice", map[string]any{
		"media_source_id": source.ID, "series_title": "Sliceable",
		"slice": map[string]any{"exclude_specials": true, "season_from": 1, "season_to": 1, "skip_first": 1},
	})
	if p.EpisodeCount != 3 || p.TotalDurationMs != 180_000 || p.SeriesEpisodeCount != 9 {
		t.Fatalf("preview = %+v", p)
	}
	if whole := callTool[preview](t, ctx, session, "preview_series_slice", map[string]any{"media_source_id": source.ID, "series_title": "Sliceable"}); whole.EpisodeCount != 9 {
		t.Errorf("preview without slice = %+v", whole)
	}
	if msg := callToolExpectError(t, ctx, session, "preview_series_slice", map[string]any{
		"media_source_id": source.ID, "series_title": "Sliceable", "slice": map[string]any{"first": 1, "last": 1},
	}); !strings.Contains(msg, "first and last") {
		t.Errorf("contradictory preview error = %q", msg)
	}

	block := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Sliced"})
	item := callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{
		"block_id": block.ID,
		"series": map[string]any{
			"media_source_id": source.ID, "series_title": "Sliceable",
			"slice": map[string]any{"seasons": []int{2}, "last": 3},
		},
	})
	if item.Series == nil || item.Series.EpisodeCount != 3 || item.Series.Slice == nil || item.Series.Slice.Seasons[0] != 2 || *item.Series.Slice.Last != 3 {
		t.Fatalf("add_block_item(slice) = %+v / %+v", item, item.Series)
	}
	for name, slice := range map[string]map[string]any{
		"contradictory": {"exclude_specials": true, "seasons": []int{0}},
		"empty result":  {"seasons": []int{9}},
	} {
		callToolExpectError(t, ctx, session, "add_block_item", map[string]any{
			"block_id": block.ID,
			"series":   map[string]any{"media_source_id": source.ID, "series_title": "Sliceable", "slice": slice},
		})
		_ = name
	}

	updated := callTool[domain.BlockItem](t, ctx, session, "update_block_item", map[string]any{
		"block_id": block.ID, "item_id": item.ID, "slice": map[string]any{"exclude_specials": true},
	})
	if updated.Series.EpisodeCount != 8 || updated.Series.Slice == nil || !updated.Series.Slice.ExcludeSpecials {
		t.Fatalf("update_block_item(slice) = %+v", updated.Series)
	}
	cleared := callTool[domain.BlockItem](t, ctx, session, "update_block_item", map[string]any{
		"block_id": block.ID, "item_id": item.ID, "slice": map[string]any{},
	})
	if cleared.Series.EpisodeCount != 9 || cleared.Series.Slice != nil {
		t.Fatalf("clearing the slice = %+v", cleared.Series)
	}
	callToolExpectError(t, ctx, session, "update_block_item", map[string]any{
		"block_id": block.ID, "item_id": item.ID, "slice": map[string]any{"first": 1, "last": 1},
	})
}
