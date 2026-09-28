package pullsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

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
}

type jellyfinItemsResponse struct {
	Items            []jellyfinItem `json:"Items"`
	TotalRecordCount int            `json:"TotalRecordCount"`
	StartIndex       int            `json:"StartIndex"`
}

const jellyfinPageSize = 200

// maxPages is a defensive ceiling against a pathological server that never
// reports StartIndex+len(Items) >= TotalRecordCount honestly — 500 pages *
// 200 items = 100k items, far beyond any real self-hosted library.
const maxPages = 500

// fetchAllItems pages through GET {baseURL}/Items until every movie/episode
// is fetched, authenticating via the X-Emby-Token header — no userId
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
	u := strings.TrimRight(baseURL, "/") + "/Items"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	q := req.URL.Query()
	q.Set("Recursive", "true")
	q.Set("IncludeItemTypes", "Movie,Episode")
	q.Set("Fields", "SeriesName,DateCreated,DateLastSaved,RunTimeTicks,Genres,ProductionYear,SeriesId")
	q.Set("StartIndex", strconv.Itoa(startIndex))
	q.Set("Limit", strconv.Itoa(limit))
	req.URL.RawQuery = q.Encode()
	req.Header.Set("X-Emby-Token", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("jellyfin GET /Items: status %d", resp.StatusCode)
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
// seriesGenres is the seriesID -> genres map fetchSeriesGenres built for this
// sync pass, used only for Episode items (see the field comment on
// jellyfinItem.SeriesId above for why). Movie genres come straight off ri.
func mapJellyfinItem(ri jellyfinItem, seriesGenres map[string][]string) (catalogsync.Item, bool) {
	if ri.Id == "" || ri.Name == "" {
		return catalogsync.Item{}, false
	}
	var kind domain.CatalogItemKind
	switch ri.Type {
	case "Movie":
		kind = domain.CatalogMovie
	case "Episode":
		kind = domain.CatalogEpisode
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
	switch {
	case len(ri.Genres) > 0:
		// Defensive: covers Movie always, and Episode on a future Jellyfin
		// version that starts populating it directly.
		item.Genres = strings.Join(ri.Genres, ",")
	case ri.SeriesId != nil:
		// seriesGenres[*ri.SeriesId] is a nil slice when the series wasn't
		// resolved (lookup failure, or simply absent from the map) —
		// strings.Join of a nil slice is "", not a panic.
		item.Genres = strings.Join(seriesGenres[*ri.SeriesId], ",")
	}
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
	u := strings.TrimRight(baseURL, "/") + "/Users"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Emby-Token", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("jellyfin GET /Users: status %d", resp.StatusCode)
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

// fetchSeriesGenres resolves the genres of each unique series in seriesIDs,
// for inheriting onto their episodes — Episode.Genres always comes back
// empty from Jellyfin itself (confirmed live), so this is the only way to
// tag an episode with a genre at all.
//
// Best-effort by design, not fatal: if the userId lookup fails, this logs a
// warning and returns an empty map — episodes simply go without a genre for
// this sync round rather than the whole sync failing. Likewise, a failure
// fetching one specific series is logged and only that series is left out of
// the returned map.
func fetchSeriesGenres(ctx context.Context, client *http.Client, baseURL, apiKey string, seriesIDs []string) map[string][]string {
	result := make(map[string][]string)
	if len(seriesIDs) == 0 {
		return result
	}

	userID, err := fetchAdminUserID(ctx, client, baseURL, apiKey)
	if err != nil {
		log.Printf("pullsync: resolve jellyfin userId for series genre lookup: %v", err)
		return result
	}

	for _, seriesID := range seriesIDs {
		genres, err := fetchOneSeriesGenres(ctx, client, baseURL, apiKey, userID, seriesID)
		if err != nil {
			log.Printf("pullsync: fetch genres for series %s: %v", seriesID, err)
			continue
		}
		result[seriesID] = genres
	}
	return result
}

func fetchOneSeriesGenres(ctx context.Context, client *http.Client, baseURL, apiKey, userID, seriesID string) ([]string, error) {
	u := strings.TrimRight(baseURL, "/") + "/Items/" + seriesID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("Fields", "Genres")
	q.Set("userId", userID)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("X-Emby-Token", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jellyfin GET /Items/%s: status %d", seriesID, resp.StatusCode)
	}
	var parsed struct {
		Genres []string `json:"Genres"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode jellyfin /Items/%s response: %w", seriesID, err)
	}
	return parsed.Genres, nil
}
