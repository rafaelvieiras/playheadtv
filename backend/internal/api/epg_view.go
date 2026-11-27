package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// The enriched, optionally tail-grouped view of the materialized EPG served
// by GET /epg and GET /channels/{id}/epg-preview.
//
// Compatibility: every field of domain.EpgEntry keeps its name and meaning;
// the catalog fields below are only ever added. Tail grouping changes the
// shape of the array (a tail group replaces its clips), so it is opt-in via
// `group_tail=1`: a client that does not ask keeps receiving one row per
// stored epg_entry. The stored rows are never grouped, only the response.

// tailGroupTitle is the generic title of a grouped tail item.
const tailGroupTitle = "Tail"

// epgItem is one epg_entry plus the catalog metadata a client needs to show
// and play it without a per-entry lookup. Every catalog field is optional
// (omitted when the item has no value, or the catalog row is gone).
type epgItem struct {
	domain.EpgEntry
	MediaSourceID         *string `json:"media_source_id,omitempty"`
	ExternalID            *string `json:"external_id,omitempty"` // native id in the media source, what the client builds stream/image URLs from
	ItemKind              *string `json:"item_kind,omitempty"`   // catalog kind (movie, episode, ...), distinct from the entry's own `kind`
	Title                 *string `json:"title,omitempty"`
	SeriesTitle           *string `json:"series_title,omitempty"`
	SeasonNumber          *int64  `json:"season_number,omitempty"`
	EpisodeNumber         *int64  `json:"episode_number,omitempty"`
	EpisodeNumberEnd      *int64  `json:"episode_number_end,omitempty"`
	SeriesExternalID      *string `json:"series_external_id,omitempty"`
	DurationMs            *int64  `json:"duration_ms,omitempty"` // the catalog item's own length (an entry may be trimmed: end_at - start_at is what is scheduled)
	ImagePrimaryTag       *string `json:"image_primary_tag,omitempty"`
	ImageBackdropTag      *string `json:"image_backdrop_tag,omitempty"`
	ImageThumbTag         *string `json:"image_thumb_tag,omitempty"`
	SeriesImagePrimaryTag *string `json:"series_image_primary_tag,omitempty"`
	// The primary video file's shape, so a player can decide whether it can
	// decode this at all before it builds a player for it (a set-top box with
	// no MPEG-4 Part 2 decoder can say so instead of failing to open). Every
	// field is optional: an older backend, or an item nobody probed, sends
	// none and the client falls back to trying.
	Width      *int64  `json:"width,omitempty"`
	Height     *int64  `json:"height,omitempty"`
	VideoCodec *string `json:"video_codec,omitempty"` // ffprobe's name: h264, hevc, mpeg4, ...
	Container  *string `json:"container,omitempty"`
	// What the guide shows about the item itself, so a client needs no second
	// request to write a program's details panel: the synopsis the source
	// stored, its age rating, its own year and genres, and the audio side of
	// the same file ("AAC Stereo"). All optional, all omitted when the source
	// did not provide them.
	Overview        *string  `json:"overview,omitempty"`
	ContentRating   *string  `json:"content_rating,omitempty"`
	CommunityRating *float64 `json:"community_rating,omitempty"`
	ProductionYear  *int64   `json:"production_year,omitempty"`
	Genres          *string  `json:"genres,omitempty"` // comma-separated, as the source wrote them
	AudioCodec      *string  `json:"audio_codec,omitempty"`
	AudioChannels   *int64   `json:"audio_channels,omitempty"`
	Bitrate         *int64   `json:"bitrate,omitempty"` // bits per second
	// Watermark is the resolved watermark of the entry (see
	// watermark_resolve.go); omitted when none applies, which is also all an
	// older backend sends.
	Watermark *domain.WatermarkRef `json:"watermark,omitempty"`

	// Requires lists the plugins a client must have installed to play this
	// entry; empty for an item from a source the core talks to directly.
	Requires []pluginRequirement `json:"requires,omitempty"`
}

