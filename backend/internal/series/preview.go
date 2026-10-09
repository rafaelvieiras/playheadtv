package series

import (
	"context"
	"errors"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// InvalidError is a rejection of client input (as opposed to a storage
// failure): the API maps it to 400.
type InvalidError string

func (e InvalidError) Error() string { return string(e) }

// IsInvalid reports whether err is an InvalidError.
func IsInvalid(err error) bool {
	var ie InvalidError
	return errors.As(err, &ie)
}

// CheckSpec verifies a new or edited spec against the catalog: the series must
// have episodes, and its slice must leave at least one of them.
func CheckSpec(ctx context.Context, db storage.DB, tenantID string, s domain.SeriesSpec) error {
	found, err := Exists(ctx, db, tenantID, s)
	if err != nil {
		return err
	}
	if !found {
		return InvalidError("no episodes of that series in the catalog")
	}
	if s.Slice != nil {
		n, _, err := Stats(ctx, db, tenantID, s, true)
		if err != nil {
			return err
		}
		if n == 0 {
			return InvalidError("series.slice matches no episodes of that series")
		}
	}
	return nil
}

// UpdateSlice replaces the slice of a stored spec (an empty slice clears it)
// and reports whether the series' episode set definition changed, which
// restarts the item's cursor. The sample seed is kept while the slice keeps
// sampling.
func UpdateSlice(ctx context.Context, db storage.DB, tenantID string, spec domain.SeriesSpec, in *domain.SeriesSlice) (domain.SeriesSpec, bool, error) {
	slice, err := NormalizeSlice(in)
	if err != nil {
		return spec, false, err
	}
	if SameSlice(spec.Slice, slice) {
		return spec, false, nil
	}
	seed, err := SliceSeedFor(slice, spec.SliceSeed)
	if err != nil {
		return spec, false, err
	}
	spec.Slice = slice
	spec.SliceSeed = seed
	if err := CheckSpec(ctx, db, tenantID, spec); err != nil {
		return spec, false, err
	}
	return spec, true, nil
}

// Preview is what a series and slice currently expand to, as the slice
// preview reports it.
type Preview struct {
	// EpisodeCount and TotalDurationMs describe the sliced episodes.
	EpisodeCount    int   `json:"episode_count"`
	TotalDurationMs int64 `json:"total_duration_ms"`
	// SeriesEpisodeCount is the whole series, slice ignored.
	SeriesEpisodeCount int `json:"series_episode_count"`
}

// PreviewSlice counts the episodes the series would expand to under slice,
// without storing anything. It validates the slice like a write does but
// does not insist on a non-empty result (a zero count is the answer). Season 0
// is counted out unless includeSpecials (the block's option) or the slice names
// it.
func PreviewSlice(ctx context.Context, db storage.DB, tenantID string, id domain.SeriesSpec, in *domain.SeriesSlice, includeSpecials bool) (Preview, error) {
	slice, err := NormalizeSlice(in)
	if err != nil {
		return Preview{}, err
	}
	eps, err := AllEpisodes(ctx, db, tenantID, id)
	if err != nil {
		return Preview{}, err
	}
	// The count does not depend on the seed: any fixed one will do.
	scheduled := eps
	if !includeSpecials {
		scheduled = DropSpecials(eps, slice)
	}
	sliced := ApplySlice(scheduled, slice, 1)
	p := Preview{EpisodeCount: len(sliced), SeriesEpisodeCount: len(eps)}
	for _, e := range sliced {
		p.TotalDurationMs += e.DurationMs
	}
	return p, nil
}
