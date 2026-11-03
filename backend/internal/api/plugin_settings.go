package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/pluginhost"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// GET /plugin-sources and PUT /plugin-sources/{id}/settings.
//
// A plugin's source configuration is the plugin's own, and the plugin declares
// which members it takes (docs/PLUGINS.md "Declared settings"). These two routes
// are the narrow door a *playback client* uses to change exactly those members:
// the settings a TV remote can reach are the ones the plugin declared, and
// nothing else about the source is writable with a device token. Everything
// else about a source (adding one, naming its plugin, the raw config) stays on
// /media-sources, which is session-only authoring.
func registerPluginSettings(mux Router, db storage.DB, host *pluginhost.Host) {
	mux.HandleFunc("GET /plugin-sources", listPluginSources(db, host))
	mux.HandleFunc("PUT /plugin-sources/{id}/settings", updatePluginSourceSettings(db, host))
}

// pluginSourceView is one plugin-backed source as a settings screen needs it:
// which plugin it uses, what that plugin declares, and the values those
// declarations currently hold. The rest of its config is not the screen's
// business and is not returned.
type pluginSourceView struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	PluginID string         `json:"plugin_id"`
	Enabled  bool           `json:"enabled"`
	Settings map[string]any `json:"settings"`
}

func listPluginSources(db storage.DB, host *pluginhost.Host) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if host == nil {
			writeError(w, http.StatusServiceUnavailable, "this backend was started without a plugin host")
			return
		}
		ctx := r.Context()
		var sources []domain.MediaSource
		if err := db.Query(ctx, &sources, "SELECT "+mediaSourceColumns+" FROM media_source"+
			" WHERE tenant_id = ? AND type = ? ORDER BY name", DefaultTenantID, domain.SourcePlugin); err != nil {
			handleDBError(w, err)
			return
		}
		installed, _ := installedSettings(ctx, host)
		views := make([]pluginSourceView, 0, len(sources))
		for _, ms := range sources {
			view := pluginSourceView{ID: ms.ID, Name: ms.Name, Settings: map[string]any{}}
			if ms.PluginID != nil {
				view.PluginID = *ms.PluginID
			}
			declared := installed[view.PluginID]
			view.Enabled = len(declared) > 0
			config := decodeConfig(ms.PluginConfigJSON)
			for key := range declared {
				if value, ok := config[key]; ok {
					view.Settings[key] = value
				}
			}
			views = append(views, view)
		}
		writeJSON(w, http.StatusOK, views)
	}
}

// installedSettings maps a plugin id to the settings it declares, so a list of
// sources costs one manifest read per installed plugin instead of one per
// source.
func installedSettings(ctx context.Context, host *pluginhost.Host) (map[string]map[string]pluginhost.ConfigField, error) {
	plugins, err := host.ListInstalled(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]pluginhost.ConfigField, len(plugins))
	for _, p := range plugins {
		manifest, ok := host.Manifest(ctx, p.ID)
		if !ok || len(manifest.Config) == 0 {
			continue
		}
		fields := make(map[string]pluginhost.ConfigField, len(manifest.Config))
		for _, f := range manifest.Config {
			fields[f.Key] = f
		}
		out[p.ID] = fields
	}
	return out, nil
}

func updatePluginSourceSettings(db storage.DB, host *pluginhost.Host) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if host == nil {
			writeError(w, http.StatusServiceUnavailable, "this backend was started without a plugin host")
			return
		}
		ctx := r.Context()

		var ms domain.MediaSource
		if err := db.Query(ctx, &ms, "SELECT "+mediaSourceColumns+" FROM media_source"+
			" WHERE tenant_id = ? AND id = ?", DefaultTenantID, r.PathValue("id")); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "media source not found")
				return
			}
			handleDBError(w, err)
			return
		}
		if ms.Type != domain.SourcePlugin || ms.PluginID == nil {
			writeError(w, http.StatusConflict, "this media source is not served by a plugin")
			return
		}

		var req struct {
			// Settings is a patch: keys not sent are left as they are, so a
			// screen that shows three of six declared settings does not blank
			// the other three.
			Settings map[string]json.RawMessage `json:"settings"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(req.Settings) == 0 {
			writeError(w, http.StatusBadRequest, "settings is required")
			return
		}

		manifest, ok := host.Manifest(ctx, *ms.PluginID)
		if !ok {
			writeError(w, http.StatusConflict,
				fmt.Sprintf("plugin %q is not installed here, so its settings cannot be changed", *ms.PluginID))
			return
		}
		fields := make(map[string]pluginhost.ConfigField, len(manifest.Config))
		for _, f := range manifest.Config {
			fields[f.Key] = f
		}
		for key, value := range req.Settings {
			field, declared := fields[key]
			if !declared {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("%q is not a setting of plugin %q", key, *ms.PluginID))
				return
			}
			if err := checkSettingValue(field, value); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}

		config := decodeConfig(ms.PluginConfigJSON)
		for key, value := range req.Settings {
			config[key] = value
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not store the settings")
			return
		}
		configText := string(encoded)
		if err := db.Exec(ctx,
			"UPDATE media_source SET plugin_config_json = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			configText, time.Now().UnixMilli(), DefaultTenantID, ms.ID); err != nil {
			handleDBError(w, err)
			return
		}

		stored := decodeConfig(&configText)
		view := pluginSourceView{ID: ms.ID, Name: ms.Name, PluginID: *ms.PluginID, Enabled: true, Settings: map[string]any{}}
		for key := range fields {
			if value, ok := stored[key]; ok {
				view.Settings[key] = value
			}
		}
		writeJSON(w, http.StatusOK, view)
	}
}

// checkSettingValue refuses a value the declaration could not have produced. The
// point is not to second-guess the plugin — it validates its own config anyway —
// but to keep a screen from writing a member the plugin cannot read back.
func checkSettingValue(field pluginhost.ConfigField, raw json.RawMessage) error {
	value, err := decodeRaw(raw)
	if err != nil {
		return fmt.Errorf("%s: %v", field.Key, err)
	}
	switch field.Type {
	case pluginhost.ConfigTypeChoice:
		for _, option := range field.Options {
			candidate, err := decodeRaw(option.Value)
			if err != nil {
				continue
			}
			if reflect.DeepEqual(value, candidate) {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of the values %s declares", field.Key, field.Label)
	case pluginhost.ConfigTypeBool:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be true or false", field.Key)
		}
	case pluginhost.ConfigTypeNumber:
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s must be a number", field.Key)
		}
	case pluginhost.ConfigTypeText:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be text", field.Key)
		}
	}
	return nil
}

func decodeRaw(raw json.RawMessage) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("not valid JSON: %v", err)
	}
	if number, ok := value.(json.Number); ok {
		if f, err := number.Float64(); err == nil {
			return f, nil
		}
	}
	return value, nil
}

// decodeConfig reads a stored plugin config into its members. A row whose config
// is empty, invalid or not an object reads as no config: a settings screen must
// not fail because someone wrote text by hand.
func decodeConfig(stored *string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if stored == nil || strings.TrimSpace(*stored) == "" {
		return out
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(*stored), &members); err != nil {
		return out
	}
	for key, value := range members {
		out[key] = value
	}
	return out
}
