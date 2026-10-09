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
// change what the stored position means, the exhaustion behavior does not
// (the rows it leaves behind are readable under both).
func InterleaveRestarts(a, b domain.Interleave) bool {
	return a.PerRound != b.PerRound || a.SeriesOrder != b.SeriesOrder
}

// ErrUnknownCollection is returned for a collection id that does not exist for
// the tenant.
var ErrUnknownCollection = errors.New("collection_id does not match a collection")

// checkCollection verifies that id names a collection of tenantID. A collection
// of another tenant is reported exactly like a missing one.
func checkCollection(ctx context.Context, db storage.DB, tenantID, id string) error {
	if id == "" {
		return errors.New("collection_id is required")
	}
	if _, err := catalogsync.LoadCollectionQuery(ctx, db, tenantID, id); err != nil {
		if errors.Is(err, catalogsync.ErrCollectionNotFound) {
			return ErrUnknownCollection
		}
		return err
	}
	return nil
}

// NewCriteriaSpec validates a criteria item's collection, order and amount and
// returns the spec to store, with a fresh random seed. The item stores only the
// collection id; the query lives in the collection. An empty order defaults to
// title; the amount is required (see NewCriteriaAmount). The interleave
// parameters are only valid (and then default to 1 per round, restart, series
// by title) with the interleaved order.
func NewCriteriaSpec(ctx context.Context, db storage.DB, tenantID, collectionID string, order string, amountIn *domain.SeriesAmountInput, interleaveIn *domain.InterleaveInput) (domain.CriteriaSpec, error) {
	if err := checkCollection(ctx, db, tenantID, collectionID); err != nil {
		return domain.CriteriaSpec{}, err
	}
	order, err := parseOrder(order)
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
		CollectionID:  collectionID,
		PlaybackOrder: order,
		Seed:          int64(binary.LittleEndian.Uint64(b[:]) >> 1),
		Amount:        amount,
		Interleave:    il,
	}, nil
}

