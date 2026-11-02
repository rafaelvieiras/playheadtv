// Command fakeplugin is a test fixture that speaks host protocol 1
// (docs/PLUGINS.md). Tests build it into a temporary directory and install it
// like a real artifact; nothing in the product uses it.
//
// Behaviour is fixed and deterministic so a test can assert on it:
//
//   - catalog.list answers in two pages (2 items, then 1) and reports
//     next_cursor only while it has more; a cursor always returns the same page;
//   - a config {"url":"empty"} lists nothing;
//   - a config {"url":"bad-duration"} lists an item with duration_ms 0, which a
//     host must drop;
//   - media.resolve returns a fixed URL and expiry.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	ID     int64 `json:"id"`
	Result any   `json:"result,omitempty"`
	Error  any   `json:"error,omitempty"`
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 8<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for in.Scan() {
		var req request
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			fmt.Fprintln(os.Stderr, "fakeplugin: bad request line:", err)
			continue
		}
		switch req.Method {
		case "info":
			write(out, response{ID: req.ID, Result: map[string]any{
				"id":           envOr("PLAYHEADTV_PLUGIN_ID", "fake"),
				"name":         "Fake source",
				"version":      envOr("PLAYHEADTV_PLUGIN_VERSION", "1.0.0"),
				"protocol":     1,
				"entrypoint":   "fakeplugin",
				"capabilities": []string{"catalog.list", "media.resolve"},
			}})
		case "catalog.list":
			var p struct {
				Config json.RawMessage `json:"config"`
				Cursor string          `json:"cursor"`
				Limit  int             `json:"limit"`
			}
			_ = json.Unmarshal(req.Params, &p)
			var cfg struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(p.Config, &cfg)

			if cfg.URL == "empty" {
				write(out, response{ID: req.ID, Result: map[string]any{"items": []any{}}})
				continue
			}
			if p.Cursor == "" {
				items := []map[string]any{
					{"external_id": "ext-1", "title": "First", "kind": "movie", "duration_ms": 600000},
					{"external_id": "ext-2", "title": "Second", "kind": "other", "duration_ms": 30000,
						"genres": "documentary", "production_year": 2026},
				}
				if cfg.URL == "bad-duration" {
					items = append(items, map[string]any{"external_id": "ext-bad", "title": "No length", "duration_ms": 0})
				}
				write(out, response{ID: req.ID, Result: map[string]any{"items": items, "next_cursor": "page-2"}})
				continue
			}
			// A cursor is idempotent: the same page comes back however many
			// times it is asked for, like a real listing.
			write(out, response{ID: req.ID, Result: map[string]any{
				"items": []map[string]any{
					{"external_id": "ext-3", "title": "Third", "kind": "episode", "series_title": "A series", "duration_ms": 1200000},
				},
			}})
		case "media.resolve":
			write(out, response{ID: req.ID, Result: map[string]any{
				"url":        "https://media.example.test/fake.mp4",
				"mime":       "video/mp4",
				"expires_at": 1893456000000,
			}})
		case "fail":
			write(out, response{ID: req.ID, Error: rpcError{Code: "unavailable", Message: "nope"}})
		case "hang":
			// Deliberately never answers: the host must time out and kill.
			continue
		case "shutdown":
			write(out, response{ID: req.ID, Result: map[string]any{}})
			return
		default:
			write(out, response{ID: req.ID, Error: rpcError{Code: "unsupported", Message: "unknown method " + req.Method}})
		}
	}
}

func write(out *bufio.Writer, resp response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return
	}
	out.Write(raw)
	out.WriteByte('\n')
	out.Flush()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
