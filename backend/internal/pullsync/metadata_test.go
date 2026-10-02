package pullsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// Realistic BaseItemDto payloads (trimmed to the fields this package reads).
const episodePayload = `{
  "Id": "ep-1", "Name": "The Pilot", "Type": "Episode", "RunTimeTicks": 13000000000,
  "SeriesName": "Some Show", "SeriesId": "series-1", "SeasonName": "Season 1",
  "ParentIndexNumber": 1, "IndexNumber": 3, "IndexNumberEnd": 4,
  "DateCreated": "2024-02-03T04:05:06.0000000Z", "PremiereDate": "2020-01-15T00:00:00.0000000Z",
  "ProductionYear": 2020, "Genres": [], "Overview": "The first one.", "OriginalTitle": "Der Pilot",
  "SortName": "pilot", "CommunityRating": 8.4,
  "ProviderIds": {"Imdb": "tt0001", "Tmdb": "42", "Tvdb": "777"},
  "People": [
    {"Name": "Dana Director", "Type": "Director", "Id": "p1"},
    {"Name": "Ana Actor", "Role": "Hero", "Type": "Actor", "PrimaryImageTag": "x"},
    {"Name": "Wally Writer", "Type": "Writer"}
  ],
  "Chapters": [{"Name": "Intro", "StartPositionTicks": 0}, {"Name": "Act 1", "StartPositionTicks": 600000000}],
  "MediaStreams": [
    {"Type": "Video", "Codec": "h264", "Width": 1920, "Height": 1080, "BitRate": 4000000},
    {"Type": "Audio", "Codec": "aac", "Language": "eng"},
    {"Type": "Audio", "Codec": "ac3", "Language": "por"},
    {"Type": "Subtitle", "Codec": "srt", "Language": "eng"}
  ],
  "MediaSources": [{"Container": "mkv", "Bitrate": 5000000}],
  "ImageTags": {"Primary": "pTag", "Thumb": "tTag"}, "BackdropImageTags": ["bTag"],
  "SeriesPrimaryImageTag": "sTag"
}`

const seriesPayload = `{
  "Id": "series-1", "Name": "Some Show", "Type": "Series",
  "Genres": ["Comedy", "Drama"], "OfficialRating": "TV-14",
  "Studios": [{"Name": "Acme Studio", "Id": "s1"}], "Tags": ["cult"],
  "ProductionLocations": ["Brazil"],
  "People": [
    {"Name": "Sam Showrunner", "Type": "Director"},
    {"Name": "Series Star", "Role": "Lead", "Type": "Actor"}
  ],
  "ImageTags": {"Primary": "seriesPrimary"}
}`

func decodeItem(t *testing.T, payload string) jellyfinItem {
	t.Helper()
	var it jellyfinItem
	if err := json.Unmarshal([]byte(payload), &it); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return it
}

