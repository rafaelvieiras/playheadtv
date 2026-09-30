package pullsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

func strPtr(s string) *string { return &s }

func int64Ptr(n int64) *int64 { return &n }

func TestMapJellyfinItem_Movie(t *testing.T) {
	item, ok := mapJellyfinItem(jellyfinItem{
		Id:           "movie-1",
		Name:         "A Movie",
		Type:         "Movie",
		RunTimeTicks: 65_316_370_000, // 6531.637s
	}, nil)
	if !ok {
		t.Fatal("mapJellyfinItem(Movie) ok = false, want true")
	}
	if item.Kind != domain.CatalogMovie {
		t.Errorf("Kind = %q, want %q", item.Kind, domain.CatalogMovie)
	}
	if item.DurationMs != 6_531_637 {
		t.Errorf("DurationMs = %d, want 6531637", item.DurationMs)
	}
	if item.SeriesTitle != nil {
		t.Errorf("SeriesTitle = %v, want nil", item.SeriesTitle)
	}
}

func TestMapJellyfinItem_MovieGenresAndProductionYear(t *testing.T) {
	item, ok := mapJellyfinItem(jellyfinItem{
		Id:             "movie-1",
		Name:           "A Movie",
		Type:           "Movie",
		Genres:         []string{"Comedy", "Action"},
		ProductionYear: int64Ptr(2019),
	}, nil)
	if !ok {
		t.Fatal("mapJellyfinItem(Movie) ok = false, want true")
	}
	if item.Genres != "Comedy,Action" {
		t.Errorf("Genres = %q, want %q", item.Genres, "Comedy,Action")
	}
	if item.ProductionYear == nil || *item.ProductionYear != 2019 {
		t.Errorf("ProductionYear = %v, want 2019", item.ProductionYear)
	}
}

func TestMapJellyfinItem_Episode(t *testing.T) {
	item, ok := mapJellyfinItem(jellyfinItem{
		Id:           "ep-1",
		Name:         "Pilot",
		Type:         "Episode",
		RunTimeTicks: 12_000_000_000,
		SeriesName:   strPtr("Some Show"),
	}, nil)
	if !ok {
		t.Fatal("mapJellyfinItem(Episode) ok = false, want true")
	}
	if item.Kind != domain.CatalogEpisode {
		t.Errorf("Kind = %q, want %q", item.Kind, domain.CatalogEpisode)
	}
	if item.SeriesTitle == nil || *item.SeriesTitle != "Some Show" {
		t.Errorf("SeriesTitle = %v, want \"Some Show\"", item.SeriesTitle)
	}
}

func TestMapJellyfinItem_EpisodeInheritsSeriesGenres(t *testing.T) {
	seriesGenres := map[string][]string{"series-1": {"Comedy", "Romance"}}
	item, ok := mapJellyfinItem(jellyfinItem{
		Id:             "ep-1",
		Name:           "Pilot",
		Type:           "Episode",
		SeriesName:     strPtr("Some Show"),
		SeriesId:       strPtr("series-1"),
		ProductionYear: int64Ptr(2020), // already inherited by Jellyfin itself on episodes
	}, seriesGenres)
	if !ok {
		t.Fatal("mapJellyfinItem(Episode) ok = false, want true")
	}
	if item.Genres != "Comedy,Romance" {
		t.Errorf("Genres = %q, want %q", item.Genres, "Comedy,Romance")
	}
	if item.ProductionYear == nil || *item.ProductionYear != 2020 {
		t.Errorf("ProductionYear = %v, want 2020", item.ProductionYear)
	}
}

func TestMapJellyfinItem_EpisodeOwnGenresPreferredOverSeries(t *testing.T) {
	// Defensive path: a future Jellyfin version populating Episode.Genres
	// directly should win over the series lookup, not be overwritten by it.
	seriesGenres := map[string][]string{"series-1": {"Comedy"}}
	item, ok := mapJellyfinItem(jellyfinItem{
		Id:       "ep-1",
		Name:     "Pilot",
		Type:     "Episode",
		Genres:   []string{"Drama"},
		SeriesId: strPtr("series-1"),
	}, seriesGenres)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if item.Genres != "Drama" {
		t.Errorf("Genres = %q, want %q (item's own genres, not the series')", item.Genres, "Drama")
	}
}

