package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/api"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/mcpserver"
)

func connectMCP(t *testing.T) (*httptest.Server, string, *mcp.ClientSession) {
	t.Helper()
	ts := newHarness(t)
	token := bootstrapSession(t, ts)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: &bearerTransport{token: token}},
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return ts, token, session
}

// The web UI can do nothing the MCP cannot. The route list comes from the
// code that registers the routes (api.Routes), so a new route without a tool
// (or a justified entry in NoMCPCounterpart) fails here.
func TestEveryRESTRouteHasAnMCPTool(t *testing.T) {
	_, _, session := connectMCP(t)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		registered[tl.Name] = tl
	}

	routes := map[string]bool{}
	for _, r := range api.Routes() {
		routes[r] = true
		tool, mapped := mcpserver.RouteTools[r]
		_, allowed := mcpserver.NoMCPCounterpart[r]
		switch {
		case mapped && allowed:
			t.Errorf("route %q is both mapped to a tool and in the no-counterpart list", r)
		case !mapped && !allowed:
			t.Errorf("REST route %q has no MCP tool: add one and map it in mcpserver.RouteTools, or justify it in NoMCPCounterpart", r)
		case mapped && registered[tool] == nil:
			t.Errorf("route %q is mapped to %q, which is not a registered tool", r, tool)
		}
	}
	// Stale entries: a mapping for a route that no longer exists.
	var stale []string
	for r := range mcpserver.RouteTools {
		if !routes[r] {
			stale = append(stale, r)
		}
	}
	for r := range mcpserver.NoMCPCounterpart {
		if !routes[r] {
			stale = append(stale, r)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("mapped routes that are not registered: %v", stale)
	}
	for r, why := range mcpserver.NoMCPCounterpart {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%q is allow-listed without a reason", r)
		}
	}
}

// Every delete (and the grid reset) declares the confirmation convention in
// its description and schema, and is rejected without confirm=true.
func TestDestructiveToolsRequireConfirm(t *testing.T) {
	_, _, session := connectMCP(t)
	ctx := context.Background()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, tl := range tools.Tools {
		if !strings.HasPrefix(tl.Name, "delete_") && tl.Name != "reset_channel_epg" {
			continue
		}
		checked++
		if !strings.Contains(tl.Description, "confirm=true") {
			t.Errorf("%s: description does not document confirm=true", tl.Name)
		}
		schema, _ := json.Marshal(tl.InputSchema)
		if !strings.Contains(string(schema), `"confirm"`) {
			t.Errorf("%s: input schema has no confirm property: %s", tl.Name, schema)
		}
		// Without it the call is refused with a clear message, before any
		// lookup, so even a made-up id never reaches the REST layer.
		props, _ := tl.InputSchema.(map[string]any)["properties"].(map[string]any)
		args := map[string]any{}
		for name := range props {
			if name != "confirm" {
				args[name] = "x"
			}
		}
		msg := callToolExpectError(t, ctx, session, tl.Name, args)
		if !strings.Contains(msg, "confirmation required") || !strings.Contains(msg, "confirm=true") {
			t.Errorf("%s without confirm: %q", tl.Name, msg)
		}
	}
	if checked < 14 {
		t.Errorf("only %d destructive tools found", checked)
	}
}