func TestMapJellyfinItem_EpisodeMetadataAndInheritance(t *testing.T) {
	ep := decodeItem(t, episodePayload)
	series := map[string]jellyfinItem{"series-1": decodeItem(t, seriesPayload)}
	item, ok := mapJellyfinItem(ep, series)
	if !ok {
		t.Fatal("ok = false")
	}
	if item.SeasonNumber == nil || *item.SeasonNumber != 1 || *item.EpisodeNumber != 3 || *item.EpisodeNumberEnd != 4 {
		t.Errorf("numbering = %v/%v/%v", item.SeasonNumber, item.EpisodeNumber, item.EpisodeNumberEnd)
	}
	if item.SeriesExternalID == nil || *item.SeriesExternalID != "series-1" || *item.SeasonName != "Season 1" {
		t.Errorf("series/season refs = %v %v", item.SeriesExternalID, item.SeasonName)
	}
	if *item.ImdbID != "tt0001" || *item.TmdbID != "42" || *item.TvdbID != "777" {
		t.Errorf("provider ids = %v %v %v", item.ImdbID, item.TmdbID, item.TvdbID)
	}
	if item.PremiereDate == nil || *item.PremiereDate != 1579046400000 {
		t.Errorf("PremiereDate = %v", item.PremiereDate)
	}
	if item.DateAdded == nil || *item.DateAdded != 1706933106000 {
		t.Errorf("DateAdded = %v", item.DateAdded)
	}
	if *item.Overview != "The first one." || *item.CommunityRating != 8.4 || *item.OriginalTitle != "Der Pilot" || *item.SortTitle != "pilot" {
		t.Errorf("episode own scalars wrong: %+v", item.CatalogMetadata)
	}
	if *item.Width != 1920 || *item.Height != 1080 || *item.VideoCodec != "h264" || *item.Container != "mkv" || *item.Bitrate != 5_000_000 {
		t.Errorf("technical info wrong: %v %v %v %v %v", item.Width, item.Height, item.VideoCodec, item.Container, item.Bitrate)
	}
	if strings.Join(item.AudioLanguages, ",") != "eng,por" || strings.Join(item.SubtitleLanguages, ",") != "eng" {
		t.Errorf("languages = %v / %v", item.AudioLanguages, item.SubtitleLanguages)
	}
	if len(item.Chapters) != 2 || item.Chapters[1].StartMs != 60_000 {
		t.Errorf("chapters = %+v", item.Chapters)
	}
	// Episode's own people win per kind, writers are ignored.
	if len(item.Directors) != 1 || item.Directors[0].Name != "Dana Director" || len(item.Cast) != 1 || item.Cast[0].Role != "Hero" {
		t.Errorf("people = %+v / %+v", item.Directors, item.Cast)
	}
	// Inherited from the series.
	if item.Genres != "Comedy,Drama" || *item.ContentRating != "TV-14" ||
		strings.Join(item.Studios, ",") != "Acme Studio" || strings.Join(item.Tags, ",") != "cult" ||
		strings.Join(item.Locations, ",") != "Brazil" {
		t.Errorf("inheritance wrong: genres=%q rating=%v studios=%v tags=%v locs=%v", item.Genres, item.ContentRating, item.Studios, item.Tags, item.Locations)
	}
	if *item.ImagePrimaryTag != "pTag" || *item.ImageThumbTag != "tTag" || *item.ImageBackdropTag != "bTag" || *item.SeriesImagePrimaryTag != "sTag" {
		t.Errorf("image tags wrong")
	}
}

func TestMapJellyfinItem_EpisodeInheritsPeopleAndSeriesImageFallback(t *testing.T) {
	ep := decodeItem(t, `{"Id":"ep-2","Name":"Two","Type":"Episode","RunTimeTicks":1000000000,"SeriesId":"series-1"}`)
	series := map[string]jellyfinItem{"series-1": decodeItem(t, seriesPayload)}
	item, _ := mapJellyfinItem(ep, series)
	if len(item.Directors) != 1 || item.Directors[0].Name != "Sam Showrunner" || len(item.Cast) != 1 || item.Cast[0].Name != "Series Star" {
		t.Errorf("people = %+v / %+v", item.Directors, item.Cast)
	}
	if item.SeriesImagePrimaryTag == nil || *item.SeriesImagePrimaryTag != "seriesPrimary" {
		t.Errorf("series image fallback = %v", item.SeriesImagePrimaryTag)
	}
	// Episode-specific facts are never inherited.
	if item.Overview != nil || item.CommunityRating != nil || item.ImdbID != nil || item.PremiereDate != nil {
		t.Errorf("episode-only fields were inherited: %+v", item.CatalogMetadata)
	}
}

func TestMapJellyfinItem_MovieHasNoEpisodeFields(t *testing.T) {
	m := decodeItem(t, `{"Id":"m1","Name":"Movie","Type":"Movie","RunTimeTicks":1000000000,"IndexNumber":9,"ParentIndexNumber":2,"OfficialRating":"PG","Taglines":["Tag line."]}`)
	item, _ := mapJellyfinItem(m, nil)
	if item.SeasonNumber != nil || item.EpisodeNumber != nil || item.SeriesExternalID != nil {
		t.Errorf("movie got episode numbering: %+v", item.CatalogMetadata)
	}
	if *item.ContentRating != "PG" || *item.Tagline != "Tag line." {
		t.Errorf("movie scalars wrong")
	}
}

