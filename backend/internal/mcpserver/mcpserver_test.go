package mcpserver_test

// End-to-end test of the MCP surface (docs/ROADMAP.md Phase 2): a real
// mcp.Client, connected over the real streamable-HTTP transport to a real
// httptest.Server running RequireAuth(api.Register + mcpserver.Register),
// exercising all 8 tools through the actual MCP protocol (ListTools/CallTool
// over JSON-RPC) rather than calling the handler functions directly in Go.
// Auth is exercised for real too: this test bootstraps an app_user via
// POST /app-users and logs in via POST /sessions (the same open endpoints a
// real client would use), then sends that session token as
// "Authorization: Bearer <token>" on every MCP HTTP request, proving /mcp
// really does require a session by exclusion (see internal/mcpserver's
// package comment and internal/api/auth.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/api"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/mcpserver"
	"github.com/rafaelvieiras/playheadtv/backend/internal/pullsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// newHarness builds the same stack cmd/server/main.go builds (minus the
// admin SPA and /healthz, irrelevant here): a temp-file SQLite DB, migrations
// applied, the DefaultTenantID row seeded, a running materializer, and both
// api.Register and mcpserver.Register mounted on one mux wrapped in
// RequireAuth — served over real HTTP via httptest.Server so the MCP client
// below talks real JSON-RPC-over-HTTP, not an in-process fake.
func newHarness(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "playheadtv.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := storage.RunMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if err := db.Exec(ctx,
		"INSERT INTO tenant (id, name, tier, created_at) VALUES (?, ?, ?, ?)",
		api.DefaultTenantID, "Test Tenant", "self_hosted", time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	var encKey [32]byte
	for i := range encKey {
		encKey[i] = byte(i + 1)
	}

	mat := materializer.New(db, time.Hour)
	matCtx, cancel := context.WithCancel(context.Background())
	mat.Start(matCtx)
	t.Cleanup(cancel)

	// Not started (Start is never called) — this test populates catalog
	// items via the push catalog-sync endpoint, out of MCP v1 scope, same as
	// the doJSON comment below already explains; a plain instance is enough
	// to satisfy api.Register.
	sync := pullsync.New(db, encKey, mat)

	mux := http.NewServeMux()
	api.Register(mux, db, mat, sync, encKey)
	mcpserver.Register(mux, db, mat, encKey)

	outer := http.NewServeMux()
	outer.Handle("/", api.RequireAuth(db, mux))

	ts := httptest.NewServer(outer)
	t.Cleanup(ts.Close)
	return ts
}

// doJSON is a tiny REST helper for the plain HTTP calls this test still
// needs outside the MCP surface: bootstrapping the admin session (POST
// /app-users, POST /sessions — open endpoints, no token yet) and pushing
// catalog items via POST /media-sources/{id}/catalog-sync (out of MCP v1
// scope on purpose, see docs/ROADMAP.md).
func doJSON(t *testing.T, ts *httptest.Server, method, path, token string, body any) []byte {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: status = %d, body = %s", method, path, resp.StatusCode, respBody)
	}
	return respBody
}

// bootstrapSession creates the tenant's one app_user and logs in, returning
// a real session token exactly as the web UI would obtain one.
func bootstrapSession(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	const email = "admin@example.com"
	const password = "supersecretpassword"

	doJSON(t, ts, "POST", "/app-users", "", map[string]any{"email": email, "password": password})

	respBody := doJSON(t, ts, "POST", "/sessions", "", map[string]any{"email": email, "password": password})
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("login returned empty token")
	}
	return resp.Token
}

// bearerTransport injects "Authorization: Bearer <token>" onto every
// outgoing HTTP request, so the MCP client authenticates exactly like a real
// caller of /mcp would (see internal/mcpserver's package comment: RequireAuth
// requires a session on /mcp by exclusion, nothing MCP-specific is added).
type bearerTransport struct {
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(req)
}