func TestMapJellyfinItem_EpisodeSeriesNotInMapNoPanic(t *testing.T) {
	item, ok := mapJellyfinItem(jellyfinItem{
		Id:       "ep-1",
		Name:     "Pilot",
		Type:     "Episode",
		SeriesId: strPtr("unknown-series"),
	}, map[string][]string{})
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if item.Genres != "" {
		t.Errorf("Genres = %q, want empty string", item.Genres)
	}
}

func TestMapJellyfinItem_Video(t *testing.T) {
	item, ok := mapJellyfinItem(jellyfinItem{
		Id:           "video-1",
		Name:         "Fox Kids - Bumper - 2002 - Logo Girando",
		Type:         "Video",
		RunTimeTicks: 51_000_000, // 5.1s
	}, nil)
	if !ok {
		t.Fatal("mapJellyfinItem(Video) ok = false, want true")
	}
	if item.Kind != domain.CatalogOther {
		t.Errorf("Kind = %q, want %q", item.Kind, domain.CatalogOther)
	}
	if item.DurationMs != 5_100 {
		t.Errorf("DurationMs = %d, want 5100", item.DurationMs)
	}
}

func TestMapJellyfinItem_UnknownTypeSkipped(t *testing.T) {
	_, ok := mapJellyfinItem(jellyfinItem{Id: "s-1", Name: "Some Show", Type: "Series"}, nil)
	if ok {
		t.Error("mapJellyfinItem(Series) ok = true, want false (not a playable item)")
	}
}

func TestMapJellyfinItem_MissingIDOrNameSkipped(t *testing.T) {
	if _, ok := mapJellyfinItem(jellyfinItem{Name: "No Id", Type: "Movie"}, nil); ok {
		t.Error("mapJellyfinItem(no Id) ok = true, want false")
	}
	if _, ok := mapJellyfinItem(jellyfinItem{Id: "x", Type: "Movie"}, nil); ok {
		t.Error("mapJellyfinItem(no Name) ok = true, want false")
	}
}

func TestMapJellyfinItem_SourceModifiedAtFallback(t *testing.T) {
	item, ok := mapJellyfinItem(jellyfinItem{
		Id: "m-1", Name: "M", Type: "Movie",
		DateLastSaved: nil,
		DateCreated:   strPtr("2024-01-02T03:04:05Z"),
	}, nil)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if item.SourceModifiedAt == nil {
		t.Fatal("SourceModifiedAt = nil, want fallback to DateCreated")
	}

	item, ok = mapJellyfinItem(jellyfinItem{Id: "m-2", Name: "M2", Type: "Movie"}, nil)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if item.SourceModifiedAt != nil {
		t.Errorf("SourceModifiedAt = %v, want nil (both dates absent)", item.SourceModifiedAt)
	}
}

// TestFetchAllItems_Pagination proves fetchAllItems drives the loop off
// StartIndex/TotalRecordCount, not off the server actually honoring Limit —
// this fake deliberately always returns exactly 2 items per call regardless
// of the requested Limit.
func TestFetchAllItems_Pagination(t *testing.T) {
	const total = 5
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		start := 0
		fmt.Sscanf(r.URL.Query().Get("StartIndex"), "%d", &start)
		items := []jellyfinItem{}
		for i := start; i < start+2 && i < total; i++ {
			items = append(items, jellyfinItem{Id: fmt.Sprintf("item-%d", i), Name: fmt.Sprintf("Item %d", i), Type: "Movie"})
		}
		json.NewEncoder(w).Encode(jellyfinItemsResponse{Items: items, TotalRecordCount: total, StartIndex: start})
	}))
	defer srv.Close()

	items, err := fetchAllItems(context.Background(), srv.Client(), srv.URL, "token")
	if err != nil {
		t.Fatalf("fetchAllItems: %v", err)
	}
	if len(items) != total {
		t.Fatalf("len(items) = %d, want %d", len(items), total)
	}
	for i, it := range items {
		if it.Id != fmt.Sprintf("item-%d", i) {
			t.Errorf("items[%d].Id = %q, want %q (out of order)", i, it.Id, fmt.Sprintf("item-%d", i))
		}
	}
	if requests != 3 { // 2+2+1
		t.Errorf("requests = %d, want 3", requests)
	}
}