func TestMCPChannelAndScheduleCRUD(t *testing.T) {
	ts, token, session := connectMCP(t)
	ctx := context.Background()

	ch := callTool[domain.Channel](t, ctx, session, "create_channel", map[string]any{"number": 7, "name": "Seven"})
	got := callTool[domain.Channel](t, ctx, session, "get_channel", map[string]any{"id": ch.ID})
	if got.ID != ch.ID || got.Name != "Seven" {
		t.Fatalf("get_channel = %+v", got)
	}

	// Validation errors are the REST handler's own, with its status.
	if msg := callToolExpectError(t, ctx, session, "update_channel", map[string]any{"id": ch.ID, "number": 7, "name": ""}); !strings.Contains(msg, "name is required") || !strings.Contains(msg, "400") {
		t.Errorf("update_channel without name: %q", msg)
	}
	if msg := callToolExpectError(t, ctx, session, "get_channel", map[string]any{"id": "missing"}); !strings.Contains(msg, "404") {
		t.Errorf("get_channel missing: %q", msg)
	}
	if msg := callToolExpectError(t, ctx, session, "get_channel", map[string]any{"id": ""}); !strings.Contains(msg, "id is required") {
		t.Errorf("get_channel without id: %q", msg)
	}

	up := callTool[domain.Channel](t, ctx, session, "update_channel", map[string]any{"id": ch.ID, "number": 8, "name": "Eight", "timezone": "America/Sao_Paulo"})
	if up.Number != 8 || up.Name != "Eight" || up.Timezone != "America/Sao_Paulo" {
		t.Fatalf("update_channel = %+v", up)
	}

	// A schedule item edit regenerates the grid synchronously, like REST.
	src := callTool[domain.MediaSource](t, ctx, session, "register_media_source", map[string]any{"type": "local_folder", "name": "Local"})
	doJSON(t, ts, "POST", "/media-sources/"+src.ID+"/catalog-sync", token, map[string]any{
		"items": []map[string]any{{"external_id": "e1", "kind": "movie", "title": "Movie One", "duration_ms": 3_600_000}},
	})
	items := callTool[struct {
		Items []domain.CatalogItem `json:"items"`
	}](t, ctx, session, "search_catalog", map[string]any{"query": "Movie One"})
	block := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "B"})
	callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{"block_id": block.ID, "catalog_item_id": items.Items[0].ID})

	sched := callTool[domain.Schedule](t, ctx, session, "create_schedule", map[string]any{"name": "S"})
	callTool[domain.Channel](t, ctx, session, "assign_channel_schedule", map[string]any{"channel_id": ch.ID, "schedule_id": sched.ID})
	renamed := callTool[domain.Schedule](t, ctx, session, "update_schedule", map[string]any{"id": sched.ID, "name": "S2", "description": "d"})
	if renamed.Name != "S2" || renamed.Description == nil || *renamed.Description != "d" {
		t.Fatalf("update_schedule = %+v", renamed)
	}
	if l := callTool[struct {
		Items []domain.Schedule `json:"items"`
	}](t, ctx, session, "list_schedules", nil); len(l.Items) != 1 {
		t.Fatalf("list_schedules = %+v", l)
	}

	epgLen := func() int {
		now := time.Now().UnixMilli()
		var out []domain.EpgEntry
		body := doJSON(t, ts, "GET", "/epg?channel_id="+ch.ID+"&start="+itoa(now)+"&end="+itoa(now+3_600_000), token, nil)
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return len(out)
	}
	item := callTool[domain.ScheduleItem](t, ctx, session, "create_schedule_item", map[string]any{
		"schedule_id": sched.ID, "block_id": block.ID, "recurrence_type": "continuous",
	})
	if epgLen() == 0 {
		t.Fatal("grid is empty after create_schedule_item")
	}
	if l := callTool[struct {
		Items []domain.ScheduleItem `json:"items"`
	}](t, ctx, session, "list_schedule_items", map[string]any{"schedule_id": sched.ID}); len(l.Items) != 1 {
		t.Fatalf("list_schedule_items = %+v", l)
	}
	if g := callTool[domain.ScheduleItem](t, ctx, session, "get_schedule_item", map[string]any{"id": item.ID}); g.ID != item.ID {
		t.Fatalf("get_schedule_item = %+v", g)
	}
	// Making the only item inactive empties the grid again (materializer ran).
	upd := callTool[domain.ScheduleItem](t, ctx, session, "update_schedule_item", map[string]any{
		"id": item.ID, "block_id": block.ID, "recurrence_type": "continuous", "active": false,
	})
	if upd.Active {
		t.Fatalf("update_schedule_item = %+v", upd)
	}
	if n := epgLen(); n != 0 {
		t.Fatalf("inactive item left %d entries: update_schedule_item did not reset the grid", n)
	}
	callTool[domain.ScheduleItem](t, ctx, session, "update_schedule_item", map[string]any{
		"id": item.ID, "block_id": block.ID, "recurrence_type": "continuous", "active": true,
	})
	if epgLen() == 0 {
		t.Fatal("re-activating did not regenerate the grid")
	}

	// The grid reset is applied only with confirmation.
	callToolExpectError(t, ctx, session, "reset_channel_epg", map[string]any{"id": ch.ID})
	callTool[map[string]any](t, ctx, session, "reset_channel_epg", map[string]any{"id": ch.ID, "confirm": true})
	if w := callTool[struct {
		Items []map[string]any `json:"items"`
	}](t, ctx, session, "get_channel_epg_warnings", map[string]any{"id": ch.ID}); len(w.Items) != 0 {
		t.Fatalf("warnings = %+v", w)
	}

	// Deleting the item empties the grid; deleting the schedule and channel works.
	callTool[map[string]any](t, ctx, session, "delete_schedule_item", map[string]any{"id": item.ID, "confirm": true})
	if n := epgLen(); n != 0 {
		t.Fatalf("delete_schedule_item left %d entries", n)
	}
	// A schedule still assigned to a channel is refused by the REST layer.
	if msg := callToolExpectError(t, ctx, session, "delete_schedule", map[string]any{"id": sched.ID, "confirm": true}); !strings.Contains(msg, "400") {
		t.Errorf("delete of an assigned schedule: %q", msg)
	}
	callTool[map[string]any](t, ctx, session, "delete_channel", map[string]any{"id": ch.ID, "confirm": true})
	callTool[map[string]any](t, ctx, session, "delete_schedule", map[string]any{"id": sched.ID, "confirm": true})
	callToolExpectError(t, ctx, session, "get_schedule", map[string]any{"id": sched.ID})
	callToolExpectError(t, ctx, session, "get_channel", map[string]any{"id": ch.ID})

	// Blocks and the EPG reads.
	if l := callTool[struct {
		Items []domain.Block `json:"items"`
	}](t, ctx, session, "list_blocks", nil); len(l.Items) != 1 {
		t.Fatalf("list_blocks = %+v", l)
	}
	if b := callTool[domain.Block](t, ctx, session, "get_block", map[string]any{"id": block.ID}); b.ID != block.ID {
		t.Fatalf("get_block = %+v", b)
	}
	callTool[map[string]any](t, ctx, session, "list_block_list_mode_review", nil)
	callTool[map[string]any](t, ctx, session, "delete_block", map[string]any{"id": block.ID, "confirm": true})
	callTool[map[string]any](t, ctx, session, "get_epg_meta", nil)
	callTool[map[string]any](t, ctx, session, "get_epg", map[string]any{"start": 1, "end": 2, "group_tail": true})
}

