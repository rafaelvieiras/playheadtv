package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/pluginhost"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// GET /catalog-items/{id}/play: what a player should open for an item whose
// source is a plugin.
//
// The backend does not serve the bytes. It asks the plugin for a stream and hands
// the client a URL to that stream, rewritten so the client can reach the machine
// the plugin runs on. Everything expensive — extracting, remuxing with -c copy —
// happens in the plugin, on the machine that has the tools; the client (a 2 GB TV
// box) only fetches segments.
//
// The answer is never cached: a plugin's URL carries a token and an expiry, and a
// remux stream lives as long as its process.
func registerPluginPlayback(mux Router, db storage.DB, host *pluginhost.Host) {
	mux.HandleFunc("GET /catalog-items/{id}/play", pluginPlay(db, host))
}

type pluginPlayback struct {
	Mode      string `json:"mode"`
	URL       string `json:"url"`
	Mime      string `json:"mime,omitempty"`
	Live      bool   `json:"live"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
	Mode2     string `json:"-"`
}

func pluginPlay(db storage.DB, host *pluginhost.Host) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if host == nil {
			writeError(w, http.StatusServiceUnavailable, "this backend was started without a plugin host")
			return
		}
		ctx := r.Context()

		// Only what playback needs, so this stays a single indexed read.
		var item struct {
			ID            string
			MediaSourceID string
			ExternalID    string
			DurationMs    int64
		}
		if err := db.Query(ctx, &item,
			"SELECT id, media_source_id, external_id, duration_ms FROM catalog_item WHERE tenant_id = ? AND id = ?",
			DefaultTenantID, r.PathValue("id")); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "catalog item not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "could not load the catalog item")
			return
		}
		var source domain.MediaSource
		if err := db.Query(ctx, &source, "SELECT "+mediaSourceColumns+
			" FROM media_source WHERE tenant_id = ? AND id = ?", DefaultTenantID, item.MediaSourceID); err != nil {
			writeError(w, http.StatusNotFound, "the media source of this item is gone")
			return
		}
		if source.Type != domain.SourcePlugin || source.PluginID == nil {
			// A media-server item is played from the media server directly: this
			// route exists only for what a plugin can provide.
			writeError(w, http.StatusConflict, "this item is not served by a plugin")
			return
		}

		startAtMs, err := parseStartMs(r.URL.Query().Get("start_ms"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		config := json.RawMessage("null")
		if source.PluginConfigJSON != nil && strings.TrimSpace(*source.PluginConfigJSON) != "" {
			config = json.RawMessage(*source.PluginConfigJSON)
		}

		// Only `remux`: the client is handed one URL, and a pair (video and audio
		// apart) needs a player that merges two sources. Asking for what the
		// player can use is the client's job when it runs the plugin itself; this
		// route is for the client that cannot run it at all.
		raw, err := host.Resolve(ctx, *source.PluginID, config, item.ExternalID, item.DurationMs,
			startAtMs, []string{pluginhost.ModeRemux})
		if err != nil {
			if errors.Is(err, pluginhost.ErrNotInstalled) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writePluginError(w, err)
			return
		}

		var answer struct {
			Mode      string `json:"mode"`
			URL       string `json:"url"`
			Mime      string `json:"mime"`
			Live      bool   `json:"live"`
			ExpiresAt *int64 `json:"expires_at"`
			// A pair would arrive here as sources, never as a url: the answer is
			// rejected rather than served as something the player cannot open.
			Sources []json.RawMessage `json:"sources"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil {
			writeError(w, http.StatusBadGateway, "the plugin answered with something unreadable")
			return
		}
		if answer.URL == "" {
			writeError(w, http.StatusBadGateway,
				fmt.Sprintf("the plugin answered in mode %q with no playable url", answer.Mode))
			return
		}
		reachable, err := reachableURL(answer.URL, r)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, pluginPlayback{
			Mode:      answer.Mode,
			URL:       reachable,
			Mime:      answer.Mime,
			Live:      answer.Live,
			ExpiresAt: answer.ExpiresAt,
		})
	}
}

func parseStartMs(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("start_ms must be a non-negative integer")
	}
	return value, nil
}

// reachableURL rewrites the host of a plugin URL to the host the client used to
// reach this backend.
//
// A plugin that runs on this machine names 127.0.0.1; the player is on the couch,
// so the host has to change while the port, the path and the token stay the
// plugin's own. A URL that already names something else is left alone: only the
// plugin knows where its stream really is.
func reachableURL(raw string, r *http.Request) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("the plugin answered with an unreadable url")
	}
	if !isLoopbackHost(parsed.Hostname()) {
		return raw, nil
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" || isLoopbackHost(host) {
		// Asked from loopback itself (a client on this machine, or a test): the
		// plugin's own address is already the right one.
		return raw, nil
	}
	port := parsed.Port()
	if port == "" {
		return raw, nil
	}
	parsed.Host = net.JoinHostPort(host, port)
	return parsed.String(), nil
}

// isLoopbackHost reports whether a host names this machine: loopback, or the
// unspecified address a listener that bound to every interface reports as its own
// (Go's net.Listener.Addr() for "0.0.0.0:0" or "[::]:0"). Both mean "the plugin is
// here", so the host part is the host's to fill in.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1", "[::1]", "0.0.0.0", "::", "[::]":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
