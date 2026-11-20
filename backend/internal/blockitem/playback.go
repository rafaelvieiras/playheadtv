package blockitem

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// BlockPlayback is the validated playback settings of a block, ready to store:
// block.playback_order, block.interleave_spec and block.playback_seed.
type BlockPlayback struct {
	Order          string
	InterleaveSpec *string
	Seed           int64
}

// NewBlockPlayback validates the playback settings of a block write (create:
// prev nil; update: prev is the stored block, a full replace like the rest of
// the PUT).
//
// A dynamic block (one with a collection) is a rule, so it walks its matches in
// any of the criteria orders: title (every match by title, looped: what a
// dynamic block originally always did), release_date, random, shuffle, or
// interleaved (round-robin across series). A manual block is an authored list
// and has no order of its own: the per-item orders decide, so only an empty or
// title value is accepted there.
//
// interleave carries the parameters of the interleaved order and is only valid
// with it, omitted ones defaulting to 1 per round, restart, series by title.
// restart reports that the stored position no longer applies (the order, the
// per-round count or the series order changed), so the block's cursor has to
// start over. The seed is kept while the order stays the same, so a
// regeneration resumes the same sequence, and is mixed with the channel id by
// the walk (two channels never shuffle alike).
func NewBlockPlayback(dynamic bool, order string, in *domain.InterleaveInput, prev *domain.Block) (bp BlockPlayback, restart bool, err error) {
	if order == "" {
		order = domain.OrderTitle
	}
	if dynamic {
		if !domain.ValidCriteriaOrder(order) {
			return bp, false, errors.New("playback_order must be one of title, release_date, random, shuffle, interleaved")
		}
	} else if order != domain.OrderTitle {
		return bp, false, errors.New("playback_order is only valid for a dynamic block (one with a collection)")
	}
	bp.Order = order
	prevOrder := domain.OrderTitle
	if prev != nil && prev.PlaybackOrder != "" {
		prevOrder = prev.PlaybackOrder
	}
	if order != domain.OrderInterleaved {
		if in != nil {
			return bp, false, errors.New("interleave is only valid with playback_order interleaved")
		}
		if order == domain.OrderTitle {
			return bp, prevOrder != order, nil
		}
		// random and shuffle mix the seed into their order; keep it across
		// regenerations, replace it when the order itself changed.
		if prevOrder == order && prev != nil && prev.PlaybackSeed != 0 {
			bp.Seed = prev.PlaybackSeed
			return bp, false, nil
		}
		if bp.Seed, err = newPlaybackSeed(); err != nil {
			return bp, false, err
		}
		return bp, true, nil
	}
	if !dynamic {
		return bp, false, errors.New("playback_order interleaved is only valid for a dynamic block (one with a collection)")
	}
	il, err := ApplyInterleave(domain.Interleave{}, in)
	if err != nil {
		return bp, false, err
	}
	b, err := json.Marshal(il)
	if err != nil {
		return bp, false, err
	}
	spec := string(b)
	bp.InterleaveSpec = &spec
	if prevOrder == domain.OrderInterleaved && prev != nil && prev.PlaybackSeed != 0 {
		bp.Seed = prev.PlaybackSeed
		prev.FillInterleave()
		if prev.Interleave != nil {
			restart = InterleaveRestarts(*prev.Interleave, il)
		}
		return bp, restart, nil
	}
	if bp.Seed, err = newPlaybackSeed(); err != nil {
		return bp, false, err
	}
	return bp, true, nil
}

// newPlaybackSeed returns a fresh non-zero seed for a walking block.
func newPlaybackSeed() (int64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(raw[:])>>1) | 1, nil
}

// NewListMode validates the list_mode of a block write: how a manual block
// plays its item list, repeat (the default for a new block) or once. It is only
// valid for a manual block (dynamic: the block always loops by its own rules),
// so a non-empty mode on a dynamic block is an error. An empty mode keeps the
// stored one on an update (prev, even though the PUT is otherwise a full
// replace: a client that never heard of the setting must not flip a block it
// did not mean to touch) and is repeat on a create. explicit reports that the
// request carried a mode, which is what settles a pending migration review.
func NewListMode(dynamic bool, mode string, prev *domain.Block) (resolved string, explicit bool, err error) {
	if mode != "" && mode != domain.ListModeRepeat && mode != domain.ListModeOnce {
		return "", false, errors.New("list_mode must be repeat or once")
	}
	if dynamic {
		if mode != "" {
			return "", false, errors.New("list_mode is only valid for a manual block (a dynamic block always loops its matches)")
		}
		// The stored value is irrelevant for a dynamic block; keep it so a
		// block turned manual again resumes its previous mode.
		if prev != nil && prev.ListMode != "" {
			return prev.ListMode, false, nil
		}
		return domain.ListModeRepeat, false, nil
	}
	if mode != "" {
		return mode, true, nil
	}
	if prev != nil && prev.ListMode != "" {
		return prev.ListMode, false, nil
	}
	return domain.ListModeRepeat, false, nil
}

// ClearListModeReview removes a block from the migration review list (its mode
// was decided, or it is no longer a manual block).
func ClearListModeReview(ctx context.Context, db storage.DB, tenantID, blockID string) error {
	return db.Exec(ctx, "DELETE FROM block_list_mode_review WHERE tenant_id = ? AND block_id = ?", tenantID, blockID)
}

// RestartBlockCursor restarts an interleaved dynamic block on every channel:
// the cursor's origin moves to now and what is still to air from it is dropped
// (the regeneration that follows rebuilds it).
func RestartBlockCursor(ctx context.Context, db storage.DB, tenantID, blockID string) error {
	nowMs := time.Now().UnixMilli()
	if err := db.Exec(ctx, "UPDATE block_cursor SET origin_ms = ? WHERE tenant_id = ? AND block_id = ?", nowMs, tenantID, blockID); err != nil {
		return err
	}
	return db.Exec(ctx, "DELETE FROM epg_entry WHERE tenant_id = ? AND source_block_item_id = ? AND end_at > ?", tenantID, blockID, nowMs)
}

// IncludesSpecials reports whether the block lets season 0 episodes in (its
// include_specials option); a missing block reads as the default, off.
func IncludesSpecials(ctx context.Context, db storage.DB, tenantID, blockID string) (bool, error) {
	var v []bool
	if err := db.Query(ctx, &v, "SELECT include_specials FROM block WHERE tenant_id = ? AND id = ?", tenantID, blockID); err != nil {
		return false, err
	}
	return len(v) > 0 && v[0], nil
}