// epgTailClip is one stored tail entry inside a tail group: all a player
// needs to play it (and show what it is) with no further request.
type epgTailClip struct {
	ID            string  `json:"id"` // the epg_entry id
	CatalogItemID string  `json:"catalog_item_id"`
	ExternalID    *string `json:"external_id,omitempty"`
	Title         *string `json:"title,omitempty"`
	StartAt       int64   `json:"start_at"`
	EndAt         int64   `json:"end_at"`
	DurationMs    *int64  `json:"duration_ms,omitempty"`
	// Same meaning as on epgItem: what the clip's video file is, so a client
	// can tell it cannot decode it before it opens it.
	Width      *int64  `json:"width,omitempty"`
	Height     *int64  `json:"height,omitempty"`
	VideoCodec *string `json:"video_codec,omitempty"`
	Container  *string `json:"container,omitempty"`
	AudioCodec *string `json:"audio_codec,omitempty"`
	// Requires lists the plugins this clip needs, same meaning as on epgItem.
	Requires []pluginRequirement `json:"requires,omitempty"`
}

// epgTailGroup replaces a run of consecutive, back-to-back tail entries of
// one channel. kind is always "tail"; its `clips` (in playing order, never
// empty) are the entries it stands for, ALL of them, also the ones outside
// the requested window (see completeTailEdges): a player can play the whole
// block without asking again, and the same run has the same id and span
// whichever window it was fetched through. id is the first clip's entry id.
// It deliberately has no catalog_item_id: it is not a single catalog item.
type epgTailGroup struct {
	ID        string              `json:"id"`
	TenantID  string              `json:"tenant_id"`
	ChannelID string              `json:"channel_id"`
	Kind      domain.EpgEntryKind `json:"kind"`
	Title     string              `json:"title"`
	StartAt   int64               `json:"start_at"`
	EndAt     int64               `json:"end_at"`
	Clips     []epgTailClip       `json:"clips"`
	// Watermark is resolved from the group's first clip (every clip of a run
	// decorates the same block).
	Watermark *domain.WatermarkRef `json:"watermark,omitempty"`

	// Requires lists the plugins a client must have installed to play this
	// entry; empty for an item from a source the core talks to directly.
	Requires []pluginRequirement `json:"requires,omitempty"`
}

type catalogBrief struct {
	ID             string
	MediaSourceID  string
	ExternalID     string
	ItemKind       string
	Title          string
	SeriesTitle    *string
	DurationMs     int64
	Genres         *string
	ProductionYear *int64
	// Set only when the item's source is a plugin-backed one: what a client
	// needs to know before it can play the entry (docs/PLUGINS.md
	// "Requirements in the guide").
	PluginID         *string
	PluginMinVersion *string
	// PluginConfig is the source's own configuration for that plugin, verbatim.
	// It travels with the entry because the client resolves playback itself: it
	// is the only one that knows what to ask the plugin for.
	PluginConfig *string
	domain.CatalogMetadata
}

// pluginRequirement is one plugin an entry needs to play. It is per entry
// rather than per channel so a single EPG payload (and the client's cached
// copy of it) carries everything a client needs to decide which channels to
// hide: the client aggregates across a channel's entries itself.
type pluginRequirement struct {
	ID         string  `json:"id"`
	MinVersion *string `json:"min_version,omitempty"`
	// Config is what the plugin needs to find the source's items. Plain text on
	// purpose: it is never a secret (docs/PLUGINS.md).
	Config json.RawMessage `json:"config,omitempty"`
}

