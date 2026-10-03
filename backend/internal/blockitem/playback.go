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
// the PUT). order is empty or title (every match by title, looped: what a
// dynamic block always did) or interleaved (round-robin across series, dynamic
// blocks only); interleave carries the parameters and is only valid with the
// interleaved order, omitted ones defaulting to 1 per round, restart, series
// by title. restart reports that the stored position no longer applies (the
// order or the per-round count or series order changed), so the block's cursor
// has to start over.
func NewBlockPlayback(dynamic bool, order string, in *domain.InterleaveInput, prev *domain.Block) (bp BlockPlayback, restart bool, err error) {
	if order == "" {
		order = domain.OrderTitle
	}
	if order != domain.OrderTitle && order != domain.OrderInterleaved {
		return bp, false, errors.New("playback_order must be title or interleaved")
	}
	bp.Order = order
	prevOrder := domain.OrderTitle
	if prev != nil && prev.PlaybackOrder != "" {
		prevOrder = prev.PlaybackOrder
	}
	if order == domain.OrderTitle {
		if in != nil {
			return bp, false, errors.New("interleave is only valid with playback_order interleaved")
		}
		return bp, prevOrder != order, nil
	}
	if !dynamic {
		return bp, false, errors.New("playback_order interleaved is only valid for a dynamic block (one with criteria)")
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
	} else {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return bp, false, err
		}
		bp.Seed = int64(binary.LittleEndian.Uint64(raw[:])>>1) | 1
		restart = true
	}
	return bp, restart, nil
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