func TestFetchAllItems_AuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "the-real-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(jellyfinItemsResponse{})
	}))
	defer srv.Close()

	if _, err := fetchAllItems(context.Background(), srv.Client(), srv.URL, "wrong-token"); err == nil {
		t.Error("fetchAllItems with wrong token succeeded, want error (401)")
	}
	if _, err := fetchAllItems(context.Background(), srv.Client(), srv.URL, "the-real-token"); err != nil {
		t.Errorf("fetchAllItems with correct token: %v", err)
	}
}

// TestFetchAllItems_PaginationGuardAgainstRunawayServer proves maxPages
// bounds the loop against a server that never reports the end honestly and
// never returns an empty page.
func TestFetchAllItems_PaginationGuardAgainstRunawayServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jellyfinItemsResponse{
			Items:            []jellyfinItem{{Id: "x", Name: "X", Type: "Movie"}},
			TotalRecordCount: 1_000_000_000,
		})
	}))
	defer srv.Close()

	if _, err := fetchAllItems(context.Background(), srv.Client(), srv.URL, "token"); err == nil {
		t.Error("fetchAllItems against a runaway server succeeded, want a maxPages error")
	}
}

// jellyfinFakeServer wires up the two endpoints fetchSeriesGenres needs:
// GET /Users (returns the given users) and GET /Items/{id} (returns genres
// for the id, from the seriesGenres map, and records whether userId was
// passed on the query string).
func jellyfinFakeServer(t *testing.T, users []jellyfinUser, seriesGenres map[string][]string) (*httptest.Server, *[]string) {
	t.Helper()
	var gotUserIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users":
			json.NewEncoder(w).Encode(users)
		case strings.HasPrefix(r.URL.Path, "/Items/"):
			gotUserIDs = append(gotUserIDs, r.URL.Query().Get("userId"))
			id := strings.TrimPrefix(r.URL.Path, "/Items/")
			genres, ok := seriesGenres[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(struct {
				Genres []string `json:"Genres"`
			}{Genres: genres})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, &gotUserIDs
}

func TestFetchSeriesGenres_HappyPath(t *testing.T) {
	srv, gotUserIDs := jellyfinFakeServer(t,
		[]jellyfinUser{{Id: "user-1"}},
		map[string][]string{"series-1": {"Comedy", "Romance"}},
	)
	defer srv.Close()

	result := fetchSeriesGenres(context.Background(), srv.Client(), srv.URL, "token", []string{"series-1"})
	if got := strings.Join(result["series-1"], ","); got != "Comedy,Romance" {
		t.Errorf("result[series-1] = %q, want %q", got, "Comedy,Romance")
	}
	if len(*gotUserIDs) != 1 || (*gotUserIDs)[0] != "user-1" {
		t.Errorf("userId sent to /Items = %v, want [user-1]", *gotUserIDs)
	}
}

func TestFetchSeriesGenres_SeriesNotFoundExcludedFromMap(t *testing.T) {
	srv, _ := jellyfinFakeServer(t,
		[]jellyfinUser{{Id: "user-1"}},
		map[string][]string{"series-1": {"Comedy"}},
	)
	defer srv.Close()

	result := fetchSeriesGenres(context.Background(), srv.Client(), srv.URL, "token", []string{"series-1", "series-missing"})
	if _, ok := result["series-missing"]; ok {
		t.Error("result contains series-missing, want it excluded (fetch for it failed)")
	}
	if got := strings.Join(result["series-1"], ","); got != "Comedy" {
		t.Errorf("result[series-1] = %q, want %q", got, "Comedy")
	}
}

func TestFetchSeriesGenres_NoUsersReturnsEmptyMap(t *testing.T) {
	srv, _ := jellyfinFakeServer(t, []jellyfinUser{}, nil)
	defer srv.Close()

	result := fetchSeriesGenres(context.Background(), srv.Client(), srv.URL, "token", []string{"series-1"})
	if len(result) != 0 {
		t.Errorf("result = %v, want empty map when /Users has no users", result)
	}
}

func TestFetchSeriesGenres_EmptySeriesIDsSkipsUsersCall(t *testing.T) {
	usersCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users" {
			usersCalled = true
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	result := fetchSeriesGenres(context.Background(), srv.Client(), srv.URL, "token", nil)
	if len(result) != 0 {
		t.Errorf("result = %v, want empty map", result)
	}
	if usersCalled {
		t.Error("GET /Users was called with no seriesIDs to resolve, want it skipped")
	}
}
