package pullsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// ErrJellyfinAuth marks a request Jellyfin refused for authentication: the API
// key is wrong, revoked, or sent in a form the server no longer accepts. It is
// wrapped by every Jellyfin call here so a caller can tell a rejected key from
// any other failure (errors.Is) and report it as such.
var ErrJellyfinAuth = errors.New("jellyfin rejected the API key")

// newJellyfinRequest builds a GET against baseURL+path carrying the API key
// in the Authorization header, the one form Jellyfin 10.10+ and 12.x both
// accept for a server API key. The legacy X-Emby-Token header and api_key
// query parameter are disabled by default from Jellyfin 12 (401), so no
// request in this package may send them: every Jellyfin call goes through
// here.
func newJellyfinRequest(ctx context.Context, baseURL, path, apiKey string) (*http.Request, error) {
	u := strings.TrimRight(baseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", jellyfinAuthorization(apiKey))
	return req, nil
}

// jellyfinAuthorization is the Authorization header value for an API key.
func jellyfinAuthorization(apiKey string) string {
	return `MediaBrowser Token="` + apiKey + `"`
}

// jellyfinStatusError describes a non-200 answer to what (e.g. "GET /Items").
// A 401 wraps ErrJellyfinAuth, with a message an operator can act on.
func jellyfinStatusError(what string, status int) error {
	if status == http.StatusUnauthorized {
		return fmt.Errorf("jellyfin %s: status %d: %w (check the API key of this media source; it must be a server API key from the Jellyfin dashboard)", what, status, ErrJellyfinAuth)
	}
	return fmt.Errorf("jellyfin %s: status %d", what, status)
}

// jellyfinItem is the subset of Jellyfin's BaseItemDto (GET /Items) this
// package cares about. Field shapes confirmed live against a real Jellyfin
// server during design: RunTimeTicks is in 100ns ticks
// (duration_ms = RunTimeTicks / 10_000); SeriesName is only ever present on
// Episode items; DateCreated is always present, DateLastSaved only once an
// item's metadata has been touched after creation (a fresh library sample
// showed it entirely absent, hence the DateCreated fallback in
// mapJellyfinItem below).
type jellyfinItem struct {
	Id            string   `json:"Id"`
	Name          string   `json:"Name"`
	Type          string   `json:"Type"` // "Movie" | "Episode" | anything else, ignored
	RunTimeTicks  int64    `json:"RunTimeTicks"`
	SeriesName    *string  `json:"SeriesName"`
	DateCreated   *string  `json:"DateCreated"`
	DateLastSaved *string  `json:"DateLastSaved"`
	Genres        []string `json:"Genres"`
	// ProductionYear is always populated for Movie and, per live testing
	// against a real Jellyfin server, already correctly inherited on Episode
	// too — no extra fetch needed for it (unlike Genres below).
	ProductionYear *int64 `json:"ProductionYear"`
	// SeriesId is only ever present on Episode items. Confirmed live: an
	// episode's own Genres always comes back empty even when requested, so
	// genre for episodes has to be resolved from the parent series via this
	// ID (see fetchSeriesGenres).
	SeriesId *string `json:"SeriesId"`

	// Descriptive metadata (issue #35). Field names follow BaseItemDto. Which
	// of these Jellyfin returns by default and which only via the Fields
	// query parameter is NOT verified against a live server here: the Fields
	// list in fetchItemsPage asks for the optional ones explicitly, and every
	// field is decoded leniently (absent means nil/empty).
	IndexNumber         *int64             `json:"IndexNumber"`
	ParentIndexNumber   *int64             `json:"ParentIndexNumber"`
	IndexNumberEnd      *int64             `json:"IndexNumberEnd"`
	SeasonName          *string            `json:"SeasonName"`
	OriginalTitle       *string            `json:"OriginalTitle"`
	SortName            *string            `json:"SortName"`
	PremiereDate        *string            `json:"PremiereDate"`
	Overview            *string            `json:"Overview"`
	Taglines            []string           `json:"Taglines"`
	OfficialRating      *string            `json:"OfficialRating"`
	CommunityRating     *float64           `json:"CommunityRating"`
	CriticRating        *float64           `json:"CriticRating"`
	ProviderIds         map[string]string  `json:"ProviderIds"`
	Studios             []jellyfinNamePair `json:"Studios"`
	Tags                []string           `json:"Tags"`
	People              []jellyfinPerson   `json:"People"`
	ProductionLocations []string           `json:"ProductionLocations"`
	Chapters            []jellyfinChapter  `json:"Chapters"`
	MediaStreams        []jellyfinStream   `json:"MediaStreams"`
	MediaSources        []jellyfinSource   `json:"MediaSources"`
	Container           *string            `json:"Container"`
	Width               *int64             `json:"Width"`
	Height              *int64             `json:"Height"`

	// Art references: image tags only, the backend never fetches images.
	ImageTags             map[string]string `json:"ImageTags"`
	BackdropImageTags     []string          `json:"BackdropImageTags"`
	SeriesPrimaryImageTag *string           `json:"SeriesPrimaryImageTag"`
}

type jellyfinNamePair struct {
	Name string `json:"Name"`
}

type jellyfinPerson struct {
	Name string `json:"Name"`
	Role string `json:"Role"`
	Type string `json:"Type"` // "Actor" | "Director" | "Writer" | ...
}

type jellyfinChapter struct {
	Name               string `json:"Name"`
	StartPositionTicks int64  `json:"StartPositionTicks"`
}

type jellyfinStream struct {
	Type     string `json:"Type"` // "Video" | "Audio" | "Subtitle" | ...
	Codec    string `json:"Codec"`
	Language string `json:"Language"`
	Width    *int64 `json:"Width"`
	Height   *int64 `json:"Height"`
	BitRate  *int64 `json:"BitRate"`
}

type jellyfinSource struct {
	Container string `json:"Container"`
	Bitrate   *int64 `json:"Bitrate"`
}

type jellyfinItemsResponse struct {
	Items            []jellyfinItem `json:"Items"`
	TotalRecordCount int            `json:"TotalRecordCount"`
	StartIndex       int            `json:"StartIndex"`
}

const jellyfinPageSize = 200

// itemFields is the Fields value of the /Items listing: the original set plus
// only what the catalog metadata needs. People, MediaStreams, MediaSources
// and Chapters are the heavy ones; their cost on a large library's first sync
// has not been measured.
const itemFields = "SeriesName,DateCreated,DateLastSaved,RunTimeTicks,Genres,ProductionYear,SeriesId," +
	"Overview,Taglines,OriginalTitle,SortName,ProviderIds,Studios,Tags,People,ProductionLocations,Chapters," +
	"MediaStreams,MediaSources"

// seriesFields is the Fields value of the per-series lookup: what an episode
// can inherit when Jellyfin leaves it empty on the episode itself.
const seriesFields = "Genres,Studios,Tags,People,ProductionLocations"

// maxPages is a defensive ceiling against a pathological server that never
// reports StartIndex+len(Items) >= TotalRecordCount honestly — 500 pages *
// 200 items = 100k items, far beyond any real self-hosted library.
const maxPages = 500

// fetchAllItems pages through GET {baseURL}/Items until every movie/episode
// is fetched, authenticating via the Authorization header (see newJellyfinRequest) — no userId
// required, matching a server-scoped API key with library read access.
func fetchAllItems(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]jellyfinItem, error) {
	var all []jellyfinItem
	start := 0
	for page := 0; page < maxPages; page++ {
		items, total, err := fetchItemsPage(ctx, client, baseURL, apiKey, start, jellyfinPageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		start += len(items)
		if len(items) == 0 || start >= total {
			return all, nil
		}
	}
	return all, fmt.Errorf("pullsync: jellyfin /Items pagination exceeded %d pages", maxPages)
}

func fetchItemsPage(ctx context.Context, client *http.Client, baseURL, apiKey string, startIndex, limit int) ([]jellyfinItem, int, error) {
	req, err := newJellyfinRequest(ctx, baseURL, "/Items", apiKey)
	if err != nil {
		return nil, 0, err
	}
	q := req.URL.Query()
	q.Set("Recursive", "true")
	// Video covers a homevideos-type library (e.g. a "TV" library of
	// bumpers/interstitials/promos used as filler content) — Jellyfin has no
	// dedicated item type for that kind of clip, it just tags it "Video".
	// mapJellyfinItem below maps it to domain.CatalogOther.
	q.Set("IncludeItemTypes", "Movie,Episode,Video")
	q.Set("Fields", itemFields)
	q.Set("StartIndex", strconv.Itoa(startIndex))
	q.Set("Limit", strconv.Itoa(limit))
	req.URL.RawQuery = q.Encode()

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, jellyfinStatusError("GET /Items", resp.StatusCode)
	}
	var parsed jellyfinItemsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, 0, fmt.Errorf("decode jellyfin /Items response: %w", err)
	}
	return parsed.Items, parsed.TotalRecordCount, nil
}

