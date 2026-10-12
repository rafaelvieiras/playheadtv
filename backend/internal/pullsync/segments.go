package pullsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/cutplan"
	"github.com/rafaelvieiras/playheadtv/backend/internal/cutruleset"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Media segments (Jellyfin 10.10+, GET /MediaSegments/{itemId}) have no bulk
// endpoint: one request per item. The lookups are therefore bounded:
//
//   - they run only for a tenant with a cut rule that can use them
//     (cutplan.UsesSegments), since nothing else reads the result;
//   - an item never looked up goes first, up to maxNewSegmentLookups per sync;
//   - an item already looked up is refreshed in rotation, refreshSegmentLookups
//     per sync, so a large library is covered over several syncs;
//   - a few workers run at once and the whole phase has a deadline;
//   - a server without the API (or refusing it), or a run of failures, ends the
//     phase. Whatever was not looked up keeps its stored value: segments never
//     fail a sync.
const (
	maxNewSegmentLookups   = 2000
	refreshSegmentLookups  = 200
	segmentWorkers         = 4
	maxSegmentErrorsInARow = 5
	segmentPhaseTimeout    = 60 * time.Second
)

// errSegmentsUnsupported means the server has no media segments API (or does
// not allow this key to read it). A 401 is not this: it is ErrJellyfinAuth.
var errSegmentsUnsupported = errors.New("jellyfin media segments API unavailable")

type jellyfinSegment struct {
	Type       string `json:"Type"`
	StartTicks int64  `json:"StartTicks"`
	EndTicks   int64  `json:"EndTicks"`
}

type jellyfinSegmentsResponse struct {
	Items []jellyfinSegment `json:"Items"`
}

// fetchMediaSegments reads one item's segments. A response without segments is
// an empty, non-nil slice.
func fetchMediaSegments(ctx context.Context, client *http.Client, baseURL, apiKey, itemID string) ([]domain.CatalogSegment, error) {
	req, err := newJellyfinRequest(ctx, baseURL, "/MediaSegments/"+url.PathEscape(itemID), apiKey)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		// Never "unsupported": a rejected key must stay visible.
		return nil, jellyfinStatusError("GET /MediaSegments/"+itemID, resp.StatusCode)
	case http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return nil, fmt.Errorf("status %d: %w", resp.StatusCode, errSegmentsUnsupported)
	default:
		return nil, fmt.Errorf("jellyfin GET /MediaSegments/%s: status %d", itemID, resp.StatusCode)
	}
	var parsed jellyfinSegmentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode jellyfin /MediaSegments/%s response: %w", itemID, err)
	}
	return mapSegments(parsed.Items), nil
}

// mapSegments converts Jellyfin segments (100ns ticks) to catalog segments,
// keeping the known types and dropping empty or inverted ones.
func mapSegments(in []jellyfinSegment) []domain.CatalogSegment {
	out := []domain.CatalogSegment{}
	for _, s := range in {
		typ := strings.ToLower(strings.TrimSpace(s.Type))
		switch typ {
		case domain.SegmentIntro, domain.SegmentOutro, domain.SegmentRecap, domain.SegmentPreview, domain.SegmentCommercial:
		default:
			continue
		}
		start, end := s.StartTicks/10_000, s.EndTicks/10_000
		if start < 0 || end <= start {
			continue
		}
		out = append(out, domain.CatalogSegment{Type: typ, StartMs: start, EndMs: end})
	}
	return out
}

