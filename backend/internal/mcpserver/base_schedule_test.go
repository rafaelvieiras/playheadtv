package mcpserver_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// assign_channel_base_schedule and update_channel both carry the base
// schedule, and neither lets it be the channel's pinned schedule.
func TestMCPChannelBaseSchedule(t *testing.T) {
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

	ch := callTool[domain.Channel](t, ctx, session, "create_channel", map[string]any{"number": 1, "name": "C1", "timezone": "UTC"})
	pinned := callTool[domain.Schedule](t, ctx, session, "create_schedule", map[string]any{"name": "Pinned"})
	base := callTool[domain.Schedule](t, ctx, session, "create_schedule", map[string]any{"name": "Base"})
	callTool[domain.Channel](t, ctx, session, "assign_channel_schedule", map[string]any{"channel_id": ch.ID, "schedule_id": pinned.ID})

	got := callTool[domain.Channel](t, ctx, session, "assign_channel_base_schedule", map[string]any{"channel_id": ch.ID, "base_schedule_id": base.ID})
	if got.BaseScheduleID == nil || *got.BaseScheduleID != base.ID || got.ScheduleID == nil || *got.ScheduleID != pinned.ID {
		t.Fatalf("assign_channel_base_schedule: %+v", got)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "assign_channel_base_schedule", Arguments: map[string]any{"channel_id": ch.ID, "base_schedule_id": pinned.ID}})
	if err == nil && !res.IsError {
		t.Fatal("the pinned schedule was accepted as the base")
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "assign_channel_schedule", Arguments: map[string]any{"channel_id": ch.ID, "schedule_id": base.ID}})
	if err == nil && !res.IsError {
		t.Fatal("the base schedule was accepted as the pinned one")
	}

	// update_channel is a full replace and carries the field too.
	got = callTool[domain.Channel](t, ctx, session, "update_channel", map[string]any{
		"id": ch.ID, "number": 1, "name": "C1", "schedule_id": pinned.ID, "base_schedule_id": base.ID,
	})
	if got.BaseScheduleID == nil || *got.BaseScheduleID != base.ID {
		t.Fatalf("update_channel: %+v", got)
	}
	got = callTool[domain.Channel](t, ctx, session, "assign_channel_base_schedule", map[string]any{"channel_id": ch.ID})
	if got.BaseScheduleID != nil {
		t.Fatalf("clearing the base: %+v", got)
	}
}