func TestSyncOneStoresMetadataOneSeriesFetchAndResyncUpdates(t *testing.T) {
	db := newTestDB(t)
	var seriesFetches atomic.Int32
	var gotFields string
	items := []jellyfinItem{
		decodeItem(t, episodePayload),
		decodeItem(t, `{"Id":"ep-2","Name":"Two","Type":"Episode","RunTimeTicks":1000000000,"SeriesId":"series-1","SeriesName":"Some Show","ParentIndexNumber":0,"IndexNumber":1}`),
		decodeItem(t, `{"Id":"ep-3","Name":"Three","Type":"Episode","RunTimeTicks":1000000000,"SeriesId":"series-1","SeriesName":"Some Show"}`),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users":
			json.NewEncoder(w).Encode([]jellyfinUser{{Id: "user-1"}})
		case "/Items/series-1":
			seriesFetches.Add(1)
			w.Write([]byte(seriesPayload))
		default:
			gotFields = r.URL.Query().Get("Fields")
			json.NewEncoder(w).Encode(jellyfinItemsResponse{Items: items, TotalRecordCount: len(items)})
		}
	}))
	defer srv.Close()

	ms := seedJellyfinSource(t, db, "src1", srv.URL, "token")
	s := newSyncer(db)
	if _, err := s.SyncOne(context.Background(), ms); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}
	if seriesFetches.Load() != 1 {
		t.Errorf("series fetches = %d, want 1 for 3 episodes of one series", seriesFetches.Load())
	}
	for _, f := range []string{"People", "MediaStreams", "Chapters", "Studios", "ProviderIds", "Overview"} {
		if !strings.Contains(gotFields, f) {
			t.Errorf("Fields %q lacks %s", gotFields, f)
		}
	}

	load := func(ext string) domain.CatalogItem {
		var rows []domain.CatalogItem
		if err := db.Query(context.Background(), &rows, "SELECT "+catalogsync.DetailColumns+" FROM catalog_item WHERE external_id = ?", ext); err != nil || len(rows) != 1 {
			t.Fatalf("load %s: %v (%d rows)", ext, err, len(rows))
		}
		if err := catalogsync.LoadRelations(context.Background(), db, testTenantID, rows, true); err != nil {
			t.Fatal(err)
		}
		return rows[0]
	}
	ep1 := load("ep-1")
	if *ep1.SeasonNumber != 1 || len(ep1.Cast) != 1 || ep1.Cast[0].Name != "Ana Actor" || len(ep1.Chapters) != 2 {
		t.Errorf("ep-1 stored wrong: %+v", ep1)
	}
	if ep2 := load("ep-2"); ep2.SeasonNumber == nil || *ep2.SeasonNumber != 0 {
		t.Errorf("ep-2 season = %v, want 0 (specials) kept distinct from nil", ep2.SeasonNumber)
	}
	if ep3 := load("ep-3"); ep3.SeasonNumber != nil || len(ep3.Studios) != 1 {
		t.Errorf("ep-3 = season %v studios %v", ep3.SeasonNumber, ep3.Studios)
	}

	// Re-sync with ep-1 changed: new cast list, no chapters, ep-3 removed.
	items[0] = decodeItem(t, strings.Replace(strings.Replace(episodePayload,
		`{"Name": "Ana Actor", "Role": "Hero", "Type": "Actor", "PrimaryImageTag": "x"}`,
		`{"Name": "New Actor", "Role": "Sidekick", "Type": "Actor"}`, 1),
		`"Chapters": [{"Name": "Intro", "StartPositionTicks": 0}, {"Name": "Act 1", "StartPositionTicks": 600000000}],`, "", 1))
	items = items[:2]
	res, err := s.SyncOne(context.Background(), ms)
	if err != nil {
		t.Fatalf("SyncOne #2: %v", err)
	}
	if res.ItemsDeleted != 1 {
		t.Errorf("ItemsDeleted = %d, want 1", res.ItemsDeleted)
	}
	ep1 = load("ep-1")
	if len(ep1.Cast) != 1 || ep1.Cast[0].Name != "New Actor" || len(ep1.Chapters) != 0 {
		t.Errorf("ep-1 after re-sync: cast=%+v chapters=%+v", ep1.Cast, ep1.Chapters)
	}
	var links []int
	db.Query(context.Background(), &links, "SELECT COUNT(*) FROM catalog_item_person")
	if links[0] != 4 { // ep-1: director + actor, ep-2: inherited director + actor
		t.Errorf("person links = %d, want 4", links[0])
	}
}
