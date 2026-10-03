package mcpserver_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// create_block and update_block carry list_mode (manual blocks only), with the
// same validation as POST/PUT /blocks.
func TestMCPBlockListMode(t *testing.T) {
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

	def := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Default"})
	if def.ListMode != domain.ListModeRepeat {
		t.Errorf("default list_mode = %q, want repeat", def.ListMode)
	}
	once := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Once", "list_mode": "once"})
	if once.ListMode != domain.ListModeOnce {
		t.Errorf("created list_mode = %q, want once", once.ListMode)
	}
	if msg := callToolExpectError(t, ctx, session, "create_block", map[string]any{"name": "Bad", "list_mode": "loop"}); !strings.Contains(msg, "list_mode") {
		t.Errorf("unknown mode error = %q", msg)
	}

	// Omitting list_mode on update keeps the stored mode; setting it changes it.
	kept := callTool[domain.Block](t, ctx, session, "update_block", map[string]any{"block_id": once.ID, "name": "Renamed"})
	if kept.ListMode != domain.ListModeOnce {
		t.Errorf("after update without list_mode = %q, want once", kept.ListMode)
	}
	changed := callTool[domain.Block](t, ctx, session, "update_block", map[string]any{"block_id": once.ID, "name": "Renamed", "list_mode": "repeat"})
	if changed.ListMode != domain.ListModeRepeat {
		t.Errorf("after update to repeat = %q", changed.ListMode)
	}

	// A dynamic block takes none.
	col := callTool[domain.Collection](t, ctx, session, "create_collection", map[string]any{
		"name":  "Movies",
		"query": map[string]any{"version": 1, "all": []map[string]any{{"field": "kind", "op": "equals", "value": "movie"}}},
	})
	if msg := callToolExpectError(t, ctx, session, "create_block", map[string]any{"name": "Dyn", "collection_id": col.ID, "list_mode": "once"}); !strings.Contains(msg, "manual") {
		t.Errorf("list_mode on a dynamic block error = %q", msg)
	}
	dyn := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "Dyn", "collection_id": col.ID})
	if dyn.ListMode != "" {
		t.Errorf("a dynamic block serves list_mode %q, want none", dyn.ListMode)
	}
}
