package mcpserver_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// The watermark tools set, read and clear a channel's and a block's watermark
// through the same REST handlers the web UI uses (validation included).
func TestMCPWatermarkTools(t *testing.T) {
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
	blk := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "B"})

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89}
	got := callTool[domain.Watermark](t, ctx, session, "set_channel_watermark", map[string]any{
		"id": ch.ID, "mode": "override", "position": "top_right", "opacity_pct": 55, "size_pct": 9,
		"image_base64": base64.StdEncoding.EncodeToString(png),
	})
	if got.Mode != domain.WatermarkOverride || got.ImageID == "" || got.ContentType != "image/png" || got.Position != "top_right" || got.OpacityPct != 55 || got.SizePct != 9 {
		t.Fatalf("set_channel_watermark: %+v", got)
	}
	read := callTool[domain.Watermark](t, ctx, session, "get_channel_watermark", map[string]any{"id": ch.ID})
	if read.ImageID != got.ImageID {
		t.Fatalf("get_channel_watermark: %+v", read)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "set_block_watermark", Arguments: map[string]any{
		"id": blk.ID, "mode": "override",
		"image_base64": base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)),
	}})
	if err == nil && !res.IsError {
		t.Fatal("an SVG was accepted as a block watermark")
	}
	b := callTool[domain.Watermark](t, ctx, session, "set_block_watermark", map[string]any{"id": blk.ID, "mode": "disable"})
	if b.Mode != domain.WatermarkDisable {
		t.Fatalf("set_block_watermark: %+v", b)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "clear_channel_watermark", Arguments: map[string]any{"id": ch.ID}})
	if err == nil && !res.IsError {
		t.Fatal("clear without confirm was accepted")
	}
	callTool[map[string]any](t, ctx, session, "clear_channel_watermark", map[string]any{"id": ch.ID, "confirm": true})
	read = callTool[domain.Watermark](t, ctx, session, "get_channel_watermark", map[string]any{"id": ch.ID})
	if read.ImageID != "" || read.Mode != domain.WatermarkDisable {
		t.Fatalf("after clear: %+v", read)
	}
}
