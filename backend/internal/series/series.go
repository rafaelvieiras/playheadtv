// Package series resolves series block items: it validates their stored spec,
// lists the series a catalog holds, and loads the episodes a series item
// expands to. Playback order itself (which episode airs next) lives in the
// materializer; this package only reads the catalog.
package series

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Episode is the slice of a catalog episode the materializer needs.
type Episode struct {
	ID            string
	Title         string
	DurationMs    int64
	SeasonNumber  *int64
	EpisodeNumber *int64
	PremiereDate  *int64
	// GroupKey and GroupTitle identify the series an item belongs to for an
	// interleaved walk (see blockitem.Episodes); empty for the episodes of a
	// series item, which have no use for them.
	GroupKey   string
	GroupTitle string
}

// Summary is one series of the catalog, as GET /series lists it.
type Summary struct {
	MediaSourceID    string  `json:"media_source_id"`
	SeriesExternalID *string `json:"series_external_id"`
	SeriesTitle      string  `json:"series_title"`
	EpisodeCount     int     `json:"episode_count"`
	TotalDurationMs  int64   `json:"total_duration_ms"`
}

// NewAmount validates a client-supplied amount. No amount (nil) means one
// episode per visit. An empty mode with a count means "count"; mode "count"
// without a count means 1. A count is only valid with mode "count" and must
// be a positive integer.
func NewAmount(in *domain.SeriesAmountInput) (domain.SeriesAmount, error) {
	if in == nil {
		return domain.SeriesAmount{Mode: domain.AmountCount, Count: 1}, nil
	}
	mode := in.Mode
	if mode == "" {
		mode = domain.AmountCount
	}
	switch mode {
	case domain.AmountCount:
		n := 1
		if in.Count != nil {
			n = *in.Count
		}
		if n < 1 {
			return domain.SeriesAmount{}, errors.New("series.amount.count must be a positive integer")
		}
		return domain.SeriesAmount{Mode: mode, Count: n}, nil
	case domain.AmountSlotEnd, domain.AmountSeriesEnd:
		if in.Count != nil {
			return domain.SeriesAmount{}, errors.New("series.amount.count is only valid with mode count")
		}
		return domain.SeriesAmount{Mode: mode}, nil
	}
	return domain.SeriesAmount{}, errors.New("series.amount.mode must be one of count, slot_end, series_end")
}

// NewSpec validates a series identity, order and amount and returns the spec
// to store, with a fresh random seed. An empty order defaults to original, a
// nil amount to one episode per visit. Slice is reserved and must be empty.
func NewSpec(mediaSourceID, externalID, title, order string, amountIn *domain.SeriesAmountInput, slice json.RawMessage) (domain.SeriesSpec, error) {
	if mediaSourceID == "" {
		return domain.SeriesSpec{}, errors.New("series.media_source_id is required")
	}
	if externalID == "" && title == "" {
		return domain.SeriesSpec{}, errors.New("series.series_external_id or series.series_title is required")
	}
	if order == "" {
		order = domain.OrderOriginal
	}
	if !domain.ValidPlaybackOrder(order) {
		return domain.SeriesSpec{}, errors.New("series.playback_order must be one of original, random, shuffle, release_date")
	}
	if len(slice) > 0 && string(slice) != "null" {
		return domain.SeriesSpec{}, errors.New("series.slice is not supported yet")
	}
	amount, err := NewAmount(amountIn)
	if err != nil {
		return domain.SeriesSpec{}, err
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return domain.SeriesSpec{}, err
	}
	return domain.SeriesSpec{
		MediaSourceID:    mediaSourceID,
		SeriesExternalID: externalID,
		SeriesTitle:      title,
		PlaybackOrder:    order,
		Seed:             int64(binary.LittleEndian.Uint64(b[:]) >> 1),
		Amount:           &amount,
	}, nil
}

// ParseSpec decodes block_item.spec of a series item.
func ParseSpec(spec *string) (domain.SeriesSpec, error) {
	var s domain.SeriesSpec
	if spec == nil {
		return s, errors.New("series block item has no spec")
	}
	if err := json.Unmarshal([]byte(*spec), &s); err != nil {
		return s, fmt.Errorf("decode series spec: %w", err)
	}
	return s, nil
}

// Encode serializes a spec for block_item.spec.
func Encode(s domain.SeriesSpec) (string, error) {
	b, err := json.Marshal(s)
	return string(b), err
}

func episodeWhere(s domain.SeriesSpec) (string, []any) {
	clause := "kind = 'episode' AND media_source_id = ? AND duration_ms > 0 AND "
	if s.SeriesExternalID != "" {
		return clause + "series_external_id = ?", []any{s.MediaSourceID, s.SeriesExternalID}
	}
	return clause + "series_title = ?", []any{s.MediaSourceID, s.SeriesTitle}
}

