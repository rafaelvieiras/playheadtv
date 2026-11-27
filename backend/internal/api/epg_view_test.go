package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// epgSeed seeds a media source, a few catalog items and two channels for the
// /epg view tests.
type epgSeed struct {
	db *storage.SQLite
}

func newEPGSeed(t *testing.T, db *storage.SQLite) *epgSeed {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Exec(ctx, `INSERT INTO media_source (id, tenant_id, type, name, sync_mode, created_at, updated_at)
		VALUES ('ms', ?, 'jellyfin', 'ms', 'pull', ?, ?)`, DefaultTenantID, now, now))
	must(db.Exec(ctx, `INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, series_title, series_external_id,
		season_number, episode_number, duration_ms, synced_at, image_primary_tag, image_backdrop_tag, image_thumb_tag, series_image_primary_tag,
		width, height, video_codec, container, audio_codec, audio_channels, bitrate,
		overview, content_rating, community_rating, genres, production_year)
		VALUES ('ep', ?, 'ms', 'jf-ep', 'episode', 'Pilot', 'Show', 'jf-show', 1, 2, 1800000, ?, 'pt', 'bt', 'tt', 'spt', 320, 240, 'mpeg4', 'avi',
		'aac', 2, 128000, 'A pilot episode.', 'PG-13', 7.4, 'Drama,Comedy', 2015)`, DefaultTenantID, now))
	must(db.Exec(ctx, `INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at)
		VALUES ('mov', ?, 'ms', 'jf-mov', 'movie', 'A Movie', 5400000, ?)`, DefaultTenantID, now))
	for i := 0; i < 3; i++ {
		must(db.Exec(ctx, `INSERT INTO catalog_item (id, tenant_id, media_source_id, external_id, kind, title, duration_ms, synced_at)
			VALUES (?, ?, 'ms', ?, 'movie', ?, 10000, ?)`, fmt.Sprintf("clip%d", i), DefaultTenantID, fmt.Sprintf("jf-clip%d", i), fmt.Sprintf("Clip %d", i), now))
	}
	must(db.Exec(ctx, `INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('c1', ?, 1, 'C1', 'UTC', ?, ?)`, DefaultTenantID, now, now))
	must(db.Exec(ctx, `INSERT INTO channel (id, tenant_id, number, name, timezone, created_at, updated_at) VALUES ('c2', ?, 2, 'C2', 'UTC', ?, ?)`, DefaultTenantID, now, now))
	return &epgSeed{db: db}
}

func (s *epgSeed) entry(t *testing.T, id, channel, kind, item string, start, end int64) {
	t.Helper()
	if err := s.db.Exec(context.Background(), `INSERT INTO epg_entry (id, tenant_id, channel_id, kind, catalog_item_id, start_at, end_at, generated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)`, id, DefaultTenantID, channel, kind, item, start, end); err != nil {
		t.Fatal(err)
	}
}