func TestMCPFillerBreakCutCRUD(t *testing.T) {
	ts, token, session := connectMCP(t)
	ctx := context.Background()

	src := callTool[domain.MediaSource](t, ctx, session, "register_media_source", map[string]any{"type": "local_folder", "name": "Local"})
	doJSON(t, ts, "POST", "/media-sources/"+src.ID+"/catalog-sync", token, map[string]any{
		"items": []map[string]any{{"external_id": "f1", "kind": "other", "title": "Bumper", "duration_ms": 30_000}},
	})
	items := callTool[struct {
		Items []domain.CatalogItem `json:"items"`
	}](t, ctx, session, "search_catalog", map[string]any{"query": "Bumper"})

	pool := callTool[domain.FillerPool](t, ctx, session, "create_filler_pool", map[string]any{"name": "Pool"})
	poolItem := callTool[domain.FillerPoolItem](t, ctx, session, "add_filler_pool_item", map[string]any{"filler_pool_id": pool.ID, "catalog_item_id": items.Items[0].ID, "weight": 1})
	if p := callTool[domain.FillerPool](t, ctx, session, "get_filler_pool", map[string]any{"id": pool.ID}); p.ID != pool.ID {
		t.Fatalf("get_filler_pool = %+v", p)
	}
	if p := callTool[domain.FillerPool](t, ctx, session, "update_filler_pool", map[string]any{"id": pool.ID, "name": "Pool2"}); p.Name != "Pool2" {
		t.Fatalf("update_filler_pool = %+v", p)
	}
	if l := callTool[struct {
		Items []domain.FillerPoolItem `json:"items"`
	}](t, ctx, session, "list_filler_pool_items", map[string]any{"pool_id": pool.ID}); len(l.Items) != 1 {
		t.Fatalf("list_filler_pool_items = %+v", l)
	}

	ch := callTool[domain.Channel](t, ctx, session, "create_channel", map[string]any{"number": 1, "name": "C"})
	rule := callTool[domain.FillerRule](t, ctx, session, "create_filler_rule", map[string]any{
		"scope_type": "channel", "scope_id": ch.ID, "kind": "fallback", "filler_pool_id": pool.ID, "selection_strategy": "sequential",
	})
	if l := callTool[struct {
		Items []domain.FillerRule `json:"items"`
	}](t, ctx, session, "list_filler_rules", nil); len(l.Items) != 1 {
		t.Fatalf("list_filler_rules = %+v", l)
	}
	callTool[domain.FillerRule](t, ctx, session, "get_filler_rule", map[string]any{"id": rule.ID})
	up := callTool[domain.FillerRule](t, ctx, session, "update_filler_rule", map[string]any{
		"id": rule.ID, "scope_type": "channel", "scope_id": ch.ID, "kind": "fallback", "filler_pool_id": pool.ID, "selection_strategy": "random",
	})
	if string(up.Selection) != "random" {
		t.Fatalf("update_filler_rule = %+v", up)
	}
	if msg := callToolExpectError(t, ctx, session, "update_filler_rule", map[string]any{"id": rule.ID, "scope_type": "nope", "scope_id": ch.ID, "kind": "fallback", "filler_pool_id": pool.ID, "selection_strategy": "random"}); !strings.Contains(msg, "400") {
		t.Errorf("update_filler_rule invalid: %q", msg)
	}

	tpl := callTool[map[string]any](t, ctx, session, "create_break_template", map[string]any{
		"scope_type": "channel", "scope_id": ch.ID, "kind": "midroll", "name": "T",
		"steps": []map[string]any{{"step_type": "single", "filler_pool_id": pool.ID, "selection_strategy": "sequential"}},
	})
	tplID, _ := tpl["id"].(string)
	callTool[map[string]any](t, ctx, session, "get_break_template", map[string]any{"id": tplID})
	got := callTool[map[string]any](t, ctx, session, "update_break_template", map[string]any{
		"id": tplID, "scope_type": "channel", "scope_id": ch.ID, "kind": "midroll", "name": "T2",
		"steps": []map[string]any{{"step_type": "single", "filler_pool_id": pool.ID, "selection_strategy": "random"}},
	})
	if got["name"] != "T2" {
		t.Fatalf("update_break_template = %+v", got)
	}
	callToolExpectError(t, ctx, session, "update_break_template", map[string]any{
		"id": tplID, "scope_type": "channel", "scope_id": ch.ID, "kind": "midroll", "steps": []map[string]any{},
	})

	cutRules := []map[string]any{{"name": "r", "match": map[string]any{}, "strategy": map[string]any{"type": "interval", "every_ms": 1_200_000}}}
	set := callTool[domain.CutRuleSet](t, ctx, session, "create_cut_rule_set", map[string]any{
		"scope_type": "channel", "scope_id": ch.ID, "name": "Cuts", "rules": cutRules,
	})
	callTool[domain.CutRuleSet](t, ctx, session, "get_cut_rule_set", map[string]any{"id": set.ID})
	upSet := callTool[domain.CutRuleSet](t, ctx, session, "update_cut_rule_set", map[string]any{
		"id": set.ID, "scope_type": "channel", "scope_id": ch.ID, "name": "Cuts2", "rules": cutRules,
	})
	if upSet.Name != "Cuts2" {
		t.Fatalf("update_cut_rule_set = %+v", upSet)
	}
	callToolExpectError(t, ctx, session, "update_cut_rule_set", map[string]any{"id": set.ID, "scope_type": "bogus"})

	// Deletes, each with confirmation.
	callTool[map[string]any](t, ctx, session, "delete_cut_rule_set", map[string]any{"id": set.ID, "confirm": true})
	callTool[map[string]any](t, ctx, session, "delete_break_template", map[string]any{"id": tplID, "confirm": true})
	callTool[map[string]any](t, ctx, session, "delete_filler_rule", map[string]any{"id": rule.ID, "confirm": true})
	callTool[map[string]any](t, ctx, session, "delete_filler_pool_item", map[string]any{"pool_id": pool.ID, "item_id": poolItem.ID, "confirm": true})
	callTool[map[string]any](t, ctx, session, "delete_filler_pool", map[string]any{"id": pool.ID, "confirm": true})
	callToolExpectError(t, ctx, session, "get_filler_pool", map[string]any{"id": pool.ID})
}