// mapJellyfinItem converts one BaseItemDto into a catalogsync.Item. Returns
// ok=false for item types this cache doesn't model, or missing Id/Name —
// callers skip those rather than failing the whole sync.
//
// series is the seriesID -> series item map fetchSeriesInfo built for this
// sync pass, used only for Episode items: Jellyfin returns no genres on an
// episode (confirmed live) and, in practice, little else the series owns, so
// a field the episode leaves empty is inherited from its series. Inherited:
// genres, studios, tags, content rating, production locations, directors and
// cast (each list only when the episode has none of that kind), and the
// series' primary image tag. Never inherited, because they describe the
// episode itself: overview, ratings, dates, provider ids, numbering.
func mapJellyfinItem(ri jellyfinItem, series map[string]jellyfinItem) (catalogsync.Item, bool) {
	if ri.Id == "" || ri.Name == "" {
		return catalogsync.Item{}, false
	}
	// A non-positive RunTimeTicks (missing/unprobed duration, or a corrupt
	// file Jellyfin couldn't analyze) must never become a playable catalog
	// item: the client's channel-assembly scheduler (see AGENTS.md "channel 1
	// churned through controllers and crashed") assumes every scheduled
	// item's clip has a positive duration and fires its next-item timer
	// immediately for a zero-length one — scheduling even one such item
	// causes a tight loop of real controller creation/disposal that exhausts
	// file descriptors. Skip it here rather than relying on a downstream
	// guard to always catch it.
	if ri.RunTimeTicks <= 0 {
		log.Printf("pullsync: skipping %q (%s): non-positive RunTimeTicks (%d)", ri.Name, ri.Id, ri.RunTimeTicks)
		return catalogsync.Item{}, false
	}
	var kind domain.CatalogItemKind
	switch ri.Type {
	case "Movie":
		kind = domain.CatalogMovie
	case "Episode":
		kind = domain.CatalogEpisode
	case "Video":
		// A homevideos-type library's clips (bumpers, interstitials, promos,
		// station IDs) — no series/genre metadata to speak of, just
		// duration/title, good enough for filler_pool curation.
		kind = domain.CatalogOther
	default:
		return catalogsync.Item{}, false
	}

	item := catalogsync.Item{
		ExternalID:     ri.Id,
		Kind:           kind,
		Title:          ri.Name,
		SeriesTitle:    ri.SeriesName,
		DurationMs:     ri.RunTimeTicks / 10_000,
		ProductionYear: ri.ProductionYear,
	}
	var parent jellyfinItem // zero value when not an episode or the series wasn't resolved
	if kind == domain.CatalogEpisode && ri.SeriesId != nil {
		parent = series[*ri.SeriesId]
	}
	switch {
	case len(ri.Genres) > 0:
		// Defensive: covers Movie always, and Episode on a future Jellyfin
		// version that starts populating it directly.
		item.Genres = strings.Join(ri.Genres, ",")
	default:
		// parent.Genres is nil when the series wasn't resolved (lookup
		// failure, or simply absent from the map) — strings.Join of a nil
		// slice is "", not a panic.
		item.Genres = strings.Join(parent.Genres, ",")
	}
	mapMetadata(&item, ri, parent, kind)
	modified := ri.DateLastSaved
	if modified == nil {
		modified = ri.DateCreated
	}
	if modified != nil {
		if t, err := time.Parse(time.RFC3339, *modified); err == nil {
			ms := t.UnixMilli()
			item.SourceModifiedAt = &ms
		}
	}
	return item, true
}

