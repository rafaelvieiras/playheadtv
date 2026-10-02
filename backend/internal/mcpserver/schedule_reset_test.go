package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// The MCP schedule tools reset the affected channels' grid synchronously,
// like the REST handlers: no manual reset, no waiting for the background run.
func TestMCPScheduleEditsResetGrid(t *testing.T) {
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

	src := callTool[domain.MediaSource](t, ctx, session, "register_media_source", map[string]any{"type": "local_folder", "name": "Local"})
	doJSON(t, ts, "POST", "/media-sources/"+src.ID+"/catalog-sync", token, map[string]any{
		"items": []map[string]any{{"external_id": "e1", "kind": "movie", "title": "Rotation Movie", "duration_ms": 3_600_000}},
	})
	items := callTool[struct {
		Items []domain.CatalogItem `json:"items"`
	}](t, ctx, session, "search_catalog", map[string]any{"query": "Rotation"})
	if len(items.Items) != 1 {
		t.Fatalf("search_catalog: %+v", items.Items)
	}
	block := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Rot"})
	callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{"block_id": block.ID, "catalog_item_id": items.Items[0].ID})

	ch := callTool[domain.Channel](t, ctx, session, "create_channel", map[string]any{"number": 1, "name": "C1", "timezone": "UTC"})
	sched := callTool[domain.Schedule](t, ctx, session, "create_schedule", map[string]any{"name": "S"})
	callTool[domain.Channel](t, ctx, session, "assign_channel_schedule", map[string]any{"channel_id": ch.ID, "schedule_id": sched.ID})

	epg := func() []domain.EpgEntry {
		var out []domain.EpgEntry
		now := time.Now().UnixMilli()
		body := doJSON(t, ts, "GET", "/epg?channel_id="+ch.ID+"&start="+itoa(now)+"&end="+itoa(now+3_600_000), token, nil)
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode epg: %v", err)
		}
		return out
	}
	if n := len(epg()); n != 0 {
		t.Fatalf("empty schedule has %d entries", n)
	}

	callTool[domain.ScheduleItem](t, ctx, session, "create_schedule_item", map[string]any{
		"schedule_id": sched.ID, "block_id": block.ID, "recurrence_type": "continuous",
	})
	got := epg()
	if len(got) == 0 {
		t.Fatal("create_schedule_item did not regenerate the grid synchronously")
	}
	if got[0].StartAt > time.Now().UnixMilli()+1000 {
		t.Errorf("hole right after the edit: first program starts %v", time.UnixMilli(got[0].StartAt))
	}

	// Clearing the assignment through MCP resets the channel as well.
	callTool[domain.Channel](t, ctx, session, "assign_channel_schedule", map[string]any{"channel_id": ch.ID})
	if n := len(epg()); n != 0 {
		t.Fatalf("clearing the schedule left %d entries", n)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestMCPAuthoringTools(t *testing.T) {
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

	src := callTool[domain.MediaSource](t, ctx, session, "register_media_source", map[string]any{"type": "local_folder", "name": "Local"})
	doJSON(t, ts, "POST", "/media-sources/"+src.ID+"/catalog-sync", token, map[string]any{
		"items": []map[string]any{
			{"external_id": "e1", "kind": "movie", "title": "One", "duration_ms": 3_600_000},
			{"external_id": "e2", "kind": "movie", "title": "Two", "duration_ms": 1_800_000},
		},
	})
	found := callTool[struct {
		Items []domain.CatalogItem `json:"items"`
	}](t, ctx, session, "search_catalog", map[string]any{})
	if len(found.Items) != 2 {
		t.Fatalf("search_catalog: %+v", found.Items)
	}

	prev := callTool[struct {
		MatchCount      int   `json:"match_count"`
		TotalDurationMs int64 `json:"total_duration_ms"`
	}](t, ctx, session, "preview_criteria", map[string]any{"query": map[string]any{
		"version": 1,
		"all": []any{
			map[string]any{"field": "kind", "op": "equals", "value": "movie"},
			map[string]any{"field": "duration", "op": "gte", "value": 30},
		},
	}})
	if prev.MatchCount != 2 || prev.TotalDurationMs != 5_400_000 {
		t.Errorf("preview_criteria = %+v", prev)
	}
	// The legacy shape and an invalid tree are rejected.
	callToolExpectError(t, ctx, session, "preview_criteria", map[string]any{"query": map[string]any{"kind": "movie", "genres": []string{"x"}}})
	callToolExpectError(t, ctx, session, "preview_criteria", map[string]any{"query": map[string]any{"version": 1, "all": []any{}}})

	block := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "B"})
	bi1 := callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{"block_id": block.ID, "catalog_item_id": found.Items[0].ID})
	bi2 := callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{"block_id": block.ID, "catalog_item_id": found.Items[1].ID})
	moved := callTool[domain.BlockItem](t, ctx, session, "update_block_item", map[string]any{"block_id": block.ID, "item_id": bi2.ID, "position": 0, "trim_duration_ms": 1000})
	if moved.Position != 0 || moved.TrimDurationMs == nil || *moved.TrimDurationMs != 1000 {
		t.Errorf("update_block_item = %+v", moved)
	}
	listed := callTool[struct {
		Items []domain.BlockItem `json:"items"`
	}](t, ctx, session, "list_block_items", map[string]any{"block_id": block.ID})
	if len(listed.Items) != 2 || listed.Items[0].ID != bi2.ID || listed.Items[1].ID != bi1.ID || listed.Items[1].Position != 1 {
		t.Errorf("order after update: %+v", listed.Items)
	}
	callToolExpectError(t, ctx, session, "update_block_item", map[string]any{"block_id": block.ID, "item_id": bi1.ID, "position": 5})

	pool := callTool[domain.FillerPool](t, ctx, session, "create_filler_pool", map[string]any{"name": "P"})
	pi := callTool[domain.FillerPoolItem](t, ctx, session, "add_filler_pool_item", map[string]any{"filler_pool_id": pool.ID, "catalog_item_id": found.Items[0].ID})
	upd := callTool[domain.FillerPoolItem](t, ctx, session, "update_filler_pool_item", map[string]any{"filler_pool_id": pool.ID, "item_id": pi.ID, "weight": 4})
	if upd.Weight != 4 {
		t.Errorf("update_filler_pool_item = %+v", upd)
	}
	callToolExpectError(t, ctx, session, "update_filler_pool_item", map[string]any{"filler_pool_id": pool.ID, "item_id": pi.ID, "weight": 0})

	ch := callTool[domain.Channel](t, ctx, session, "create_channel", map[string]any{"number": 1, "name": "C1", "timezone": "UTC"})
	grid := callTool[struct {
		Entries []domain.EpgEntry `json:"entries"`
	}](t, ctx, session, "preview_channel_grid_reset", map[string]any{"channel_id": ch.ID})
	if len(grid.Entries) != 0 {
		t.Errorf("channel without schedule/filler previewed %d entries", len(grid.Entries))
	}
	callToolExpectError(t, ctx, session, "preview_channel_grid_reset", map[string]any{"channel_id": "nope"})
}
