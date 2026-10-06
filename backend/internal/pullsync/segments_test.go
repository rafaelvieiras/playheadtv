package pullsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/cutruleset"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func TestMapSegments(t *testing.T) {
	got := mapSegments([]jellyfinSegment{
		{Type: "Intro", StartTicks: 30_000_000, EndTicks: 900_000_000},
		{Type: "Outro", StartTicks: 25_000_000_000, EndTicks: 26_000_000_000},
		{Type: "Recap", StartTicks: 0, EndTicks: 0},                      // empty
		{Type: "Commercial", StartTicks: 500, EndTicks: 100},             // inverted
		{Type: "Unknown", StartTicks: 0, EndTicks: 1_000_000_000},        // not stored
		{Type: "Preview", StartTicks: -100_000, EndTicks: 1_000_000_000}, // negative start
	})
	want := []domain.CatalogSegment{
		{Type: "intro", StartMs: 3000, EndMs: 90000},
		{Type: "outro", StartMs: 2_500_000, EndMs: 2_600_000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapSegments = %+v, want %+v", got, want)
	}
	if got := mapSegments(nil); got == nil || len(got) != 0 {
		t.Fatalf("no segments must map to an empty, non-nil slice, got %#v", got)
	}
}

func TestPickSegmentLookups(t *testing.T) {
	var ids []string
	known := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("i%04d", i)
		ids = append(ids, id)
		known[id] = i >= 10 // the first 10 were never looked up
	}
	first := pickSegmentLookups(ids, known, 0)
	if len(first) != 10+refreshSegmentLookups {
		t.Fatalf("picked %d, want %d", len(first), 10+refreshSegmentLookups)
	}
	for i := 0; i < 10; i++ {
		if first[i] != ids[i] {
			t.Fatalf("never-looked-up items must come first, got %v", first[:12])
		}
	}
	// The refresh window moves from one sync to the next and never repeats within it.
	second := pickSegmentLookups(ids, known, 1)
	if first[10] == second[10] {
		t.Fatalf("the refresh window did not advance")
	}
	if got := pickSegmentLookups(ids[:50], map[string]bool{}, 0); len(got) != 50 {
		t.Fatalf("small library: picked %d, want 50", len(got))
	}
}

// segmentsServer is a fake Jellyfin: /Items lists items, /MediaSegments/{id}
// answers per segmentsByID (a missing id is 200 with no segments), or with
// apiStatus when it is not 200 (e.g. 404 for a server older than 10.10).
type segmentsServer struct {
	mu          sync.Mutex
	items       []jellyfinItem
	segments    map[string][]jellyfinSegment
	apiStatus   int
	segRequests []string
}

func (f *segmentsServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := strings.CutPrefix(r.URL.Path, "/MediaSegments/"); ok {
			f.mu.Lock()
			f.segRequests = append(f.segRequests, id)
			status, segs := f.apiStatus, f.segments[id]
			f.mu.Unlock()
			if status != 0 && status != http.StatusOK {
				http.Error(w, "nope", status)
				return
			}
			json.NewEncoder(w).Encode(jellyfinSegmentsResponse{Items: segs})
			return
		}
		json.NewEncoder(w).Encode(jellyfinItemsResponse{Items: f.items, TotalRecordCount: len(f.items)})
	})
}

func (f *segmentsServer) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.segRequests...)
}

func seedAfterIntroRule(t *testing.T, db *storage.SQLite) {
	t.Helper()
	_, err := cutruleset.Create(context.Background(), db, testTenantID, cutruleset.Input{
		ScopeType: "channel", ScopeID: "c1",
		Rules: []domain.CutRule{{Strategy: domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleAfterIntro}}}},
	})
	if err != nil {
		t.Fatalf("seed cut rule set: %v", err)
	}
}

// storedSegments returns the raw media_segments column of an item and whether
// it is NULL.
func storedSegments(t *testing.T, db *storage.SQLite, ext string) (string, bool) {
	t.Helper()
	var rows []struct{ MediaSegments *string }
	if err := db.Query(context.Background(), &rows, "SELECT media_segments FROM catalog_item WHERE external_id = ?", ext); err != nil || len(rows) != 1 {
		t.Fatalf("load %s: %v (%d rows)", ext, err, len(rows))
	}
	if rows[0].MediaSegments == nil {
		return "", true
	}
	return *rows[0].MediaSegments, false
}