// mapMetadata fills item's descriptive metadata from ri, falling back to
// parent (the episode's series, zero value otherwise) where documented on
// mapJellyfinItem.
func mapMetadata(item *catalogsync.Item, ri, parent jellyfinItem, kind domain.CatalogItemKind) {
	if kind == domain.CatalogEpisode {
		item.SeasonNumber = ri.ParentIndexNumber
		item.EpisodeNumber = ri.IndexNumber
		item.EpisodeNumberEnd = ri.IndexNumberEnd
		item.SeriesExternalID = nonEmpty(ri.SeriesId)
		item.SeasonName = nonEmpty(ri.SeasonName)
	}
	item.OriginalTitle = nonEmpty(ri.OriginalTitle)
	item.SortTitle = nonEmpty(ri.SortName)
	item.PremiereDate = parseTimeMs(ri.PremiereDate)
	item.DateAdded = parseTimeMs(ri.DateCreated)
	item.Overview = nonEmpty(ri.Overview)
	if len(ri.Taglines) > 0 {
		item.Tagline = nonEmpty(&ri.Taglines[0])
	}
	item.ContentRating = firstNonEmpty(ri.OfficialRating, parent.OfficialRating)
	item.CommunityRating = ri.CommunityRating
	item.CriticRating = ri.CriticRating
	item.ImdbID = providerID(ri.ProviderIds, "Imdb")
	item.TmdbID = providerID(ri.ProviderIds, "Tmdb")
	item.TvdbID = providerID(ri.ProviderIds, "Tvdb")

	// Technical info: item-level values first, then the first video stream
	// and first media source.
	var video *jellyfinStream
	for i := range ri.MediaStreams {
		st := &ri.MediaStreams[i]
		switch strings.ToLower(st.Type) {
		case "video":
			if video == nil {
				video = st
			}
		case "audio":
			item.AudioLanguages = append(item.AudioLanguages, st.Language)
		case "subtitle":
			item.SubtitleLanguages = append(item.SubtitleLanguages, st.Language)
		}
	}
	item.Width, item.Height = ri.Width, ri.Height
	var src *jellyfinSource
	if len(ri.MediaSources) > 0 {
		src = &ri.MediaSources[0]
	}
	item.Container = nonEmpty(ri.Container)
	if video != nil {
		if item.Width == nil {
			item.Width = video.Width
		}
		if item.Height == nil {
			item.Height = video.Height
		}
		item.VideoCodec = nonEmpty(&video.Codec)
	}
	if src != nil {
		if item.Container == nil {
			item.Container = nonEmpty(&src.Container)
		}
		item.Bitrate = src.Bitrate
	}
	if item.Bitrate == nil && video != nil {
		item.Bitrate = video.BitRate
	}

	item.Studios = namePairs(ri.Studios)
	if len(item.Studios) == 0 {
		item.Studios = namePairs(parent.Studios)
	}
	item.Tags = ri.Tags
	if len(item.Tags) == 0 {
		item.Tags = parent.Tags
	}
	item.Locations = ri.ProductionLocations
	if len(item.Locations) == 0 {
		item.Locations = parent.ProductionLocations
	}
	item.Directors = people(ri.People, "Director")
	if len(item.Directors) == 0 {
		item.Directors = people(parent.People, "Director")
	}
	item.Cast = people(ri.People, "Actor")
	if len(item.Cast) == 0 {
		item.Cast = people(parent.People, "Actor")
	}
	for _, c := range ri.Chapters {
		item.Chapters = append(item.Chapters, domain.CatalogChapter{Name: c.Name, StartMs: c.StartPositionTicks / 10_000})
	}

	item.ImagePrimaryTag = nonEmpty(mapTag(ri.ImageTags, "Primary"))
	item.ImageThumbTag = nonEmpty(mapTag(ri.ImageTags, "Thumb"))
	if len(ri.BackdropImageTags) > 0 {
		item.ImageBackdropTag = nonEmpty(&ri.BackdropImageTags[0])
	}
	if kind == domain.CatalogEpisode {
		item.SeriesImagePrimaryTag = firstNonEmpty(ri.SeriesPrimaryImageTag, mapTag(parent.ImageTags, "Primary"))
	}
}

