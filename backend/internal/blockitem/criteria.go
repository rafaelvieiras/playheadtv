// Package blockitem holds what block items of type criteria need: validating
// and storing their spec, loading the catalog items their query matches, and
// the live view served to clients. It sits next to internal/series, which does
// the same for series items; Decorate here fills both views.
package blockitem

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/series"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// DefaultCriteriaOrder is the playback order of a criteria item created
// without one: the order dynamic blocks and pools list their matches in.
const DefaultCriteriaOrder = domain.OrderTitle

// NewCriteriaAmount validates a client-supplied amount of a criteria item.
// The amount is required, and "all matches" must be chosen explicitly:
// mode "count" with a positive count, or mode "all" without one. An empty
// mode with a count means "count".
func NewCriteriaAmount(in *domain.SeriesAmountInput) (domain.CriteriaAmount, error) {
	if in == nil {
		return domain.CriteriaAmount{}, errors.New("criteria.amount is required: set mode count with a count, or mode all to air every match")
	}
	mode := in.Mode
	if mode == "" && in.Count != nil {
		mode = domain.CriteriaAmountCount
	}
	switch mode {
	case domain.CriteriaAmountCount:
		if in.Count == nil {
			return domain.CriteriaAmount{}, errors.New("criteria.amount.count is required with mode count")
		}
		if *in.Count < 1 {
			return domain.CriteriaAmount{}, errors.New("criteria.amount.count must be a positive integer")
		}
		return domain.CriteriaAmount{Mode: mode, Count: *in.Count}, nil
	case domain.CriteriaAmountAll:
		if in.Count != nil {
			return domain.CriteriaAmount{}, errors.New("criteria.amount.count is only valid with mode count")
		}
		return domain.CriteriaAmount{Mode: mode}, nil
	}
	return domain.CriteriaAmount{}, errors.New("criteria.amount.mode must be count or all")
}

func parseOrder(order string) (string, error) {
	if order == "" {
		order = DefaultCriteriaOrder
	}
	if !domain.ValidCriteriaOrder(order) {
		return "", errors.New("criteria.playback_order must be one of title, release_date, random, shuffle, interleaved")
	}
	return order, nil
}

// ApplyInterleave overlays the fields set in in onto base (the zero value, or
// the stored parameters) and validates the result: per_round a positive
// integer, on_exhaustion restart or drop, series_order one of title,
// release_date, random, shuffle. Unset fields keep base's value, else the
// default (1, restart, title).
func ApplyInterleave(base domain.Interleave, in *domain.InterleaveInput) (domain.Interleave, error) {
	il := base
	if in != nil {
		if in.PerRound != nil {
			il.PerRound = *in.PerRound
			if il.PerRound < 1 {
				return il, errors.New("interleave.per_round must be a positive integer")
			}
		}
		if in.OnExhaustion != "" {
			il.OnExhaustion = in.OnExhaustion
		}
		if in.SeriesOrder != "" {
			il.SeriesOrder = in.SeriesOrder
		}
	}
	il = il.Normalized()
	if il.OnExhaustion != domain.OnExhaustionRestart && il.OnExhaustion != domain.OnExhaustionDrop {
		return il, errors.New("interleave.on_exhaustion must be restart or drop")
	}
	if !domain.ValidSeriesOrder(il.SeriesOrder) {
		return il, errors.New("interleave.series_order must be one of title, release_date, random, shuffle")
	}
	return il, nil
}

// InterleaveRestarts reports whether changing the interleave parameters from
// a to b must restart the cursor: the per-round count and the series order
// change what the stored position means, the exhaustion behavior does not.
func InterleaveRestarts(a, b domain.Interleave) bool {
	return a.PerRound != b.PerRound || a.SeriesOrder != b.SeriesOrder
}

func parseQuery(raw json.RawMessage) (json.RawMessage, error) {
	q, err := catalogsync.ParseQuery(raw)
	if err != nil {
		return nil, fmt.Errorf("criteria.query: %w", err)
	}
	return json.RawMessage(q.JSON()), nil
}

