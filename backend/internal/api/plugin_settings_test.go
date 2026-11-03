package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// installFakePluginWithSettings puts a plugin row and its artifact directory in
// place, so the host can read the manifest the plugin declares. Installing a
// real artifact is internal/pluginhost's test; here the manifest file is what
// matters.
func installFakePluginWithSettings(t *testing.T, db interface {
	Exec(context.Context, string, ...any) error
}, host interface{ InstalledDir(string, string) string }) {
	t.Helper()
	dir := host.InstalledDir("fake", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create plugin dir: %v", err)
	}
	manifest := `{
		"id": "fake",
		"name": "Fake source",
		"version": "1.0.0",
		"protocol": 1,
		"entrypoint": "fakeplugin",
		"capabilities": ["catalog.list", "media.resolve"],
		"config": [
			{"key": "max_height", "label": "Quality", "type": "choice",
			 "options": [{"value": 1080, "label": "1080p"}, {"value": 720, "label": "720p"}]},
			{"key": "include_shorts", "label": "Include shorts", "type": "bool"}
		]
	}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	now := time.Now().UnixMilli()
	if err := db.Exec(context.Background(), `INSERT INTO plugin
		(id, version, protocol, os, arch, artifact_sha256, entrypoint, capabilities, enabled, installed_at, updated_at)
		VALUES ('fake', '1.0.0', 1, 'linux', 'amd64', 'abc', 'fakeplugin', 'catalog.list,media.resolve', 1, ?, ?)`,
		now, now); err != nil {
		t.Fatalf("seed plugin row: %v", err)
	}
}

// Setting a plugin's declared settings is one of the few writes a paired playback
// device may make, and it reaches only what the plugin declared.
func TestPluginSourceSettingsAreExactlyWhatThePluginDeclares(t *testing.T) {
	db, mux, host := newTestDBWithPluginHost(t)
	installFakePluginWithSettings(t, db, host)
	seedPluginSource(t, db, "src-1", "fake", nil)

	rec := doRequest(t, mux, "GET", "/plugin-sources", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugin-sources = %d: %s", rec.Code, rec.Body.String())
	}
	var list []pluginSourceView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 || list[0].ID != "src-1" || list[0].PluginID != "fake" {
		t.Fatalf("listing = %+v", list)
	}
	// Nothing declared is set yet, and the source's own config (its url) is not
	// the screen's business.
	if len(list[0].Settings) != 0 {
		t.Fatalf("settings = %v, want nothing set yet", list[0].Settings)
	}

	rec = doRequest(t, mux, "PUT", "/plugin-sources/src-1/settings", `{"settings":{"max_height":1080}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT settings = %d: %s", rec.Code, rec.Body.String())
	}
	var updated pluginSourceView
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, ok := updated.Settings["max_height"].(float64); !ok || got != 1080 {
		t.Fatalf("max_height = %v (%T), want the number 1080", updated.Settings["max_height"], updated.Settings["max_height"])
	}

	// The rest of the config survived the patch: a settings screen must never be
	// able to blank what it does not show.
	var stored struct {
		Config *string
	}
	if err := db.Query(context.Background(), &stored,
		"SELECT plugin_config_json AS config FROM media_source WHERE id = 'src-1'"); err != nil {
		t.Fatalf("read config: %v", err)
	}
	var members map[string]any
	if err := json.Unmarshal([]byte(*stored.Config), &members); err != nil {
		t.Fatalf("stored config is not an object: %v (%s)", err, *stored.Config)
	}
	if members["url"] != "https://example.test/channel" {
		t.Fatalf("stored config = %v, the source's own url was lost", members)
	}
	if members["max_height"] != float64(1080) {
		t.Fatalf("stored max_height = %v (%T), want a JSON number", members["max_height"], members["max_height"])
	}

	t.Run("a key the plugin does not declare", func(t *testing.T) {
		rec := doRequest(t, mux, "PUT", "/plugin-sources/src-1/settings", `{"settings":{"url":"https://elsewhere.test"}}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a value the choice does not offer", func(t *testing.T) {
		rec := doRequest(t, mux, "PUT", "/plugin-sources/src-1/settings", `{"settings":{"max_height":9999}}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a value of the wrong type", func(t *testing.T) {
		rec := doRequest(t, mux, "PUT", "/plugin-sources/src-1/settings", `{"settings":{"include_shorts":"yes"}}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a source that is not a plugin's", func(t *testing.T) {
		rec := doRequest(t, mux, "POST", "/media-sources", `{"type":"local_folder","name":"NAS"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create source: %d %s", rec.Code, rec.Body.String())
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		rec = doRequest(t, mux, "PUT", "/plugin-sources/"+created.ID+"/settings", `{"settings":{"max_height":1080}}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
		}
	})
}

// The authority this route gives a device is the reason it exists apart from
// /media-sources: a paired TV can change declared settings, and nothing else
// about a source.
func TestADeviceMayChangeDeclaredSettingsAndNothingElse(t *testing.T) {
	db, mux, host := newTestDBWithPluginHost(t)
	installFakePluginWithSettings(t, db, host)
	seedPluginSource(t, db, "src-1", "fake", nil)

	rec := doRequest(t, mux, "POST", "/devices", `{"name":"Living Room","platform":"android_tv"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create device: %d %s", rec.Code, rec.Body.String())
	}
	var created createDeviceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	device := func(method, path, body string) *httptest.ResponseRecorder {
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+created.Token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := device("GET", "/plugin-sources", ""); rec.Code != http.StatusOK {
		t.Fatalf("device GET /plugin-sources = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := device("PUT", "/plugin-sources/src-1/settings", `{"settings":{"max_height":720}}`); rec.Code != http.StatusOK {
		t.Fatalf("device PUT settings = %d: %s", rec.Code, rec.Body.String())
	}
	// Everything else about a source stays authoring, which a device cannot do.
	if rec := device("PUT", "/media-sources/src-1", `{"type":"plugin","name":"Renamed","plugin_id":"fake","plugin_config":{}}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("device PUT /media-sources = %d, want 401", rec.Code)
	}
	if rec := device("PUT", "/plugin-sources/src-1/settings/extra", `{"settings":{"max_height":720}}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("device PUT an unknown plugin-sources path = %d, want 401", rec.Code)
	}
}
