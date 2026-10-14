package api

import (
	"context"
	"strings"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// watermarkResolver decides, per guide entry, which watermark (if any) the
// client draws while it is on air. The rules (docs/ARCHITECTURE.md
// "Watermark"):
//
//   - an entry belongs to the block of the schedule item that produced it
//     (epg_entry.source_schedule_item_id): its program, and also the
//     preroll/midroll/postroll/tail filler that decorates it, whatever the
//     scope of the filler rule (a channel-wide rule's filler around a block's
//     program still follows that block);
//   - a block in override mode with an image uses its own; in disable mode
//     there is none; in inherit mode (or with no row) the channel's applies;
//   - an entry with no schedule item (a channel fallback entry) has no block,
//     so the channel's applies;
//   - the channel's applies only in override mode with an image; its inherit
//     and disable both mean none.
//
// A block in override mode with no image uploaded yet resolves to none
// (nothing to draw), not to the channel's.
type watermarkResolver struct {
	channels map[string]domain.Watermark
	blocks   map[string]domain.Watermark
	blockOf  map[string]string // schedule_item id -> block id
}

// newWatermarkResolver loads the tenant's watermark rows (images are not read)
// and the block of every schedule item the entries came from. With no
// watermark configured at all it does no further query.
func newWatermarkResolver(ctx context.Context, db storage.DB, entries []domain.EpgEntry) (*watermarkResolver, error) {
	res := &watermarkResolver{channels: map[string]domain.Watermark{}, blocks: map[string]domain.Watermark{}, blockOf: map[string]string{}}
	var rows []domain.Watermark
	if err := db.Query(ctx, &rows, "SELECT "+watermarkMetaColumns+" FROM watermark WHERE tenant_id = ?", DefaultTenantID); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return res, nil
	}
	for _, w := range rows {
		if w.OwnerType == "channel" {
			res.channels[w.OwnerID] = w
		} else {
			res.blocks[w.OwnerID] = w
		}
	}
	if len(res.blocks) == 0 {
		return res, nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, e := range entries {
		if e.SourceScheduleItemID != nil && !seen[*e.SourceScheduleItemID] {
			seen[*e.SourceScheduleItemID] = true
			ids = append(ids, *e.SourceScheduleItemID)
		}
	}
	type itemBlock struct {
		ID      string
		BlockID string
	}
	for from := 0; from < len(ids); from += catalogLookupChunk {
		chunk := ids[from:min(from+catalogLookupChunk, len(ids))]
		args := []any{DefaultTenantID}
		for _, id := range chunk {
			args = append(args, id)
		}
		var found []itemBlock
		if err := db.Query(ctx, &found,
			"SELECT id, block_id FROM schedule_item WHERE tenant_id = ? AND id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+")",
			args...); err != nil {
			return nil, err
		}
		for _, f := range found {
			res.blockOf[f.ID] = f.BlockID
		}
	}
	return res, nil
}

func refOf(w domain.Watermark) *domain.WatermarkRef {
	if w.Mode != domain.WatermarkOverride || w.ImageID == "" {
		return nil
	}
	return &domain.WatermarkRef{ImageID: w.ImageID, Position: w.Position, OpacityPct: w.OpacityPct, SizePct: w.SizePct}
}

func (r *watermarkResolver) resolve(e domain.EpgEntry) *domain.WatermarkRef {
	if r == nil || (len(r.channels) == 0 && len(r.blocks) == 0) {
		return nil
	}
	if e.SourceScheduleItemID != nil {
		if blockID, ok := r.blockOf[*e.SourceScheduleItemID]; ok {
			if b, ok := r.blocks[blockID]; ok {
				switch b.Mode {
				case domain.WatermarkOverride:
					return refOf(b)
				case domain.WatermarkDisable:
					return nil
				}
			}
		}
	}
	if c, ok := r.channels[e.ChannelID]; ok {
		return refOf(c)
	}
	return nil
}