// fetchSegmentsFor looks up the given items with a small worker pool and
// returns the ones that succeeded. It stops early (leaving the rest out) when
// the server lacks the API or too many requests fail in a row.
func fetchSegmentsFor(ctx context.Context, client *http.Client, baseURL, apiKey string, ids []string) map[string][]domain.CatalogSegment {
	result := make(map[string][]domain.CatalogSegment, len(ids))
	if len(ids) == 0 {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, segmentPhaseTimeout)
	defer cancel()

	var (
		mu        sync.Mutex
		successes atomic.Int64
		failures  atomic.Int64 // consecutive
		stopped   atomic.Bool
		logOnce   sync.Once
	)
	stop := func(why error) {
		stopped.Store(true)
		logOnce.Do(func() { log.Printf("pullsync: media segment lookups stopped: %v", why) })
	}
	work := make(chan string)
	var wg sync.WaitGroup
	for w := 0; w < segmentWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				if stopped.Load() || ctx.Err() != nil {
					continue
				}
				segs, err := fetchMediaSegments(ctx, client, baseURL, apiKey, id)
				switch {
				case err == nil:
					successes.Add(1)
					failures.Store(0)
					mu.Lock()
					result[id] = segs
					mu.Unlock()
				case errors.Is(err, ErrJellyfinAuth):
					// The key was accepted for /Items moments ago, so this is a
					// server-wide refusal: stop and say why in the log.
					stop(err)
				case errors.Is(err, errSegmentsUnsupported) && successes.Load() == 0:
					// Before any success this is the server (no such API), not
					// one item.
					stop(err)
				default:
					if failures.Add(1) >= maxSegmentErrorsInARow {
						stop(err)
					}
				}
			}
		}()
	}
	for _, id := range ids {
		work <- id
	}
	close(work)
	wg.Wait()
	return result
}

// pickSegmentLookups chooses which items to look up this sync: every item never
// looked up (up to maxNewSegmentLookups) first, then a rotating window of
// refreshSegmentLookups already-known ones. known maps external id to whether
// the item already has a stored lookup; ids are in remote order. bucket
// advances the rotation from one sync to the next.
func pickSegmentLookups(ids []string, known map[string]bool, bucket int64) []string {
	var fresh, old []string
	for _, id := range ids {
		if known[id] {
			old = append(old, id)
		} else {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) > maxNewSegmentLookups {
		fresh = fresh[:maxNewSegmentLookups]
	}
	if len(old) <= refreshSegmentLookups {
		return append(fresh, old...)
	}
	start := int((bucket * refreshSegmentLookups) % int64(len(old)))
	for i := 0; i < refreshSegmentLookups; i++ {
		fresh = append(fresh, old[(start+i)%len(old)])
	}
	return fresh
}

// tenantUsesSegments reports whether any of the tenant's cut rule sets can use
// media segments. A failing read counts as no (segments are an optimization
// of cut placement, never a reason to fail a sync).
func tenantUsesSegments(ctx context.Context, db storage.DB, tenantID string) bool {
	sets, err := cutruleset.List(ctx, db, tenantID)
	if err != nil {
		log.Printf("pullsync: tenant %s: read cut rule sets for media segments: %v", tenantID, err)
		return false
	}
	for _, set := range sets {
		if cutplan.UsesSegments(set.CutConfig) {
			return true
		}
	}
	return false
}

// resolveSegments returns the segments to store for each remote item, keyed by
// external id: freshly looked up ones, else what the row already holds (nil for
// an item never looked up, which stores NULL). Never returns an error.
func (s *Syncer) resolveSegments(ctx context.Context, ms domain.MediaSource, apiKey string, remote []jellyfinItem, now int64) map[string][]domain.CatalogSegment {
	out := map[string][]domain.CatalogSegment{}
	var rows []struct {
		ExternalID    string
		MediaSegments *string
	}
	if err := s.db.Query(ctx, &rows, "SELECT external_id, media_segments FROM catalog_item WHERE media_source_id = ?", ms.ID); err != nil {
		log.Printf("pullsync: media source %s: read stored media segments: %v", ms.ID, err)
		return out
	}
	known := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.MediaSegments == nil {
			continue
		}
		var segs []domain.CatalogSegment
		if err := json.Unmarshal([]byte(*r.MediaSegments), &segs); err != nil {
			continue
		}
		known[r.ExternalID] = true
		out[r.ExternalID] = segs
	}
	if !tenantUsesSegments(ctx, s.db, ms.TenantID) {
		return out
	}
	var ids []string
	for _, ri := range remote {
		if (ri.Type == "Movie" || ri.Type == "Episode") && ri.Id != "" && ri.RunTimeTicks > 0 {
			ids = append(ids, ri.Id)
		}
	}
	bucket := now / int64(pullInterval/time.Millisecond)
	fetched := fetchSegmentsFor(ctx, s.client, *ms.BaseURL, apiKey, pickSegmentLookups(ids, known, bucket))
	for id, segs := range fetched {
		out[id] = segs
	}
	return out
}