func TestMCPMediaSourcesNeverLeakCredentials(t *testing.T) {
	ts, token, session := connectMCP(t)
	ctx := context.Background()
	const secret = "s3cr3t-api-key-value"

	src := callTool[domain.MediaSource](t, ctx, session, "register_media_source", map[string]any{
		"type": "plex", "name": "Plex", "base_url": "http://plex.example.com", "credential": secret,
	})
	up := callTool[domain.MediaSource](t, ctx, session, "update_media_source", map[string]any{
		"id": src.ID, "type": "plex", "name": "Plex 2", "base_url": "http://plex.example.com", "credential": secret + "-v2",
	})
	if up.Name != "Plex 2" {
		t.Fatalf("update_media_source = %+v", up)
	}
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"list_media_sources", nil},
		{"get_media_source", map[string]any{"id": src.ID}},
		{"update_media_source", map[string]any{"id": src.ID, "type": "plex", "name": "Plex 3", "base_url": "http://plex.example.com"}},
	} {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.tool, Arguments: call.args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %v", call.tool, err, res)
		}
		b, _ := json.Marshal(res)
		for _, leak := range []string{secret, "credential_cipher", "\"credential\""} {
			if strings.Contains(string(b), leak) {
				t.Errorf("%s leaks %q: %s", call.tool, leak, b)
			}
		}
	}
	// Omitting the credential on update keeps the stored one.
	body := doJSON(t, ts, "GET", "/media-sources/"+src.ID, token, nil)
	if strings.Contains(string(body), secret) {
		t.Errorf("REST leaks the credential: %s", body)
	}

	// Validation and the sync tools are the REST ones.
	if msg := callToolExpectError(t, ctx, session, "update_media_source", map[string]any{"id": src.ID, "type": "bogus", "name": "x"}); !strings.Contains(msg, "400") {
		t.Errorf("update_media_source invalid type: %q", msg)
	}
	if msg := callToolExpectError(t, ctx, session, "pull_sync_media_source", map[string]any{"id": src.ID}); !strings.Contains(msg, "409") {
		t.Errorf("pull_sync on a plex source: %q", msg)
	}
	if msg := callToolExpectError(t, ctx, session, "get_media_source", map[string]any{"id": "missing"}); !strings.Contains(msg, "404") {
		t.Errorf("get_media_source missing: %q", msg)
	}

	local := callTool[domain.MediaSource](t, ctx, session, "register_media_source", map[string]any{"type": "local_folder", "name": "Local"})
	callTool[map[string]any](t, ctx, session, "sync_media_source_catalog", map[string]any{
		"id": local.ID, "items": []map[string]any{{"external_id": "e1", "kind": "movie", "title": "Pushed", "duration_ms": 60_000}},
	})
	found := callTool[struct {
		Items []domain.CatalogItem `json:"items"`
	}](t, ctx, session, "search_catalog", map[string]any{"query": "Pushed"})
	if len(found.Items) != 1 {
		t.Fatalf("sync_media_source_catalog did not push the item: %+v", found)
	}
	// A reachable Jellyfin source is in pull mode: catalog-sync is refused.
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) }))
	defer jf.Close()
	pull := callTool[domain.MediaSource](t, ctx, session, "register_media_source", map[string]any{"type": "jellyfin", "name": "JF", "base_url": jf.URL})
	if msg := callToolExpectError(t, ctx, session, "sync_media_source_catalog", map[string]any{"id": pull.ID}); !strings.Contains(msg, "409") {
		t.Errorf("catalog sync on a pull-mode source: %q", msg)
	}

	callToolExpectError(t, ctx, session, "delete_media_source", map[string]any{"id": src.ID})
	callTool[map[string]any](t, ctx, session, "delete_media_source", map[string]any{"id": src.ID, "confirm": true})
	callToolExpectError(t, ctx, session, "get_media_source", map[string]any{"id": src.ID})
}