// UpdateCriteriaSpec applies the editable fields of a criteria item (nil or
// empty means unchanged) to spec. Pointing the item at another collection (or
// at one for the first time, for an item held for review) keeps the cursor like
// the old query edit did; the deprecated embedded query is left alone. orderChanged reports a different playback
// order or different interleave parameters (see InterleaveRestarts), which
// restart the item's cursor; changing the query or the amount keeps it.
// interleaveIn overlays the stored interleave parameters and is only valid when
// the resulting order is interleaved.
func UpdateCriteriaSpec(ctx context.Context, db storage.DB, tenantID string, spec domain.CriteriaSpec, collectionID *string, order *string, amountIn *domain.SeriesAmountInput, interleaveIn *domain.InterleaveInput) (domain.CriteriaSpec, bool, error) {
	orderChanged := false
	if collectionID != nil {
		if err := checkCollection(ctx, db, tenantID, *collectionID); err != nil {
			return spec, false, err
		}
		spec.CollectionID = *collectionID
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

// ResolveSpec returns spec with its effective query filled in: the query of
// the collection it references (the source of truth), or, only while it has no
// collection (an item held for review), its deprecated embedded query. The
// result is for evaluation only, never to be stored back.
func ResolveSpec(ctx context.Context, db storage.DB, tenantID string, spec domain.CriteriaSpec) (domain.CriteriaSpec, error) {
	if spec.CollectionID == "" {
		return spec, nil
	}
	q, err := catalogsync.LoadCollectionQuery(ctx, db, tenantID, spec.CollectionID)
	if err != nil {
		return spec, err
	}
	spec.Query = json.RawMessage(q.JSON())
	return spec, nil
}

// schedulable restricts a query to what can be put on air: a positive duration.
// spec carries its effective query (see ResolveSpec). Season 0 episodes of a
// series are left out unless includeSpecials (the owning block's option).
func schedulable(spec domain.CriteriaSpec, tenantID string, includeSpecials bool) (string, []any, error) {
	q, err := catalogsync.ParseQuery(spec.Query)
	if err != nil {
		return "", nil, err
	}
	where, args := q.Where(tenantID)
	where += " AND duration_ms > 0"
	if !includeSpecials {
		where += " AND " + series.NotSpecialSQL
	}
	return where, args, nil
}

// Episodes loads the catalog items the item's query currently matches (items
// with a non-positive duration left out), in no particular order, in the shape
// the materializer walks series episodes in.
func Episodes(ctx context.Context, db storage.DB, tenantID string, spec domain.CriteriaSpec, includeSpecials bool) ([]series.Episode, error) {
	spec, err := ResolveSpec(ctx, db, tenantID, spec)
	if err != nil {
		return nil, err
	}
	where, args, err := schedulable(spec, tenantID, includeSpecials)
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
func Stats(ctx context.Context, db storage.DB, tenantID string, spec domain.CriteriaSpec, includeSpecials bool) (int, int64, error) {
	spec, err := ResolveSpec(ctx, db, tenantID, spec)
	if err != nil {
		return 0, 0, err
	}
	return statsOf(ctx, db, tenantID, spec, includeSpecials)
}

// statsOf is Stats for a spec whose effective query is already resolved.
func statsOf(ctx context.Context, db storage.DB, tenantID string, spec domain.CriteriaSpec, includeSpecials bool) (int, int64, error) {
	where, args, err := schedulable(spec, tenantID, includeSpecials)
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
// are read live from the catalog, as the owning block's include_specials option
// would schedule them). An item whose spec cannot be decoded is
// left without a view; one whose collection is gone is shown without counts.
func Decorate(ctx context.Context, db storage.DB, tenantID string, items []domain.BlockItem) error {
	specials := map[string]bool{}
	includeFor := func(blockID string) (bool, error) {
		if v, ok := specials[blockID]; ok {
			return v, nil
		}
		v, err := IncludesSpecials(ctx, db, tenantID, blockID)
		specials[blockID] = v
		return v, err
	}
	for i := range items {
		include, err := includeFor(items[i].BlockID)
		if err != nil {
			return err
		}
		if err := series.Decorate(ctx, db, tenantID, items[i:i+1], include); err != nil {
			return err
		}
	}
	names := map[string]*string{}
	for i := range items {
		if !items[i].IsCriteria() {
			continue
		}
		spec, err := ParseCriteriaSpec(items[i].Spec)
		if err != nil {
			continue
		}
		includeSpecials, err := includeFor(items[i].BlockID)
		if err != nil {
			return err
		}
		view := &domain.CriteriaItemView{
			PlaybackOrder: spec.PlaybackOrder,
			Amount:        spec.Amount,
			Interleave:    spec.Interleave,
		}
		if spec.CollectionID != "" {
			id := spec.CollectionID
			view.CollectionID = &id
			name, seen := names[id]
			if !seen {
				var n []string
				if err := db.Query(ctx, &n, "SELECT name FROM collection WHERE tenant_id = ? AND id = ?", tenantID, id); err != nil {
					return err
				}
				if len(n) > 0 {
					name = &n[0]
				}
				names[id] = name
			}
			view.CollectionName = name
		}
		eff, err := ResolveSpec(ctx, db, tenantID, spec)
		if err != nil && !errors.Is(err, catalogsync.ErrCollectionNotFound) {
			return err
		}
		// A stored query that no longer parses (or a missing collection) is
		// shown without counts rather than failing the whole listing; a
		// database error still fails it.
		if err == nil {
			if _, perr := catalogsync.ParseQuery(eff.Query); perr == nil {
				if view.MatchCount, view.TotalDurationMs, err = statsOf(ctx, db, tenantID, eff, includeSpecials); err != nil {
					return err
				}
			}
			var query any
			if json.Unmarshal(eff.Query, &query) == nil {
				view.Query = query
			}
		}
		items[i].Criteria = view
	}
	return nil
}
