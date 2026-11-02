package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// seedPluginSource inserts a plugin-backed media source row directly: the API
// tests exercise reading and validating those rows, not installing a plugin
// (internal/pluginhost covers install/run with a real artifact).
func seedPluginSource(t *testing.T, db *storage.SQLite, id, pluginID string, minVersion *string) {
	t.Helper()
	now := time.Now().UnixMilli()
	config := `{"url":"https://example.test/channel"}`
	if err := db.Exec(context.Background(), `INSERT INTO media_source
		(id, tenant_id, type, name, sync_mode, plugin_id, plugin_min_version, plugin_config_json, created_at, updated_at)
		VALUES (?, ?, 'plugin', ?, 'pull', ?, ?, ?, ?, ?)`,
		id, DefaultTenantID, id, pluginID, minVersion, config, now, now); err != nil {
		t.Fatalf("seed plugin source: %v", err)
	}
}

func TestCreatePluginMediaSource(t *testing.T) {
	_, mux := newTestDB(t)

	rec := doRequest(t, mux, "POST", "/media-sources", `{
		"type": "plugin",
		"name": "Fixture channel",
		"plugin_id": "fake",
		"plugin_min_version": "1.0.0",
		"plugin_config": {"url": "https://example.test/channel"}
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /media-sources = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["type"] != "plugin" || got["plugin_id"] != "fake" {
		t.Errorf("response = %v", got)
	}
	// A plugin source is always pulled by this backend: there is no remote
	// server to probe, so a plugin source must never land in push mode.
	if got["sync_mode"] != "pull" {
		t.Errorf("sync_mode = %v, want pull", got["sync_mode"])
	}
	if got["plugin_min_version"] != "1.0.0" {
		t.Errorf("plugin_min_version = %v", got["plugin_min_version"])
	}
	if got["plugin_config_json"] == nil {
		t.Errorf("plugin_config_json was not stored: %v", got)
	}
}

func TestCreateMediaSourceRejectsBadPluginFields(t *testing.T) {
	_, mux := newTestDB(t)

	cases := map[string]string{
		"plugin without plugin_id": `{"type":"plugin","name":"x","plugin_config":{"url":"u"}}`,
		"invalid plugin_id":        `{"type":"plugin","name":"x","plugin_id":"Not A Plugin"}`,
		"invalid min version":      `{"type":"plugin","name":"x","plugin_id":"fake","plugin_min_version":"../1.0"}`,
		"config is not an object":  `{"type":"plugin","name":"x","plugin_id":"fake","plugin_config":"nope"}`,
		"jellyfin with plugin_id":  `{"type":"jellyfin","name":"x","base_url":"http://127.0.0.1:1","plugin_id":"fake"}`,
	}
	for name, body := range cases {
		rec := doRequest(t, mux, "POST", "/media-sources", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

func TestListPluginsWithoutARegistry(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "GET", "/plugins", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugins = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		RegistryConfigured bool             `json:"registry_configured"`
		Installed          []map[string]any `json:"installed"`
		Plugins            []map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RegistryConfigured {
		t.Error("registry_configured = true without PLAYHEADTV_PLUGIN_REGISTRY_URL")
	}
	if len(got.Installed) != 0 || len(got.Plugins) != 0 {
		t.Errorf("response = %+v, want empty lists", got)
	}
}

func TestInstallPluginWithoutRegistryIsRefused(t *testing.T) {
	_, mux := newTestDB(t)
	rec := doRequest(t, mux, "POST", "/plugins/fake/install", `{}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestPluginRoutesRejectUnknownPlugin(t *testing.T) {
	_, mux := newTestDB(t)
	if rec := doRequest(t, mux, "DELETE", "/plugins/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete unknown: status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if rec := doRequest(t, mux, "PUT", "/plugins/nope", `{"enabled":true}`); rec.Code != http.StatusNotFound {
		t.Errorf("update unknown: status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// TestEPGEntryCarriesItsPluginRequirement is the contract a client relies on to
// hide a channel it cannot play (docs/PLUGINS.md "Requirements in the guide").
func TestEPGEntryCarriesItsPluginRequirement(t *testing.T) {
	db, mux := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	minVersion := "1.2.0"
	seedPluginSource(t, db, "ms-plugin", "fake", &minVersion)
	if err := db.Exec(ctx, `INSERT INTO media_source (id, tenant_id, type, name, base_url, sync_mode, created_at, updated_at)
		VALUES ('ms-jf', ?, 'jellyfin', 'JF', 'http://127.0.0.1:1', 'pull', ?, ?)`, DefaultTenantID, now, now); err != nil {
		t.Fatalf("seed jellyfin source: %v", err)
	}
	for _, item := range []struct{ id, source, title string }{
		{"item-plugin", "ms-plugin", "From a plugin"},
		{"item-jf", "ms-jf", "From Jellyfin"},
	} {
		if err := db.Exec(ctx, `INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at)
			VALUES (?, ?, ?, ?, 'movie', ?, 600000, ?)`, item.id, DefaultTenantID, item.source, "ext-"+item.id, item.title, now); err != nil {
			t.Fatalf("seed catalog item: %v", err)
		}
	}
	if err := db.Exec(ctx, `INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at)
		VALUES ('c1', ?, 1, 'C1', 'UTC', ?, ?)`, DefaultTenantID, now, now); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	for i, item := range []string{"item-plugin", "item-jf"} {
		start := now + int64(i)*600000
		if err := db.Exec(ctx, `INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, generated_at)
			VALUES (?, ?, 'c1', 'program', ?, ?, ?, 1)`, "e"+item, DefaultTenantID, item, start, start+600000); err != nil {
			t.Fatalf("seed epg entry: %v", err)
		}
	}

	entries := getEPGJSON(t, mux, "/epg")
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	byItem := map[string]map[string]any{}
	for _, e := range entries {
		byItem[e["catalog_item_id"].(string)] = e
	}

	pluginEntry := byItem["item-plugin"]
	requires, _ := pluginEntry["requires"].([]any)
	if len(requires) != 1 {
		t.Fatalf("plugin entry requires = %v, want one requirement", pluginEntry["requires"])
	}
	req := requires[0].(map[string]any)
	if req["id"] != "fake" || req["min_version"] != "1.2.0" {
		t.Errorf("requirement = %v, want fake >= 1.2.0", req)
	}
	// The client resolves playback itself, so the source's configuration has to
	// reach it with the entry instead of in a separate lookup.
	config, _ := req["config"].(map[string]any)
	if config["url"] != "https://example.test/channel" {
		t.Errorf("requirement config = %v, want the source's own config", req["config"])
	}

	if _, present := byItem["item-jf"]["requires"]; present {
		t.Errorf("a Jellyfin item must not require a plugin: %v", byItem["item-jf"]["requires"])
	}
}

// A source with no configuration still names the plugin it needs, without an
// empty object the client would have to special-case.
func TestEPGPluginRequirementOmitsAnAbsentConfig(t *testing.T) {
	db, mux := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	seedPluginSource(t, db, "ms-plugin", "fake", nil)
	if err := db.Exec(ctx, `UPDATE media_source SET plugin_config_json = NULL WHERE id = 'ms-plugin'`); err != nil {
		t.Fatalf("clear config: %v", err)
	}
	if err := db.Exec(ctx, `INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at)
		VALUES ('item-plugin', ?, 'ms-plugin', 'ext', 'movie', 'From a plugin', 600000, ?)`, DefaultTenantID, now); err != nil {
		t.Fatalf("seed catalog item: %v", err)
	}
	if err := db.Exec(ctx, `INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at)
		VALUES ('c1', ?, 1, 'C1', 'UTC', ?, ?)`, DefaultTenantID, now, now); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := db.Exec(ctx, `INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, generated_at)
		VALUES ('e1', ?, 'c1', 'program', 'item-plugin', ?, ?, 1)`, DefaultTenantID, now, now+600000); err != nil {
		t.Fatalf("seed epg entry: %v", err)
	}

	entries := getEPGJSON(t, mux, "/epg")
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	requires, _ := entries[0]["requires"].([]any)
	if len(requires) != 1 {
		t.Fatalf("requires = %v, want one requirement", entries[0]["requires"])
	}
	req := requires[0].(map[string]any)
	if req["id"] != "fake" {
		t.Errorf("requirement = %v, want the plugin id", req)
	}
	if _, present := req["config"]; present {
		t.Errorf("an absent config must be omitted: %v", req)
	}
}