// NewCriteriaSpec validates a criteria item's query, order and amount and
// returns the spec to store, with a fresh random seed. An empty order
// defaults to title; the amount is required (see NewCriteriaAmount). The
// interleave parameters are only valid (and then default to 1 per round,
// restart, series by title) with the interleaved order.
func NewCriteriaSpec(query json.RawMessage, order string, amountIn *domain.SeriesAmountInput, interleaveIn *domain.InterleaveInput) (domain.CriteriaSpec, error) {
	q, err := parseQuery(query)
	if err != nil {
		return domain.CriteriaSpec{}, err
	}
	order, err = parseOrder(order)
	if err != nil {
		return domain.CriteriaSpec{}, err
	}
	amount, err := NewCriteriaAmount(amountIn)
	if err != nil {
		return domain.CriteriaSpec{}, err
	}
	var il *domain.Interleave
	if order == domain.OrderInterleaved {
		v, err := ApplyInterleave(domain.Interleave{}, interleaveIn)
		if err != nil {
			return domain.CriteriaSpec{}, err
		}
		il = &v
	} else if interleaveIn != nil {
		return domain.CriteriaSpec{}, errors.New("criteria.interleave is only valid with playback_order interleaved")
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return domain.CriteriaSpec{}, err
	}
	return domain.CriteriaSpec{
		Query:         q,
		PlaybackOrder: order,
		Seed:          int64(binary.LittleEndian.Uint64(b[:]) >> 1),
		Amount:        amount,
		Interleave:    il,
	}, nil
}

// UpdateCriteriaSpec applies the editable fields of a criteria item (nil or
// empty means unchanged) to spec. orderChanged reports a different playback
// order or different interleave parameters (see InterleaveRestarts), which
// restart the item's cursor; changing the query or the amount keeps it.
// interleaveIn overlays the stored interleave parameters and is only valid when
// the resulting order is interleaved.
func UpdateCriteriaSpec(spec domain.CriteriaSpec, query json.RawMessage, order *string, amountIn *domain.SeriesAmountInput, interleaveIn *domain.InterleaveInput) (domain.CriteriaSpec, bool, error) {
	orderChanged := false
	if len(query) > 0 && string(query) != "null" {
		q, err := parseQuery(query)
		if err != nil {
			return spec, false, err
		}
		spec.Query = q
	}
	if order != nil {
		if !domain.ValidCriteriaOrder(*order) {
			return spec, false, errors.New("playback_order must be one of title, release_date, random, shuffle, interleaved for a criteria item")
		}
		orderChanged = spec.PlaybackOrder != *order
		spec.PlaybackOrder = *order
	}
	if spec.PlaybackOrder == domain.OrderInterleaved {
		var base domain.Interleave
		if spec.Interleave != nil {
			base = *spec.Interleave
		}
		il, err := ApplyInterleave(base, interleaveIn)
		if err != nil {
			return spec, false, err
		}
		if spec.Interleave != nil && InterleaveRestarts(*spec.Interleave, il) {
			orderChanged = true
		}
		spec.Interleave = &il
	} else {
		if interleaveIn != nil {
			return spec, false, errors.New("interleave is only valid with playback_order interleaved")
		}
		spec.Interleave = nil
	}
	if amountIn != nil {
		a, err := NewCriteriaAmount(amountIn)
		if err != nil {
			return spec, false, err
		}
		spec.Amount = a
	}
	return spec, orderChanged, nil
}

// ParseCriteriaSpec decodes block_item.spec of a criteria item.
func ParseCriteriaSpec(spec *string) (domain.CriteriaSpec, error) {
	var s domain.CriteriaSpec
	if spec == nil {
		return s, errors.New("criteria block item has no spec")
	}
	if err := json.Unmarshal([]byte(*spec), &s); err != nil {
		return s, fmt.Errorf("decode criteria spec: %w", err)
	}
	return s, nil
}

// EncodeCriteriaSpec serializes a spec for block_item.spec.
func EncodeCriteriaSpec(s domain.CriteriaSpec) (string, error) {
	b, err := json.Marshal(s)
	return string(b), err
}

// schedulable restricts a query to what can be put on air: a positive duration.
func schedulable(spec domain.CriteriaSpec, tenantID string) (string, []any, error) {
	q, err := catalogsync.ParseQuery(spec.Query)
	if err != nil {
		return "", nil, err
	}
	where, args := q.Where(tenantID)
	return where + " AND duration_ms > 0", args, nil
}