func TestMCPDevicesAndStreamPreferences(t *testing.T) {
	ts, token, session := connectMCP(t)
	ctx := context.Background()

	created := callTool[struct {
		Device domain.Device `json:"device"`
		Token  string        `json:"token"`
	}](t, ctx, session, "create_device", map[string]any{"name": "Living room", "platform": "linux"})
	if created.Token == "" || created.Device.ID == "" {
		t.Fatalf("create_device = %+v", created)
	}
	callToolExpectError(t, ctx, session, "create_device", map[string]any{"name": "x", "platform": "toaster"})

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_devices"})
	if err != nil || res.IsError {
		t.Fatalf("list_devices: %v %v", err, res)
	}
	b, _ := json.Marshal(res)
	for _, leak := range []string{created.Token, "api_token_hash", "token"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("list_devices leaks %q: %s", leak, b)
		}
	}
	if !strings.Contains(string(b), created.Device.ID) {
		t.Errorf("list_devices does not list the device: %s", b)
	}

	// The device token is a playback credential: it works on the device
	// routes and is refused by the MCP endpoint, like any non-session token.
	req, _ := http.NewRequest("GET", ts.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+created.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("device token on /mcp = %d, want 401", resp.StatusCode)
	}

	callToolExpectError(t, ctx, session, "delete_device", map[string]any{"id": created.Device.ID})
	callTool[map[string]any](t, ctx, session, "delete_device", map[string]any{"id": created.Device.ID, "confirm": true})
	if msg := callToolExpectError(t, ctx, session, "delete_device", map[string]any{"id": created.Device.ID, "confirm": true}); !strings.Contains(msg, "404") {
		t.Errorf("second delete: %q", msg)
	}

	// Stream preferences: own, then global and per-channel config.
	empty := callTool[struct {
		Rules domain.StreamRuleCascade `json:"rules"`
	}](t, ctx, session, "get_my_stream_preference", nil)
	if len(empty.Rules.Steps) != 0 {
		t.Fatalf("fresh preference = %+v", empty)
	}
	rules := map[string]any{"steps": []map[string]any{{"audio_language": "pt"}}}
	set := callTool[struct {
		Rules domain.StreamRuleCascade `json:"rules"`
	}](t, ctx, session, "set_my_stream_preference", map[string]any{"rules": rules})
	if len(set.Rules.Steps) != 1 {
		t.Fatalf("set_my_stream_preference = %+v", set)
	}
	if got := callTool[struct {
		Rules domain.StreamRuleCascade `json:"rules"`
	}](t, ctx, session, "get_my_stream_preference", nil); len(got.Rules.Steps) != 1 {
		t.Fatalf("get_my_stream_preference = %+v", got)
	}
	// The REST view of the same row agrees (same user, same handler).
	if body := doJSON(t, ts, "GET", "/stream-preferences/me", token, nil); !strings.Contains(string(body), `"pt"`) {
		t.Errorf("REST /stream-preferences/me = %s", body)
	}

	callTool[map[string]any](t, ctx, session, "set_stream_preference_config", map[string]any{"scope_type": "global", "rules": rules})
	cfg := callTool[struct {
		ScopeType string                   `json:"scope_type"`
		Rules     domain.StreamRuleCascade `json:"rules"`
	}](t, ctx, session, "get_stream_preference_config", map[string]any{"scope_type": "global"})
	if cfg.ScopeType != "global" || len(cfg.Rules.Steps) != 1 {
		t.Fatalf("get_stream_preference_config = %+v", cfg)
	}
	if msg := callToolExpectError(t, ctx, session, "get_stream_preference_config", map[string]any{"scope_type": "channel"}); !strings.Contains(msg, "scope_id") {
		t.Errorf("channel scope without id: %q", msg)
	}
	if msg := callToolExpectError(t, ctx, session, "set_stream_preference_config", map[string]any{"scope_type": "nope", "rules": rules}); !strings.Contains(msg, "400") {
		t.Errorf("bad scope: %q", msg)
	}
	tooMany := make([]map[string]any, 21)
	for i := range tooMany {
		tooMany[i] = map[string]any{}
	}
	if msg := callToolExpectError(t, ctx, session, "set_my_stream_preference", map[string]any{"rules": map[string]any{"steps": tooMany}}); !strings.Contains(msg, "at most 20") {
		t.Errorf("21 steps: %q", msg)
	}
}

func TestMCPPreviewCollection(t *testing.T) {
	_, _, session := connectMCP(t)
	ctx := context.Background()
	callToolExpectError(t, ctx, session, "preview_collection", map[string]any{"id": "missing"})
}