// Episodes loads the episodes of the series, in no particular order. Items
// with a non-positive duration are left out (they cannot be scheduled).
func Episodes(ctx context.Context, db storage.DB, tenantID string, s domain.SeriesSpec) ([]Episode, error) {
	where, args := episodeWhere(s)
	type row struct {
		ID            string
		Title         string
		DurationMs    int64
		SeasonNumber  *int64
		EpisodeNumber *int64
		PremiereDate  *int64
	}
	var rows []row
	err := db.Query(ctx, &rows,
		"SELECT id, title, duration_ms, season_number, episode_number, premiere_date FROM catalog_item WHERE tenant_id = ? AND "+where+" ORDER BY id",
		append([]any{tenantID}, args...)...)
	if err != nil {
		return nil, err
	}
	out := make([]Episode, len(rows))
	for i, r := range rows {
		out[i] = Episode{ID: r.ID, Title: r.Title, DurationMs: r.DurationMs, SeasonNumber: r.SeasonNumber, EpisodeNumber: r.EpisodeNumber, PremiereDate: r.PremiereDate}
	}
	return out, nil
}

// Stats counts the schedulable episodes of the series and their total duration.
func Stats(ctx context.Context, db storage.DB, tenantID string, s domain.SeriesSpec) (int, int64, error) {
	where, args := episodeWhere(s)
	var rows []struct {
		EpisodeCount    int
		TotalDurationMs int64
	}
	err := db.Query(ctx, &rows,
		"SELECT COUNT(*) AS episode_count, COALESCE(SUM(duration_ms), 0) AS total_duration_ms FROM catalog_item WHERE tenant_id = ? AND "+where,
		append([]any{tenantID}, args...)...)
	if err != nil || len(rows) == 0 {
		return 0, 0, err
	}
	return rows[0].EpisodeCount, rows[0].TotalDurationMs, nil
}

// Decorate fills Series on every series item of items (episode count and total
// duration are read live from the catalog). A series item whose spec cannot be
// decoded is left without a view.
func Decorate(ctx context.Context, db storage.DB, tenantID string, items []domain.BlockItem) error {
	for i := range items {
		if !items[i].IsSeries() {
			continue
		}
		spec, err := ParseSpec(items[i].Spec)
		if err != nil {
			continue
		}
		count, total, err := Stats(ctx, db, tenantID, spec)
		if err != nil {
			return err
		}
		items[i].Series = &domain.SeriesItemView{
			MediaSourceID:    spec.MediaSourceID,
			SeriesExternalID: spec.SeriesExternalID,
			SeriesTitle:      spec.SeriesTitle,
			PlaybackOrder:    spec.PlaybackOrder,
			Amount:           spec.EffectiveAmount(),
			Slice:            spec.Slice,
			EpisodeCount:     count,
			TotalDurationMs:  total,
		}
	}
	return nil
}

// List returns the series of the catalog (episodes grouped by media source,
// external id and title), ordered by title. q matches the series title
// accent- and case-insensitively; mediaSourceID narrows to one source.
func List(ctx context.Context, db storage.DB, tenantID, q, mediaSourceID string, limit, offset int) ([]Summary, error) {
	query := "SELECT media_source_id, series_external_id, series_title, COUNT(*) AS episode_count, SUM(duration_ms) AS total_duration_ms " +
		"FROM catalog_item WHERE tenant_id = ? AND kind = 'episode' AND series_title IS NOT NULL AND duration_ms > 0"
	args := []any{tenantID}
	if q != "" {
		// search_text is "folded title \n folded series title": match after the newline.
		query += " AND search_text LIKE ?"
		args = append(args, "%\n%"+catalogsync.FoldText(q)+"%")
	}
	if mediaSourceID != "" {
		query += " AND media_source_id = ?"
		args = append(args, mediaSourceID)
	}
	query += " GROUP BY media_source_id, series_external_id, series_title ORDER BY series_title, media_source_id LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	var out []Summary
	if err := db.Query(ctx, &out, query, args...); err != nil {
		return nil, err
	}
	return out, nil
}

// Exists reports whether the media source holds at least one episode of the series.
func Exists(ctx context.Context, db storage.DB, tenantID string, s domain.SeriesSpec) (bool, error) {
	where, args := episodeWhere(s)
	var ids []string
	err := db.Query(ctx, &ids, "SELECT id FROM catalog_item WHERE tenant_id = ? AND "+where+" LIMIT 1", append([]any{tenantID}, args...)...)
	return len(ids) > 0, err
}