// Episodes loads the catalog items the item's query currently matches (items
// with a non-positive duration left out), in no particular order, in the shape
// the materializer walks series episodes in.
func Episodes(ctx context.Context, db storage.DB, tenantID string, spec domain.CriteriaSpec) ([]series.Episode, error) {
	where, args, err := schedulable(spec, tenantID)
	if err != nil {
		return nil, err
	}
	type row struct {
		ID               string
		Title            string
		DurationMs       int64
		SeasonNumber     *int64
		EpisodeNumber    *int64
		PremiereDate     *int64
		Kind             string
		MediaSourceID    string
		SeriesExternalID *string
		SeriesTitle      *string
	}
	var rows []row
	if err := db.Query(ctx, &rows,
		"SELECT id, title, duration_ms, season_number, episode_number, premiere_date, kind, media_source_id, series_external_id, series_title FROM catalog_item WHERE "+where+" ORDER BY id", args...,
	); err != nil {
		return nil, err
	}
	out := make([]series.Episode, len(rows))
	for i, r := range rows {
		e := series.Episode{ID: r.ID, Title: r.Title, DurationMs: r.DurationMs, SeasonNumber: r.SeasonNumber, EpisodeNumber: r.EpisodeNumber, PremiereDate: r.PremiereDate}
		// The series an item belongs to: its media source plus external id
		// (else title), as series items identify one. An item without a series
		// (a movie, a bumper, an episode with no series title) is a one-item
		// series of its own.
		switch {
		case r.Kind == "episode" && r.SeriesTitle != nil && *r.SeriesTitle != "":
			e.GroupTitle = *r.SeriesTitle
			if r.SeriesExternalID != nil && *r.SeriesExternalID != "" {
				e.GroupKey = "s:" + r.MediaSourceID + ":" + *r.SeriesExternalID
			} else {
				e.GroupKey = "t:" + r.MediaSourceID + ":" + *r.SeriesTitle
			}
		default:
			e.GroupTitle = r.Title
			e.GroupKey = "i:" + r.ID
		}
		out[i] = e
	}
	return out, nil
}

// Stats counts the schedulable items the query matches and their total duration.
func Stats(ctx context.Context, db storage.DB, tenantID string, spec domain.CriteriaSpec) (int, int64, error) {
	where, args, err := schedulable(spec, tenantID)
	if err != nil {
		return 0, 0, err
	}
	var rows []struct {
		MatchCount      int
		TotalDurationMs int64
	}
	if err := db.Query(ctx, &rows,
		"SELECT COUNT(*) AS match_count, COALESCE(SUM(duration_ms), 0) AS total_duration_ms FROM catalog_item WHERE "+where, args...,
	); err != nil || len(rows) == 0 {
		return 0, 0, err
	}
	return rows[0].MatchCount, rows[0].TotalDurationMs, nil
}

// Decorate fills the series and criteria views of items (counts and durations
// are read live from the catalog). An item whose spec cannot be decoded is
// left without a view.
func Decorate(ctx context.Context, db storage.DB, tenantID string, items []domain.BlockItem) error {
	if err := series.Decorate(ctx, db, tenantID, items); err != nil {
		return err
	}
	for i := range items {
		if !items[i].IsCriteria() {
			continue
		}
		spec, err := ParseCriteriaSpec(items[i].Spec)
		if err != nil {
			continue
		}
		count, total := 0, int64(0)
		// A stored query that no longer parses is shown without counts rather
		// than failing the whole listing; a database error still fails it.
		if _, perr := catalogsync.ParseQuery(spec.Query); perr == nil {
			if count, total, err = Stats(ctx, db, tenantID, spec); err != nil {
				return err
			}
		}
		var query any
		if err := json.Unmarshal(spec.Query, &query); err != nil {
			continue
		}
		items[i].Criteria = &domain.CriteriaItemView{
			Query:           query,
			PlaybackOrder:   spec.PlaybackOrder,
			Amount:          spec.Amount,
			Interleave:      spec.Interleave,
			MatchCount:      count,
			TotalDurationMs: total,
		}
	}
	return nil
}