// blankToNil drops an empty string from an optional field: the columns behind
// these are NOT NULL with an empty default, and "" means "the source did not
// say" just as a null does, so it must not reach the payload as a value.
func blankToNil(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

// catalogLookupChunk bounds the number of ids in one IN (...) list.
const catalogLookupChunk = 400

func loadCatalogBriefs(ctx context.Context, db storage.DB, ids []string) (map[string]catalogBrief, error) {
	out := make(map[string]catalogBrief, len(ids))
	for from := 0; from < len(ids); from += catalogLookupChunk {
		to := min(from+catalogLookupChunk, len(ids))
		chunk := ids[from:to]
		args := make([]any, 0, len(chunk)+1)
		args = append(args, DefaultTenantID)
		for _, id := range chunk {
			args = append(args, id)
		}
		var rows []catalogBrief
		err := db.Query(ctx, &rows,
			"SELECT ci.id, ci.media_source_id, ci.external_id, ci.kind AS item_kind, ci.title, ci.series_title,"+
				" ci.duration_ms, ci.season_number, ci.episode_number, ci.episode_number_end, ci.series_external_id,"+
				" ci.image_primary_tag, ci.image_backdrop_tag, ci.image_thumb_tag, ci.series_image_primary_tag,"+
				" ci.width, ci.height, ci.video_codec, ci.container,"+
				" ci.audio_codec, ci.audio_channels, ci.bitrate,"+
				" ci.overview, ci.content_rating, ci.community_rating, ci.genres, ci.production_year,"+
				" ms.plugin_id, ms.plugin_min_version, ms.plugin_config_json AS plugin_config"+
				" FROM catalog_item ci LEFT JOIN media_source ms ON ms.id = ci.media_source_id"+
				" WHERE ci.tenant_id = ? AND ci.id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+")",
			args...)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.ID] = r
		}
	}
	return out, nil
}

// completeTailEdges makes tail runs cut by the window complete: for each
// channel whose first (last) returned entry is a tail clip, the back-to-back
// tail clips before (after) it that the window left out are added. entries
// must be ordered by channel, then start. Only used when grouping, where a
// run is one item and must not depend on where the window happened to cut it.
func completeTailEdges(ctx context.Context, db storage.DB, entries []domain.EpgEntry) ([]domain.EpgEntry, error) {
	if len(entries) == 0 {
		return entries, nil
	}
	out := make([]domain.EpgEntry, 0, len(entries))
	for from := 0; from < len(entries); {
		to := from
		for to < len(entries) && entries[to].ChannelID == entries[from].ChannelID {
			to++
		}
		seg := entries[from:to]
		first, last := seg[0], seg[len(seg)-1]
		var before, after []domain.EpgEntry
		var err error
		if first.Kind == domain.EpgTail {
			if before, err = adjacentTail(ctx, db, first, true); err != nil {
				return nil, err
			}
		}
		if last.Kind == domain.EpgTail {
			if after, err = adjacentTail(ctx, db, last, false); err != nil {
				return nil, err
			}
		}
		out = append(out, before...)
		out = append(out, seg...)
		out = append(out, after...)
		from = to
	}
	return out, nil
}

// adjacentTail returns the tail clips contiguous with edge, going backwards
// (returned in playing order) or forwards, stopping at the first gap or
// non-tail entry.
func adjacentTail(ctx context.Context, db storage.DB, edge domain.EpgEntry, backwards bool) ([]domain.EpgEntry, error) {
	var rows []domain.EpgEntry
	var err error
	if backwards {
		// Nearest non-tail entry before the edge bounds the search.
		var bound []int64
		if err = db.Query(ctx, &bound,
			"SELECT COALESCE(MAX(end_at), 0) FROM epg_entry WHERE tenant_id = ? AND channel_id = ? AND kind != 'tail' AND end_at <= ?",
			DefaultTenantID, edge.ChannelID, edge.StartAt); err != nil {
			return nil, err
		}
		err = db.Query(ctx, &rows,
			"SELECT "+epgEntryColumns+" FROM epg_entry WHERE tenant_id = ? AND channel_id = ? AND kind = 'tail' AND start_at >= ? AND end_at <= ? ORDER BY start_at DESC",
			DefaultTenantID, edge.ChannelID, bound[0], edge.StartAt)
	} else {
		var bound []int64
		if err = db.Query(ctx, &bound,
			"SELECT COALESCE(MIN(start_at), 9223372036854775807) FROM epg_entry WHERE tenant_id = ? AND channel_id = ? AND kind != 'tail' AND start_at >= ?",
			DefaultTenantID, edge.ChannelID, edge.EndAt); err != nil {
			return nil, err
		}
		err = db.Query(ctx, &rows,
			"SELECT "+epgEntryColumns+" FROM epg_entry WHERE tenant_id = ? AND channel_id = ? AND kind = 'tail' AND start_at >= ? AND end_at <= ? ORDER BY start_at",
			DefaultTenantID, edge.ChannelID, edge.EndAt, bound[0])
	}
	if err != nil {
		return nil, err
	}
	var run []domain.EpgEntry
	cur := edge
	for _, r := range rows {
		if backwards && r.EndAt == cur.StartAt || !backwards && r.StartAt == cur.EndAt {
			run = append(run, r)
			cur = r
			continue
		}
		break
	}
	if backwards {
		for i, j := 0, len(run)-1; i < j; i, j = i+1, j-1 {
			run[i], run[j] = run[j], run[i]
		}
	}
	return run, nil
}