func nonEmpty(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

func firstNonEmpty(vals ...*string) *string {
	for _, v := range vals {
		if r := nonEmpty(v); r != nil {
			return r
		}
	}
	return nil
}

func mapTag(m map[string]string, key string) *string {
	if v, ok := m[key]; ok {
		return &v
	}
	return nil
}

func providerID(m map[string]string, key string) *string {
	// Jellyfin spells the keys "Imdb"/"Tmdb"/"Tvdb", but be lenient on case.
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return nonEmpty(&v)
		}
	}
	return nil
}

func parseTimeMs(s *string) *int64 {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil
	}
	ms := t.UnixMilli()
	return &ms
}

func namePairs(in []jellyfinNamePair) []string {
	var out []string
	for _, p := range in {
		if n := strings.TrimSpace(p.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// people returns the people of one Jellyfin Type. The per-item cap is applied
// by catalogsync (directors first), not here.
func people(in []jellyfinPerson, typ string) []domain.CatalogPerson {
	var out []domain.CatalogPerson
	for _, p := range in {
		if strings.EqualFold(p.Type, typ) && strings.TrimSpace(p.Name) != "" {
			out = append(out, domain.CatalogPerson{Name: p.Name, Type: typ, Role: p.Role})
		}
	}
	return out
}

// jellyfinUser is the subset of Jellyfin's UserDto (GET /Users) needed to
// resolve a userId for endpoints that require one.
type jellyfinUser struct {
	Id string `json:"Id"`
}

// fetchAdminUserID returns the first user Jellyfin reports for this API key,
// used only as a required-but-otherwise-irrelevant userId query parameter
// (confirmed live: GET /Items/{id} 400s without one on this Jellyfin
// version) — no assumption of admin privileges or a specific identity.
func fetchAdminUserID(ctx context.Context, client *http.Client, baseURL, apiKey string) (string, error) {
	req, err := newJellyfinRequest(ctx, baseURL, "/Users", apiKey)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", jellyfinStatusError("GET /Users", resp.StatusCode)
	}
	var users []jellyfinUser
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return "", fmt.Errorf("decode jellyfin /Users response: %w", err)
	}
	if len(users) == 0 {
		return "", fmt.Errorf("pullsync: no jellyfin users found")
	}
	return users[0].Id, nil
}

// fetchSeriesInfo resolves each unique series in seriesIDs (one request per
// series, never per episode), for inheriting onto their episodes — Episode
// genres always come back empty from Jellyfin itself (confirmed live), so
// this is the only way to tag an episode with a genre at all, and it is also
// where the series-level studios, tags, people and so on come from.
//
// Best-effort by design, not fatal: if the userId lookup fails, this logs a
// warning and returns an empty map — episodes simply go without inherited
// data for this sync round rather than the whole sync failing. Likewise, a
// failure fetching one specific series is logged and only that series is left
// out of the returned map.
func fetchSeriesInfo(ctx context.Context, client *http.Client, baseURL, apiKey string, seriesIDs []string) map[string]jellyfinItem {
	result := make(map[string]jellyfinItem)
	if len(seriesIDs) == 0 {
		return result
	}

	userID, err := fetchAdminUserID(ctx, client, baseURL, apiKey)
	if err != nil {
		log.Printf("pullsync: resolve jellyfin userId for series lookup: %v", err)
		return result
	}

	for _, seriesID := range seriesIDs {
		info, err := fetchOneSeries(ctx, client, baseURL, apiKey, userID, seriesID)
		if err != nil {
			log.Printf("pullsync: fetch series %s: %v", seriesID, err)
			continue
		}
		result[seriesID] = info
	}
	return result
}

func fetchOneSeries(ctx context.Context, client *http.Client, baseURL, apiKey, userID, seriesID string) (jellyfinItem, error) {
	req, err := newJellyfinRequest(ctx, baseURL, "/Items/"+seriesID, apiKey)
	if err != nil {
		return jellyfinItem{}, err
	}
	q := req.URL.Query()
	q.Set("Fields", seriesFields)
	q.Set("userId", userID)
	req.URL.RawQuery = q.Encode()

	resp, err := client.Do(req)
	if err != nil {
		return jellyfinItem{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return jellyfinItem{}, jellyfinStatusError("GET /Items/"+seriesID, resp.StatusCode)
	}
	var parsed jellyfinItem
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return jellyfinItem{}, fmt.Errorf("decode jellyfin /Items/%s response: %w", seriesID, err)
	}
	return parsed, nil
}
