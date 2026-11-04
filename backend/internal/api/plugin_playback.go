package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
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
	// Where a client can reach this machine's plugin streams, when it is not the
	// address the client used to reach the API. A client that talks to the backend
	// through a reverse proxy (a domain, a VPN) would otherwise be handed
	// "https://<proxy>:<ephemeral port>", which nothing serves: the proxy knows
	// nothing about the plugin's port, and the plugin is on this LAN.
	streamHost := strings.TrimSpace(os.Getenv("PLAYHEADTV_PLUGIN_STREAM_HOST"))
	mux.HandleFunc("GET /catalog-items/{id}/play", pluginPlay(db, host, streamHost))
}

// pluginPlayback is what the client opens. `url` is always one address it can
// fetch; `audio_url` is set in mode pair, where the audio is a second address and
// the player merges them (docs/PLUGINS.md "Modes").
type pluginPlayback struct {
	Mode      string `json:"mode"`
	URL       string `json:"url"`
	AudioURL  string `json:"audio_url,omitempty"`
	Mime      string `json:"mime,omitempty"`
	Live      bool   `json:"live"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

func pluginPlay(db storage.DB, host *pluginhost.Host, streamHost string) http.HandlerFunc {
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

		// Which shapes this client can play, best first: the client says so
		// (`modes=pair,remux`) because only it knows whether its player can merge a
		// second source. Absent, the answer stays what every client could always
		// play: a remux.
		modes, err := playModes(r.URL.Query().Get("modes"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		raw, err := host.Resolve(ctx, *source.PluginID, config, item.ExternalID, item.DurationMs,
			startAtMs, modes)
		if err != nil {
			if errors.Is(err, pluginhost.ErrNotInstalled) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writePluginError(w, err)
			return
		}

		var answer struct {
			Mode      string            `json:"mode"`
			URL       string            `json:"url"`
			Mime      string            `json:"mime"`
			Live      bool              `json:"live"`
			Headers   map[string]string `json:"headers"`
			ExpiresAt *int64            `json:"expires_at"`
			// A pair arrives as two sources, one video and one audio, and the
			// player merges them.
			Sources []pairSource `json:"sources"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil {
			writeError(w, http.StatusBadGateway, "the plugin answered with something unreadable")
			return
		}
		out, err := playbackFromAnswer(answer.Mode, answer.URL, answer.Sources, answer.Mime, answer.Live,
			answer.ExpiresAt, r, streamHost)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	}
}

// playbackFromAnswer turns a plugin's answer into what the client opens, with
// every address rewritten to the machine the client can reach. A pair needs one
// video and one audio address; anything else is a single address the player
// opens on its own.
func playbackFromAnswer(mode, rawURL string, sources []pairSource, mime string, live bool,
	expiresAt *int64, r *http.Request, streamHost string) (pluginPlayback, error) {
	out := pluginPlayback{Mode: mode, Mime: mime, Live: live, ExpiresAt: expiresAt}
	if mode == pluginhost.ModePair {
		video, audio := pairSources(sources)
		if video == "" || audio == "" {
			return pluginPlayback{}, fmt.Errorf(
				"the plugin answered a pair with no video or no audio url (%d sources)", len(sources))
		}
		reachable, err := reachableURL(video, r, streamHost)
		if err != nil {
			return pluginPlayback{}, err
		}
		reachableAudio, err := reachableURL(audio, r, streamHost)
		if err != nil {
			return pluginPlayback{}, err
		}
		out.URL, out.AudioURL = reachable, reachableAudio
		return out, nil
	}
	if rawURL == "" {
		return pluginPlayback{}, fmt.Errorf("the plugin answered in mode %q with no playable url", mode)
	}
	reachable, err := reachableURL(rawURL, r, streamHost)
	if err != nil {
		return pluginPlayback{}, err
	}
	out.URL = reachable
	return out, nil
}

// playModes reads the client's `modes` list. Absent is the answer every client
// has always received (a remux); otherwise the list is taken in the order given,
// because that order is the client's preference and the plugin honours it.
func playModes(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{pluginhost.ModeRemux}, nil
	}
	var modes []string
	for _, mode := range strings.Split(raw, ",") {
		switch mode = strings.TrimSpace(mode); mode {
		case pluginhost.ModeSingle, pluginhost.ModePair, pluginhost.ModeRemux:
			modes = append(modes, mode)
		case "":
			// A trailing comma or a padding space is not a request for anything.
		default:
			return nil, fmt.Errorf("modes must be a comma-separated list of single, pair, remux (got %q)", mode)
		}
	}
	if len(modes) == 0 {
		return nil, fmt.Errorf("modes must name at least one of single, pair, remux")
	}
	return modes, nil
}

// pairSource is one track of a pair answer.
type pairSource struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Mime    string            `json:"mime"`
	Headers map[string]string `json:"headers"`
}

// pairSources returns the video and audio urls of a pair answer, or an empty
// string for a track the plugin did not send: two sources of the same type are
// not a pair the player can merge.
func pairSources(sources []pairSource) (video, audio string) {
	for _, source := range sources {
		switch {
		case source.Type == "video" && video == "":
			video = source.URL
		case source.Type == "audio" && audio == "":
			audio = source.URL
		}
	}
	return video, audio
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
func reachableURL(raw string, r *http.Request, overrideHost string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("the plugin answered with an unreadable url")
	}
	if !isLoopbackHost(parsed.Hostname()) {
		return raw, nil
	}
	host := overrideHost
	if host == "" {
		host = r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
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