func TestSyncOneStoresIntroSegments(t *testing.T) {
	db := newTestDB(t)
	seedAfterIntroRule(t, db)
	fake := &segmentsServer{
		items: []jellyfinItem{
			{Id: "ep-1", Name: "One", Type: "Episode", RunTimeTicks: 27_000_000_000},
			{Id: "ep-2", Name: "Two", Type: "Episode", RunTimeTicks: 27_000_000_000},
			{Id: "clip", Name: "Bumper", Type: "Video", RunTimeTicks: 300_000_000},
		},
		segments: map[string][]jellyfinSegment{
			"ep-1": {{Type: "Intro", StartTicks: 60_000_000, EndTicks: 1_200_000_000}, {Type: "Outro", StartTicks: 25_000_000_000, EndTicks: 26_500_000_000}},
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	ms := seedJellyfinSource(t, db, "src1", srv.URL, "token")

	if _, err := newSyncer(db).SyncOne(context.Background(), ms); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	var rows []domain.CatalogItem
	if err := db.Query(context.Background(), &rows, "SELECT "+catalogsync.DetailColumns+" FROM catalog_item WHERE external_id = 'ep-1'"); err != nil || len(rows) != 1 {
		t.Fatalf("load ep-1: %v", err)
	}
	if err := catalogsync.LoadRelations(context.Background(), db, testTenantID, rows, true); err != nil {
		t.Fatal(err)
	}
	want := []domain.CatalogSegment{{Type: "intro", StartMs: 6000, EndMs: 120000}, {Type: "outro", StartMs: 2_500_000, EndMs: 2_650_000}}
	if !reflect.DeepEqual(rows[0].Segments, want) {
		t.Fatalf("ep-1 segments = %+v, want %+v", rows[0].Segments, want)
	}
	// Looked up, none: '[]', not NULL.
	if raw, null := storedSegments(t, db, "ep-2"); null || raw != "[]" {
		t.Fatalf("ep-2 media_segments = %q (null %v), want []", raw, null)
	}
	// Only movies and episodes are looked up, one request each.
	reqs := fake.requests()
	if len(reqs) != 2 {
		t.Fatalf("segment requests = %v, want one per movie/episode", reqs)
	}
	if _, null := storedSegments(t, db, "clip"); !null {
		t.Fatalf("a Video item must stay NULL (not looked up)")
	}
}

func TestSyncOneWithoutSegmentsAPILeavesThemEmptyAndSucceeds(t *testing.T) {
	db := newTestDB(t)
	seedAfterIntroRule(t, db)
	fake := &segmentsServer{apiStatus: http.StatusNotFound}
	for i := 0; i < 30; i++ {
		fake.items = append(fake.items, jellyfinItem{Id: fmt.Sprintf("m%02d", i), Name: fmt.Sprintf("Movie %d", i), Type: "Movie", RunTimeTicks: 60_000_000_000})
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	ms := seedJellyfinSource(t, db, "src1", srv.URL, "token")

	res, err := newSyncer(db).SyncOne(context.Background(), ms)
	if err != nil {
		t.Fatalf("SyncOne must not fail without the segments API: %v", err)
	}
	if res.ItemsUpserted != 30 {
		t.Fatalf("upserted %d, want 30", res.ItemsUpserted)
	}
	if n := len(fake.requests()); n > segmentWorkers {
		t.Fatalf("%d segment requests against a server without the API, want at most %d (one per worker in flight)", n, segmentWorkers)
	}
	for _, id := range []string{"m00", "m29"} {
		if _, null := storedSegments(t, db, id); !null {
			t.Fatalf("%s must stay NULL", id)
		}
	}
}

func TestSyncOneSegmentFailureKeepsStoredValue(t *testing.T) {
	db := newTestDB(t)
	seedAfterIntroRule(t, db)
	fake := &segmentsServer{
		items:    []jellyfinItem{{Id: "ep-1", Name: "One", Type: "Episode", RunTimeTicks: 27_000_000_000}},
		segments: map[string][]jellyfinSegment{"ep-1": {{Type: "Intro", StartTicks: 0, EndTicks: 600_000_000}}},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	ms := seedJellyfinSource(t, db, "src1", srv.URL, "token")
	s := newSyncer(db)
	if _, err := s.SyncOne(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	first, _ := storedSegments(t, db, "ep-1")
	if !strings.Contains(first, `"intro"`) {
		t.Fatalf("first sync stored %q", first)
	}
	// The server now errors on the lookup: the stored segments survive.
	fake.mu.Lock()
	fake.apiStatus = http.StatusInternalServerError
	fake.mu.Unlock()
	if _, err := s.SyncOne(context.Background(), ms); err != nil {
		t.Fatalf("SyncOne with a failing lookup: %v", err)
	}
	if again, _ := storedSegments(t, db, "ep-1"); again != first {
		t.Fatalf("stored segments changed to %q after a failed lookup, want %q", again, first)
	}
}

func TestSyncOneSkipsSegmentLookupsWhenNoRuleCanUseThem(t *testing.T) {
	db := newTestDB(t)
	// A set that only uses the chapter middle does not need segments.
	if _, err := cutruleset.Create(context.Background(), db, testTenantID, cutruleset.Input{
		ScopeType: "channel", ScopeID: "c1",
		Rules: []domain.CutRule{{Strategy: domain.CutStrategy{Type: domain.CutChapter, Roles: []domain.ChapterRole{domain.ChapterRoleMiddle}}}},
	}); err != nil {
		t.Fatal(err)
	}
	fake := &segmentsServer{items: []jellyfinItem{{Id: "ep-1", Name: "One", Type: "Episode", RunTimeTicks: 27_000_000_000}}}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	ms := seedJellyfinSource(t, db, "src1", srv.URL, "token")
	if _, err := newSyncer(db).SyncOne(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	if reqs := fake.requests(); len(reqs) != 0 {
		t.Fatalf("segment requests = %v, want none", reqs)
	}
	if _, null := storedSegments(t, db, "ep-1"); !null {
		t.Fatalf("media_segments must stay NULL")
	}
}