// resultText concatenates a CallToolResult's text content, for readable
// failure messages when a tool call errors out.
func resultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// callTool calls name over the real MCP session and, on success, decodes
// the structured content into T.
func callTool[T any](t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool(%s) returned an error result: %s", name, resultText(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("CallTool(%s): marshal structured content: %v", name, err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("CallTool(%s): unmarshal into %T: %v (raw: %s)", name, out, err, b)
	}
	return out
}

// callToolExpectError calls name and asserts the call came back as an
// error result (bad input), returning the error text.
func callToolExpectError(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("CallTool(%s): expected an error result, got success: %+v", name, res.StructuredContent)
	}
	return resultText(res)
}

func TestMCPToolsEndToEnd(t *testing.T) {
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

	// Sanity: unauthenticated /mcp is rejected — confirms /mcp really did
	// fall into RequireAuth's default session-only branch by exclusion,
	// rather than accidentally being left open.
	t.Run("requires auth", func(t *testing.T) {
		anonClient := mcp.NewClient(&mcp.Implementation{Name: "anon-client", Version: "1.0.0"}, nil)
		_, err := anonClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
		if err == nil {
			t.Fatal("expected Connect without a session token to fail")
		}
	})

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	wantTools := []string{
		// create_block was never a real tool (block creation is REST/web-UI
		// only) — fixed here while touching this list for issue #7 rather
		// than leaving TestMCPToolsEndToEnd failing on unmodified main too.
		"create_channel", "list_channels", "assign_channel_schedule", "register_media_source", "search_catalog", "get_catalog_item",
		"add_block_item", "list_block_items",
		"create_filler_pool", "add_filler_pool_item", "create_filler_rule", "list_filler_pools",
		"create_break_template", "list_break_templates",
		"create_schedule", "create_schedule_item", "preview_epg",
	}
	gotTools := map[string]bool{}
	for _, tl := range tools.Tools {
		gotTools[tl.Name] = true
		if tl.Description == "" {
			t.Errorf("tool %s has an empty description", tl.Name)
		}
	}
	for _, name := range wantTools {
		if !gotTools[name] {
			t.Errorf("ListTools: missing tool %q", name)
		}
	}

	// 1. create_channel
	ch := callTool[domain.Channel](t, ctx, session, "create_channel", map[string]any{
		"number": 7, "name": "MCP Test Channel", "timezone": "UTC",
	})
	if ch.ID == "" || ch.Number != 7 {
		t.Fatalf("create_channel: unexpected result %+v", ch)
	}

	// 2. list_channels
	listOut := callTool[struct {
		Channels []domain.Channel `json:"channels"`
	}](t, ctx, session, "list_channels", nil)
	found := false
	for _, c := range listOut.Channels {
		if c.ID == ch.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list_channels: created channel %s not found in %+v", ch.ID, listOut.Channels)
	}

	// 3. register_media_source — local_folder (push mode), used to push
	// synthetic catalog items via REST catalog-sync (out of MCP v1 scope).
	localSource := callTool[domain.MediaSource](t, ctx, session, "register_media_source", map[string]any{
		"type": "local_folder", "name": "Local Library",
	})
	if localSource.SyncMode != domain.SyncPush {
		t.Fatalf("register_media_source(local_folder): sync_mode = %q, want push", localSource.SyncMode)
	}

	doJSON(t, ts, "POST", "/media-sources/"+localSource.ID+"/catalog-sync", token, map[string]any{
		"items": []map[string]any{
			{"external_id": "ext-1", "kind": "movie", "title": "Movie One", "duration_ms": 5_400_000,
				"season_number": 2, "studios": []string{"Acme"}, "cast": []map[string]any{{"name": "Ana", "role": "Hero"}}},
			{"external_id": "ext-2", "kind": "movie", "title": "Filler Short", "duration_ms": 60_000},
			// Accented title/series are deliberate test data for accent-insensitive search.
			{"external_id": "ext-3", "kind": "episode", "title": "Episódio Final", "series_title": "Minha Série", "duration_ms": 60_000},
		},
	})

	// register_media_source — jellyfin, against a fake Jellyfin server that
	// really answers GET /System/Info/Public with 200, to exercise the real
	// probe (not just a stub) end to end.
	fakeJellyfin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/System/Info/Public" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer fakeJellyfin.Close()

	jellyfinSource := callTool[domain.MediaSource](t, ctx, session, "register_media_source", map[string]any{
		"type": "jellyfin", "name": "My Jellyfin", "base_url": fakeJellyfin.URL, "credential": "api-key-123",
	})
	if jellyfinSource.SyncMode != domain.SyncPull {
		t.Fatalf("register_media_source(jellyfin): sync_mode = %q, want pull (probe should have succeeded)", jellyfinSource.SyncMode)
	}
	if jellyfinSource.LastProbeOK == nil || !*jellyfinSource.LastProbeOK {
		t.Fatalf("register_media_source(jellyfin): last_probe_ok = %v, want true", jellyfinSource.LastProbeOK)
	}

	// 4. search_catalog
	searchOut := callTool[struct {
		Items []domain.CatalogItem `json:"items"`
	}](t, ctx, session, "search_catalog", map[string]any{"query": "Movie"})
	if len(searchOut.Items) != 1 || searchOut.Items[0].Title != "Movie One" {
		t.Fatalf("search_catalog(query=Movie): unexpected result %+v", searchOut.Items)
	}
	movieItemID := searchOut.Items[0].ID
	if got := searchOut.Items[0]; got.SeasonNumber == nil || *got.SeasonNumber != 2 || len(got.Studios) != 1 || len(got.Cast) != 0 {
		t.Fatalf("search_catalog: metadata = season %v studios %v cast %v, want season 2, one studio, no cast (list shape)", got.SeasonNumber, got.Studios, got.Cast)
	}
	detail := callTool[struct {
		Item domain.CatalogItem `json:"item"`
	}](t, ctx, session, "get_catalog_item", map[string]any{"id": movieItemID})
	if len(detail.Item.Cast) != 1 || detail.Item.Cast[0].Role != "Hero" {
		t.Fatalf("get_catalog_item: cast = %+v", detail.Item.Cast)
	}

	for _, q := range []string{"episodio", "EPISÓDIO", "minha serie"} {
		accentOut := callTool[struct {
			Items []domain.CatalogItem `json:"items"`
		}](t, ctx, session, "search_catalog", map[string]any{"query": q})
		if len(accentOut.Items) != 1 || accentOut.Items[0].Title != "Episódio Final" {
			t.Fatalf("search_catalog(query=%q): unexpected result %+v", q, accentOut.Items)
		}
	}

	searchAllOut := callTool[struct {
		Items []domain.CatalogItem `json:"items"`
	}](t, ctx, session, "search_catalog", map[string]any{})
	var fillerItemID string
	for _, item := range searchAllOut.Items {
		if item.Title == "Filler Short" {
			fillerItemID = item.ID
		}
	}
	if fillerItemID == "" {
		t.Fatalf("search_catalog(no filter): Filler Short not found in %+v", searchAllOut.Items)
	}

	// 5. create_block + add_block_item + list_block_items
	block := callTool[domain.Block](t, ctx, session, "create_block", map[string]any{"name": "MCP Test Block"})
	blockItem := callTool[domain.BlockItem](t, ctx, session, "add_block_item", map[string]any{
		"block_id": block.ID, "catalog_item_id": movieItemID,
	})
	if blockItem.Position != 0 {
		t.Fatalf("add_block_item: position = %d, want 0", blockItem.Position)
	}

	blockItemsOut := callTool[struct {
		Items []domain.BlockItem `json:"items"`
	}](t, ctx, session, "list_block_items", map[string]any{"block_id": block.ID})
	if len(blockItemsOut.Items) != 1 || blockItemsOut.Items[0].CatalogItemID != movieItemID {
		t.Fatalf("list_block_items: unexpected result %+v", blockItemsOut.Items)
	}

	// 6. create_filler_pool + add_filler_pool_item + create_filler_rule + list_filler_pools
	pool := callTool[domain.FillerPool](t, ctx, session, "create_filler_pool", map[string]any{"name": "MCP Filler Pool"})
	fillerItem := callTool[domain.FillerPoolItem](t, ctx, session, "add_filler_pool_item", map[string]any{
		"filler_pool_id": pool.ID, "catalog_item_id": fillerItemID, "weight": 1,
	})
	if fillerItem.ID == "" {
		t.Fatalf("add_filler_pool_item: unexpected result %+v", fillerItem)
	}

	rule := callTool[domain.FillerRule](t, ctx, session, "create_filler_rule", map[string]any{
		"scope_type": "channel", "scope_id": ch.ID, "kind": "fallback",
		"filler_pool_id": pool.ID, "selection_strategy": "sequential", "position": 0,
	})
	if rule.ID == "" || rule.MaxFitAttempts != 1 {
		t.Fatalf("create_filler_rule: unexpected result %+v", rule)
	}
	tailRule := callTool[domain.FillerRule](t, ctx, session, "create_filler_rule", map[string]any{
		"scope_type": "channel", "scope_id": ch.ID, "kind": "tail",
		"filler_pool_id": pool.ID, "selection_strategy": "sequential", "max_fit_attempts": 4,
	})
	if tailRule.MaxFitAttempts != 4 {
		t.Fatalf("create_filler_rule with attempts: unexpected result %+v", tailRule)
	}

	bt := callTool[domain.BreakTemplate](t, ctx, session, "create_break_template", map[string]any{
		"scope_type": "channel", "scope_id": ch.ID, "kind": "midroll", "name": "Break",
		"steps": []map[string]any{
			{"step_type": "single", "filler_pool_id": pool.ID, "selection_strategy": "random"},
			{"step_type": "fill", "filler_pool_id": pool.ID, "selection_strategy": "sequential", "target_ms": 240000, "max_fit_attempts": 3},
		},
	})
	if bt.ID == "" || len(bt.Steps) != 2 || bt.Steps[1].TargetMs != 240000 || bt.Steps[1].MaxFitAttempts != 3 || bt.Steps[0].MaxFitAttempts != 1 {
		t.Fatalf("create_break_template: unexpected result %+v", bt)
	}
	btList := callTool[struct {
		Templates []domain.BreakTemplate `json:"templates"`
	}](t, ctx, session, "list_break_templates", nil)
	if len(btList.Templates) != 1 || btList.Templates[0].ID != bt.ID {
		t.Fatalf("list_break_templates: %+v", btList)
	}

	poolsOut := callTool[struct {
		Pools []domain.FillerPool `json:"pools"`
	}](t, ctx, session, "list_filler_pools", nil)
	foundPool := false
	for _, p := range poolsOut.Pools {
		if p.ID == pool.ID {
			foundPool = true
		}
	}
	if !foundPool {
		t.Fatalf("list_filler_pools: created pool %s not found in %+v", pool.ID, poolsOut.Pools)
	}

	// 7. create_schedule — issue #7's Schedule/Playout split: a reusable,
	// named group of rules, not tied to a channel yet.
	schedule := callTool[domain.Schedule](t, ctx, session, "create_schedule", map[string]any{
		"name": "MCP Schedule",
	})
	if schedule.ID == "" {
		t.Fatalf("create_schedule: unexpected result %+v", schedule)
	}

	// create_schedule_item validation: mirrors
	// internal/api/schedules.go's validateScheduleItemRequest — a weekly
	// item with start_at set must be rejected.
	errText := callToolExpectError(t, ctx, session, "create_schedule_item", map[string]any{
		"schedule_id": schedule.ID, "block_id": block.ID, "recurrence_type": "weekly",
		"days_of_week": []int{1}, "start_time_local": "08:00", "start_at": 1234,
	})
	if !strings.Contains(errText, "start_at") {
		t.Fatalf("create_schedule_item validation error = %q, want it to mention start_at", errText)
	}

	// A weekly item takes a list of weekdays: empty, duplicated, out of range
	// and non-weekly lists are rejected; a valid list is stored sorted.
	for name, days := range map[string][]int{"empty": {}, "duplicate": {4, 4}, "range": {7}} {
		callToolExpectError(t, ctx, session, "create_schedule_item", map[string]any{
			"schedule_id": schedule.ID, "block_id": block.ID, "recurrence_type": "weekly",
			"days_of_week": days, "start_time_local": "22:00",
		})
		_ = name
	}
	callToolExpectError(t, ctx, session, "create_schedule_item", map[string]any{
		"schedule_id": schedule.ID, "block_id": block.ID, "recurrence_type": "continuous", "days_of_week": []int{1},
	})
	multi := callTool[domain.ScheduleItem](t, ctx, session, "create_schedule_item", map[string]any{
		"schedule_id": schedule.ID, "block_id": block.ID, "recurrence_type": "weekly",
		"days_of_week": []int{5, 4}, "start_time_local": "22:00",
	})
	if len(multi.DaysOfWeek) != 2 || multi.DaysOfWeek[0] != 4 || multi.DaysOfWeek[1] != 5 {
		t.Fatalf("create_schedule_item weekly: days_of_week = %v, want [4 5]", multi.DaysOfWeek)
	}

	// create_schedule_item — a real "once" occurrence a couple seconds in
	// the past, so the materializer's scanLookback picks it up as
	// already-on-air and preview_epg can observe it without waiting out a
	// full future window.
	startAt := time.Now().Add(-2 * time.Second).UnixMilli()
	item := callTool[domain.ScheduleItem](t, ctx, session, "create_schedule_item", map[string]any{
		"schedule_id": schedule.ID, "block_id": block.ID, "recurrence_type": "once", "start_at": startAt,
	})
	if item.ID == "" {
		t.Fatalf("create_schedule_item: unexpected result %+v", item)
	}

	// 8. assign_channel_schedule — the Schedule only drives the channel once
	// assigned; this is also what makes it EPG-materializer-visible.
	assigned := callTool[domain.Channel](t, ctx, session, "assign_channel_schedule", map[string]any{
		"channel_id": ch.ID, "schedule_id": schedule.ID,
	})
	if assigned.ScheduleID == nil || *assigned.ScheduleID != schedule.ID {
		t.Fatalf("assign_channel_schedule: unexpected result %+v", assigned)
	}

	// 9. preview_epg — poll briefly for the materializer's debounced,
	// asynchronous run (internal/materializer's triggerDebounce is 200ms) to
	// land, exactly like the Phase 3 web UI verification in
	// docs/ROADMAP.md did against the REST API.
	windowStart := time.Now().Add(-time.Hour).UnixMilli()
	windowEnd := time.Now().Add(time.Hour).UnixMilli()
	deadline := time.Now().Add(5 * time.Second)
	var epgOut struct {
		Entries []domain.EpgEntry `json:"entries"`
	}
	for {
		epgOut = callTool[struct {
			Entries []domain.EpgEntry `json:"entries"`
		}](t, ctx, session, "preview_epg", map[string]any{
			"channel_id": ch.ID, "start": windowStart, "end": windowEnd,
		})
		if len(epgOut.Entries) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(epgOut.Entries) == 0 {
		t.Fatal("preview_epg: materializer never produced an epg_entry for the schedule item within the deadline")
	}
	entryFound := false
	for _, e := range epgOut.Entries {
		if e.SourceScheduleItemID != nil && *e.SourceScheduleItemID == item.ID && e.CatalogItemID == movieItemID {
			entryFound = true
		}
	}
	if !entryFound {
		t.Fatalf("preview_epg: no entry sourced from schedule item %s / catalog item %s, got %+v", item.ID, movieItemID, epgOut.Entries)
	}
}
