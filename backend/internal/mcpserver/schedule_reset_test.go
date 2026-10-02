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
