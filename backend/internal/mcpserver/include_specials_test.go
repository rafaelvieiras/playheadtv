package mcpserver_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// create_block and update_block carry include_specials, with the same rules as
// POST/PUT /blocks: off by default, kept when an update omits it.
func TestMCPBlockIncludeSpecials(t *testing.T) {
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

	if def := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Default"}); def.IncludeSpecials {
		t.Error("default include_specials is on, want off")
	}
	on := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "On", "include_specials": true})
	if !on.IncludeSpecials {
		t.Fatal("created with include_specials true, read back off")
	}
	kept := callTool[domain.Block](t, ctx, session, "update_block", map[string]any{"block_id": on.ID, "name": "Renamed"})
	if !kept.IncludeSpecials {
		t.Error("update without include_specials turned it off")
	}
	off := callTool[domain.Block](t, ctx, session, "update_block", map[string]any{"block_id": on.ID, "name": "Renamed", "include_specials": false})
	if off.IncludeSpecials {
		t.Error("update with include_specials false left it on")
	}
}