func getEPGJSON(t *testing.T, mux *http.ServeMux, path string) []map[string]any {
	t.Helper()
	rec := doRequest(t, mux, "GET", path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestEPGEnrichedIsAdditive(t *testing.T) {
	db, mux := newTestDB(t)
	s := newEPGSeed(t, db)
	s.entry(t, "e1", "c1", "program", "ep", 1000, 1801000)
	s.entry(t, "e2", "c1", "program", "mov", 1801000, 5401000)

	got := getEPGJSON(t, mux, "/epg?channel_id=c1")
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
	e := got[0]
	// The pre-existing fields are untouched.
	for k, want := range map[string]any{
		"id": "e1", "tenant_id": DefaultTenantID, "channel_id": "c1", "kind": "program",
		"catalog_item_id": "ep", "start_at": 1000.0, "end_at": 1801000.0,
	} {
		if e[k] != want {
			t.Errorf("%s = %v, want %v", k, e[k], want)
		}
	}
	if v, ok := e["source_schedule_item_id"]; !ok || v != nil {
		t.Errorf("source_schedule_item_id = %v (present %v), want null", v, ok)
	}
	if _, ok := e["generated_at"]; !ok {
		t.Error("generated_at missing")
	}
	// The new ones.
	for k, want := range map[string]any{
		"media_source_id": "ms", "external_id": "jf-ep", "item_kind": "episode", "title": "Pilot",
		"series_title": "Show", "series_external_id": "jf-show", "season_number": 1.0, "episode_number": 2.0,
		"duration_ms": 1800000.0, "image_primary_tag": "pt", "image_backdrop_tag": "bt",
		"image_thumb_tag": "tt", "series_image_primary_tag": "spt",
	} {
		if e[k] != want {
			t.Errorf("%s = %v, want %v", k, e[k], want)
		}
	}
	// The shape of the media file, which is what a player decides "can I decode
	// this?" from.
	for k, want := range map[string]any{
		"width": 320.0, "height": 240.0, "video_codec": "mpeg4", "container": "avi",
		"audio_codec": "aac", "audio_channels": 2.0, "bitrate": 128000.0,
	} {
		if e[k] != want {
			t.Errorf("%s = %v, want %v", k, e[k], want)
		}
	}
	// What the guide needs to write a program's details panel with no second
	// request: the synopsis, the rating, the item's own year and genres.
	for k, want := range map[string]any{
		"overview": "A pilot episode.", "content_rating": "PG-13", "community_rating": 7.4,
		"genres": "Drama,Comedy", "production_year": 2015.0,
	} {
		if e[k] != want {
			t.Errorf("%s = %v, want %v", k, e[k], want)
		}
	}
	// A movie has no series/art fields at all (omitted, not null).
	for _, k := range []string{"series_title", "season_number", "image_primary_tag", "series_image_primary_tag",
		"width", "height", "video_codec", "container", "audio_codec", "audio_channels", "bitrate",
		"overview", "content_rating", "community_rating", "genres", "production_year"} {
		if _, ok := got[1][k]; ok {
			t.Errorf("movie entry unexpectedly has %s", k)
		}
	}
	if got[1]["title"] != "A Movie" || got[1]["external_id"] != "jf-mov" {
		t.Errorf("movie = %v", got[1])
	}
}

func TestEPGTailNotGroupedByDefault(t *testing.T) {
	db, mux := newTestDB(t)
	s := newEPGSeed(t, db)
	s.entry(t, "t0", "c1", "tail", "clip0", 0, 10000)
	s.entry(t, "t1", "c1", "tail", "clip1", 10000, 20000)

	got := getEPGJSON(t, mux, "/epg?channel_id=c1")
	if len(got) != 2 || got[0]["catalog_item_id"] != "clip0" || got[0]["clips"] != nil {
		t.Fatalf("default response must keep one row per stored entry, got %v", got)
	}
}

func TestEPGTailGrouping(t *testing.T) {
	db, mux := newTestDB(t)
	s := newEPGSeed(t, db)
	// c1: tail x3, program, tail x2 (split by the program), a gap, tail x1.
	s.entry(t, "a0", "c1", "tail", "clip0", 0, 10000)
	s.entry(t, "a1", "c1", "tail", "clip1", 10000, 20000)
	s.entry(t, "a2", "c1", "tail", "clip2", 20000, 30000)
	s.entry(t, "p", "c1", "program", "ep", 30000, 1830000)
	s.entry(t, "b0", "c1", "tail", "clip0", 1830000, 1840000)
	s.entry(t, "b1", "c1", "tail", "clip1", 1840000, 1850000)
	s.entry(t, "g0", "c1", "tail", "clip2", 1900000, 1910000) // gap before it
	// A tail of another channel directly "after" c1's last one must not merge.
	s.entry(t, "x0", "c2", "tail", "clip0", 1910000, 1920000)
	s.entry(t, "f", "c2", "fallback", "mov", 1920000, 1930000)

	if err := db.Exec(context.Background(),
		`UPDATE catalog_item SET video_codec = 'mpeg4', container = 'avi', width = 320, height = 240 WHERE id = 'clip1'`); err != nil {
		t.Fatal(err)
	}

	got := getEPGJSON(t, mux, "/epg?group_tail=1")
	var kinds []string
	for _, e := range got {
		kinds = append(kinds, e["kind"].(string))
	}
	want := []string{"tail", "program", "tail", "tail", "tail", "fallback"}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}

	g := got[0]
	if g["id"] != "a0" || g["start_at"] != 0.0 || g["end_at"] != 30000.0 || g["title"] != "Tail" || g["channel_id"] != "c1" {
		t.Fatalf("group = %v", g)
	}
	if _, has := g["catalog_item_id"]; has {
		t.Error("a group must not claim a single catalog_item_id")
	}
	clips := g["clips"].([]any)
	if len(clips) != 3 {
		t.Fatalf("clips = %d", len(clips))
	}
	for i, c := range clips {
		c := c.(map[string]any)
		wantID := fmt.Sprintf("a%d", i)
		if c["id"] != wantID || c["catalog_item_id"] != fmt.Sprintf("clip%d", i) || c["external_id"] != fmt.Sprintf("jf-clip%d", i) ||
			c["duration_ms"] != 10000.0 || c["start_at"] != float64(i*10000) || c["end_at"] != float64((i+1)*10000) {
			t.Errorf("clip %d = %v", i, c)
		}
	}
	// A clip is played on its own, so it carries the shape of its own file (a
	// player decides "can I decode this?" before it opens one). These seed
	// movies were never probed, so the fields are absent - what matters is that
	// the field is there when the catalog has a value.
	if _, has := clips[0].(map[string]any)["video_codec"]; has {
		t.Errorf("unprobed clip should not claim a codec: %v", clips[0])
	}
	if c1 := clips[1].(map[string]any); c1["video_codec"] != "mpeg4" || c1["container"] != "avi" ||
		c1["width"] != 320.0 || c1["height"] != 240.0 {
		t.Errorf("probed clip lost its shape: %v", c1)
	}
	if n := len(got[2]["clips"].([]any)); n != 2 {
		t.Errorf("second group clips = %d, want 2", n)
	}
	if n := len(got[3]["clips"].([]any)); n != 1 || got[3]["start_at"] != 1900000.0 {
		t.Errorf("after-gap group = %v", got[3])
	}
	if got[4]["channel_id"] != "c2" || len(got[4]["clips"].([]any)) != 1 {
		t.Errorf("other channel group = %v", got[4])
	}
	// Storage is untouched: the clips are still separate rows.
	var counts []int
	if err := db.Query(context.Background(), &counts, "SELECT COUNT(*) FROM epg_entry WHERE kind = 'tail'"); err != nil {
		t.Fatal(err)
	}
	if counts[0] != 7 {
		t.Errorf("stored tail rows = %d, want 7", counts[0])
	}
}

func TestEPGTailGroupOfHundreds(t *testing.T) {
	db, mux := newTestDB(t)
	s := newEPGSeed(t, db)
	const clips = 1200
	for i := 0; i < clips; i++ {
		s.entry(t, fmt.Sprintf("t%04d", i), "c1", "tail", fmt.Sprintf("clip%d", i%3), int64(i)*10000, int64(i+1)*10000)
	}
	s.entry(t, "prog", "c1", "program", "mov", clips*10000, clips*10000+5400000)

	got := getEPGJSON(t, mux, "/epg?channel_id=c1&group_tail=1&start=0&end=99999999")
	if len(got) != 2 {
		t.Fatalf("items = %d, want 2", len(got))
	}
	cs := got[0]["clips"].([]any)
	if len(cs) != clips || got[0]["end_at"] != float64(clips*10000) {
		t.Fatalf("clips = %d, end = %v", len(cs), got[0]["end_at"])
	}
	for i, c := range cs {
		if c.(map[string]any)["id"] != fmt.Sprintf("t%04d", i) {
			t.Fatalf("clip %d out of order: %v", i, c)
		}
	}
}

func TestEPGCacheHeaders(t *testing.T) {
	db, mux := newTestDB(t)
	s := newEPGSeed(t, db)
	s.entry(t, "e1", "c1", "program", "mov", 1000, 2000)

	get := func(etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/epg?channel_id=c1", nil)
		req.Header.Set("Authorization", "Bearer "+testSessionToken)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	first := get("")
	etag := first.Header().Get("ETag")
	if first.Code != 200 || etag == "" || first.Header().Get("Cache-Control") != "no-cache" || first.Header().Get("X-Epg-Revision") != "0" {
		t.Fatalf("headers = %v", first.Header())
	}
	again := get(etag)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("ETag") != etag {
		t.Fatalf("revalidation = %d body=%q", again.Code, again.Body.String())
	}
	if rec := get(`W/"other"`); rec.Code != 200 {
		t.Fatalf("stale validator = %d", rec.Code)
	}

	// A new revision, then a catalog change, each invalidate the validator.
	if err := db.Exec(context.Background(), "INSERT INTO epg_revision (tenant_id, revision, valid_until, updated_at) VALUES (?, 4, 9, 9)", DefaultTenantID); err != nil {
		t.Fatal(err)
	}
	rec := get(etag)
	if rec.Code != 200 || rec.Header().Get("ETag") == etag || rec.Header().Get("X-Epg-Revision") != "4" {
		t.Fatalf("after revision bump: %d %v", rec.Code, rec.Header())
	}
	etag = rec.Header().Get("ETag")
	if err := db.Exec(context.Background(), "UPDATE catalog_item SET title = 'Renamed' WHERE id = 'mov'"); err != nil {
		t.Fatal(err)
	}
	if rec := get(etag); rec.Code != 200 {
		t.Fatalf("catalog rename must invalidate the validator, got %d", rec.Code)
	}
}

func TestEPGPreviewAcceptsGroupTail(t *testing.T) {
	db, mux := newTestDB(t)
	newEPGSeed(t, db)
	// The preview of a channel with nothing configured is empty either way.
	got := getEPGJSON(t, mux, "/channels/c1/epg-preview?group_tail=1")
	if len(got) != 0 {
		t.Fatalf("preview = %v", got)
	}
}

func TestEPGTailGroupIsCompleteAcrossTheWindow(t *testing.T) {
	db, mux := newTestDB(t)
	s := newEPGSeed(t, db)
	s.entry(t, "p0", "c1", "program", "mov", 0, 10000)
	for i := 0; i < 10; i++ { // tail run 10000..110000
		s.entry(t, fmt.Sprintf("t%d", i), "c1", "tail", "clip0", int64(10000+i*10000), int64(20000+i*10000))
	}
	s.entry(t, "p1", "c1", "program", "mov", 110000, 120000)

	// A window that cuts the run in the middle on both sides still gets all
	// ten clips, and the same group id and span as an unbounded request.
	cut := getEPGJSON(t, mux, "/epg?channel_id=c1&group_tail=1&start=45000&end=75000")
	if len(cut) != 1 || len(cut[0]["clips"].([]any)) != 10 || cut[0]["id"] != "t0" ||
		cut[0]["start_at"] != 10000.0 || cut[0]["end_at"] != 110000.0 {
		t.Fatalf("cut window = %v", cut)
	}
	// Cut on the left only, and on the right only.
	left := getEPGJSON(t, mux, "/epg?channel_id=c1&group_tail=1&start=45000&end=115000")
	if len(left) != 2 || len(left[0]["clips"].([]any)) != 10 || left[0]["id"] != "t0" {
		t.Fatalf("left cut = %v", left)
	}
	right := getEPGJSON(t, mux, "/epg?channel_id=c1&group_tail=1&start=0&end=45000")
	if len(right) != 2 || len(right[1]["clips"].([]any)) != 10 {
		t.Fatalf("right cut = %v", right)
	}
	// Without grouping the window is honoured exactly.
	flat := getEPGJSON(t, mux, "/epg?channel_id=c1&start=45000&end=75000")
	if len(flat) != 4 {
		t.Fatalf("flat window = %d entries, want 4", len(flat))
	}
}

func TestEPGGzipForLargeResponses(t *testing.T) {
	db, mux := newTestDB(t)
	s := newEPGSeed(t, db)
	for i := 0; i < 300; i++ {
		s.entry(t, fmt.Sprintf("t%04d", i), "c1", "tail", "clip0", int64(i)*10000, int64(i+1)*10000)
	}
	get := func(acceptEncoding, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/epg?channel_id=c1&group_tail=1", nil)
		req.Header.Set("Authorization", "Bearer "+testSessionToken)
		if acceptEncoding != "" {
			req.Header.Set("Accept-Encoding", acceptEncoding)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	plain := get("", "")
	zipped := get("gzip, deflate", "")
	if plain.Header().Get("Content-Encoding") != "" || zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("encodings = %q / %q", plain.Header().Get("Content-Encoding"), zipped.Header().Get("Content-Encoding"))
	}
	if zipped.Body.Len() >= plain.Body.Len()/2 {
		t.Errorf("gzip did not help: %d vs %d bytes", zipped.Body.Len(), plain.Body.Len())
	}
	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := io.ReadAll(zr)
	if string(decoded) != plain.Body.String() {
		t.Error("gzip body does not decode to the plain body")
	}
	// Each representation has its own validator.
	if plain.Header().Get("ETag") == zipped.Header().Get("ETag") {
		t.Error("plain and gzip representations share an ETag")
	}
	if rec := get("gzip", zipped.Header().Get("ETag")); rec.Code != http.StatusNotModified {
		t.Errorf("gzip revalidation = %d", rec.Code)
	}
	if rec := get("gzip;q=0", ""); rec.Header().Get("Content-Encoding") != "" {
		t.Error("gzip;q=0 must not be compressed")
	}
}
