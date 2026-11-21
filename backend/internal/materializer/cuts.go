package materializer

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// Cutting an item into segments.
//
// A program can be played in segments: one catalog item cut at some offsets,
// with a break (the midroll of its block or channel, a plain rule or a break
// template) between consecutive segments. Two independent pieces make that
// work:
//
//   - WHERE to cut is the CutPlanner's business (the seam below). The default
//     planner never cuts, so nothing changes until a planner is plugged in.
//   - HOW a cut becomes entries is segmenter.split: segments of kind program
//     (each with its own start/end and item_offset_ms), the midroll break
//     between them, and the space the breaks add is taken from the slot like
//     any other break (every call site checks the whole span against its
//     limit, and counts every part toward its entry cap).

// CutItem is what a CutPlanner is asked about: one airing of a catalog item.
// It is a struct so the seam can grow (kind, chapters, ...) without breaking
// planners already written.
type CutItem struct {
	CatalogItemID string
	// DurationMs is how long the airing lasts: the catalog item's length, or
	// the trimmed length of a block item that plays only part of it.
	DurationMs int64
	// ChannelID and BlockID say where the item airs, for a planner whose rules
	// are scoped to a channel or a block. BlockID is the block the occurrence,
	// continuous item or dynamic block plays from.
	ChannelID string
	BlockID   string
}

// CutPlanner decides where an item is cut. PlanCuts returns the offsets, in
// milliseconds from the start of the item, at which a break goes; none (nil or
// empty) plays the item whole. The result must be deterministic for the same
// item, so a regenerated guide cuts where the previous one did.
//
// Offsets are normalized by the materializer (sorted, de-duplicated, anything
// outside (0, DurationMs) or closer than minSegmentMs to a neighbour or to an
// end dropped, at most maxCutsPerItem kept), and a cut at which the break
// resolves to nothing (no midroll configured, empty pool) is skipped, so a
// planner can be naive.
type CutPlanner interface {
	PlanCuts(item CutItem) []int64
}

// noCuts is the default planner: it never cuts.
type noCuts struct{}

func (noCuts) PlanCuts(CutItem) []int64 { return nil }

// NoCuts returns the planner that never cuts (the default).
func NoCuts() CutPlanner { return noCuts{} }

const (
	// minSegmentMs is the shortest segment (and so the closest a cut may sit to
	// the start or the end of the item, or to another cut) the materializer
	// accepts.
	minSegmentMs = 1000
	// maxCutsPerItem bounds the breaks one item gets, a guard against a planner
	// that returns a cut every few milliseconds.
	maxCutsPerItem = 20
)

// normalizeCuts applies the rules documented on CutPlanner.
func normalizeCuts(cuts []int64, durMs int64) []int64 {
	if len(cuts) == 0 {
		return nil
	}
	sorted := append([]int64(nil), cuts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var out []int64
	last := int64(0)
	for _, c := range sorted {
		if c-last < minSegmentMs || durMs-c < minSegmentMs {
			continue
		}
		out = append(out, c)
		last = c
		if len(out) == maxCutsPerItem {
			break
		}
	}
	return out
}

// segmenter turns a planned program entry into segments with breaks.
type segmenter struct {
	planner CutPlanner
	mid     *midrollSource
	// channelID and blockID are passed on to the planner (see CutItem).
	channelID, blockID string
}

// split cuts prog (a whole airing of an item, offset 0) where the planner says
// and returns the entries that replace it, in order: the first segment keeps
// prog's id and every other field, and each later segment copies them with a
// fresh id, its own span and its offset; the midroll break entries between
// them carry prog's schedule item, generation time and rotation position and
// none of its series bookkeeping. All segments of one airing share a
// split_group (prog's id). When there is nothing to cut, prog comes back alone,
// untouched.
func (s segmenter) split(prog domain.EpgEntry) []domain.EpgEntry {
	if s.planner == nil || prog.Kind != domain.EpgProgram || prog.ItemOffsetMs != 0 {
		return []domain.EpgEntry{prog}
	}
	durMs := prog.EndAt - prog.StartAt
	cuts := normalizeCuts(s.planner.PlanCuts(CutItem{CatalogItemID: prog.CatalogItemID, DurationMs: durMs, ChannelID: s.channelID, BlockID: s.blockID}), durMs)
	if len(cuts) == 0 {
		return []domain.EpgEntry{prog}
	}
	group := prog.ID
	var out []domain.EpgEntry
	at := prog.StartAt // wall-clock position of the next entry
	segFrom := int64(0)
	closeSegment := func(to int64) {
		seg := prog
		if len(out) > 0 {
			seg.ID = uuid.NewString()
		}
		seg.StartAt = at
		seg.EndAt = at + (to - segFrom)
		seg.ItemOffsetMs = segFrom
		seg.SplitGroup = &group
		out = append(out, seg)
		at = seg.EndAt
		segFrom = to
	}
	for _, cut := range cuts {
		parts := s.mid.pickBreak(time.UnixMilli(at).UTC())
		if len(parts) == 0 {
			continue // nothing to air at this cut: the segment just goes on
		}
		closeSegment(cut)
		for _, p := range parts {
			m := domain.EpgEntry{
				ID:                   uuid.NewString(),
				TenantID:             prog.TenantID,
				ChannelID:            prog.ChannelID,
				Kind:                 domain.EpgMidRoll,
				CatalogItemID:        p.item.CatalogItemID,
				StartAt:              at,
				EndAt:                at + p.dur,
				SourceScheduleItemID: prog.SourceScheduleItemID,
				GeneratedAt:          prog.GeneratedAt,
				RotationNext:         prog.RotationNext,
			}
			out = append(out, m)
			at = m.EndAt
		}
	}
	if len(out) == 0 {
		return []domain.EpgEntry{prog}
	}
	closeSegment(durMs)
	return out
}

// spanEnd is the end instant of the last of entries.
func spanEnd(entries []domain.EpgEntry) time.Time {
	return time.UnixMilli(entries[len(entries)-1].EndAt)
}