func strPtr(s string) *string { return &s }
func i64Ptr(n int64) *int64   { return &n }

// buildEPGView turns stored entries (ordered by channel, then start) into the
// response array: enriched rows and, when groupTail is set, tail groups.
//
// completeEdges (stored entries only, never a dry-run preview, whose rows are
// not in epg_entry) makes a tail run cut by the requested window complete.
func buildEPGView(ctx context.Context, db storage.DB, entries []domain.EpgEntry, groupTail, completeEdges bool) ([]any, error) {
	if groupTail && completeEdges {
		var err error
		if entries, err = completeTailEdges(ctx, db, entries); err != nil {
			return nil, err
		}
	}
	seen := make(map[string]struct{})
	var ids []string
	for _, e := range entries {
		if _, ok := seen[e.CatalogItemID]; !ok {
			seen[e.CatalogItemID] = struct{}{}
			ids = append(ids, e.CatalogItemID)
		}
	}
	briefs, err := loadCatalogBriefs(ctx, db, ids)
	if err != nil {
		return nil, err
	}

	wms, err := newWatermarkResolver(ctx, db, entries)
	if err != nil {
		return nil, err
	}

	out := make([]any, 0, len(entries))
	var group *epgTailGroup
	var groupLast domain.EpgEntry
	flush := func() {
		if group != nil {
			group.Requires = unionRequirements(group.Clips)
			out = append(out, *group)
			group = nil
		}
	}
	for _, e := range entries {
		b, haveBrief := briefs[e.CatalogItemID]
		if groupTail && e.Kind == domain.EpgTail {
			// Back-to-back tail entries of one channel only: a gap, another
			// kind in between or another channel starts a new group.
			if group != nil && (groupLast.ChannelID != e.ChannelID || groupLast.EndAt != e.StartAt) {
				flush()
			}
			clip := epgTailClip{ID: e.ID, CatalogItemID: e.CatalogItemID, StartAt: e.StartAt, EndAt: e.EndAt}
			if haveBrief {
				clip.ExternalID = strPtr(b.ExternalID)
				clip.Title = strPtr(b.Title)
				clip.DurationMs = i64Ptr(b.DurationMs)
				clip.Width = b.Width
				clip.Height = b.Height
				clip.VideoCodec = b.VideoCodec
				clip.Container = b.Container
				clip.AudioCodec = blankToNil(b.AudioCodec)
				clip.Requires = requirementFor(b)
			}
			if group == nil {
				group = &epgTailGroup{
					ID: e.ID, TenantID: e.TenantID, ChannelID: e.ChannelID, Kind: domain.EpgTail,
					Title: tailGroupTitle, StartAt: e.StartAt,
					Watermark: wms.resolve(e),
				}
			}
			group.Clips = append(group.Clips, clip)
			group.EndAt = e.EndAt
			groupLast = e
			continue
		}
		flush()
		item := epgItem{EpgEntry: e, Watermark: wms.resolve(e)}
		if haveBrief {
			item.MediaSourceID = strPtr(b.MediaSourceID)
			item.ExternalID = strPtr(b.ExternalID)
			item.ItemKind = strPtr(b.ItemKind)
			item.Title = strPtr(b.Title)
			item.SeriesTitle = b.SeriesTitle
			item.SeasonNumber = b.SeasonNumber
			item.EpisodeNumber = b.EpisodeNumber
			item.EpisodeNumberEnd = b.EpisodeNumberEnd
			item.SeriesExternalID = b.SeriesExternalID
			item.DurationMs = i64Ptr(b.DurationMs)
			item.ImagePrimaryTag = b.ImagePrimaryTag
			item.ImageBackdropTag = b.ImageBackdropTag
			item.ImageThumbTag = b.ImageThumbTag
			item.SeriesImagePrimaryTag = b.SeriesImagePrimaryTag
			item.Width = b.Width
			item.Height = b.Height
			item.VideoCodec = b.VideoCodec
			item.Container = b.Container
			item.AudioCodec = blankToNil(b.AudioCodec)
			item.AudioChannels = b.AudioChannels
			item.Bitrate = b.Bitrate
			item.Overview = blankToNil(b.Overview)
			item.ContentRating = blankToNil(b.ContentRating)
			item.CommunityRating = b.CommunityRating
			item.ProductionYear = b.ProductionYear
			item.Genres = blankToNil(b.Genres)
			item.Requires = requirementFor(b)
		}
		out = append(out, item)
	}
	flush()
	return out, nil
}

// unionRequirements is the set of plugins a tail group needs: a run of clips
// can mix sources, and playing any of them needs its own plugin. Order follows
// first appearance so the payload is stable for the same run.
func unionRequirements(clips []epgTailClip) []pluginRequirement {
	var out []pluginRequirement
	seen := make(map[string]bool, len(clips))
	for _, c := range clips {
		for _, r := range c.Requires {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	return out
}

// requirementFor turns a brief's plugin columns into the requirement list a
// client acts on.
func requirementFor(b catalogBrief) []pluginRequirement {
	if b.PluginID == nil || *b.PluginID == "" {
		return nil
	}
	requirement := pluginRequirement{ID: *b.PluginID, MinVersion: b.PluginMinVersion}
	// Only a well-formed object is forwarded: one bad row must not make the whole
	// guide unserialisable.
	if b.PluginConfig != nil && json.Valid([]byte(*b.PluginConfig)) {
		requirement.Config = json.RawMessage(*b.PluginConfig)
	}
	return []pluginRequirement{requirement}
}

func wantsTailGroups(r *http.Request) bool {
	switch strings.ToLower(r.URL.Query().Get("group_tail")) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// currentEPGRevision reads the tenant's epg_revision (0 when none yet).
func currentEPGRevision(ctx context.Context, db storage.DB) (int64, error) {
	var rev domain.EpgRevision
	err := db.Query(ctx, &rev,
		"SELECT tenant_id, revision, valid_until, updated_at FROM epg_revision WHERE tenant_id = ?", DefaultTenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev.Revision, err
}

// writeEPGView serializes view and serves it with a validator, so a client
// (or a CDN in front) can revalidate cheaply: the ETag combines the EPG
// revision with a hash of the exact body, so it also moves when catalog
// metadata changes without a new revision. Cache-Control is `no-cache`:
// storable, but always revalidated (If-None-Match gets a 304 with no body).
// A body over gzipMinBytes is gzip-compressed for a client that accepts it (a
// tail of hours of short clips is the bulk of a large response, and repeats
// itself heavily); the compressed representation has its own `-gzip` ETag
// suffix.
func writeEPGView(w http.ResponseWriter, r *http.Request, revision int64, view []any) {
	body, err := json.Marshal(view)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not encode the guide")
		return
	}
	body = append(body, '\n')
	sum := sha256.Sum256(body)
	etag := `"` + strconv.FormatInt(revision, 10) + "-" + hex.EncodeToString(sum[:8])
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Epg-Revision", strconv.FormatInt(revision, 10))
	h.Add("Vary", "Authorization")
	h.Add("Vary", "Accept-Encoding")
	compress := len(body) > gzipMinBytes && acceptsGzip(r)
	if compress {
		etag += "-gzip"
	}
	etag += `"`
	h.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json")
	if compress {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write(body)
		zw.Close()
		body = buf.Bytes()
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// gzipMinBytes is the response size under which compressing isn't worth it.
const gzipMinBytes = 4096

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(name, "gzip") && !strings.Contains(strings.ReplaceAll(params, " ", ""), "q=0") {
			return true
		}
	}
	return false
}

func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}
