package materializer

import (
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// maxLoopEntries bounds the entries one looping fill (a continuous item's span
// or a dynamic block's occurrence) emits, programs and fillers together, as a
// last-resort guard against a configuration that makes no real progress (for
// example a pool of near-zero-length fillers repeated over a long window).
const maxLoopEntries = 50000

// breakStage says how far the unit under way (preroll, program, postroll, then
// the midroll before the next program) had got where a span starts: a kept
// head can end in the middle of one, and the generation resuming right after
// it owes only the rest.
type breakStage int

const (
	// stageFresh: nothing owed but the preroll of the first program (the start
	// of an occurrence, or of a span right after a timed item).
	stageFresh breakStage = iota
	// stageAfterPre: the preroll aired, the program is next.
	stageAfterPre
	// stageAfterProgram: the program aired; its postroll, then a midroll and
	// the next preroll, are owed.
	stageAfterProgram
	// stageAfterPost: the postroll aired; a midroll and the next preroll are owed.
	stageAfterPost
	// stageAfterMid: the midroll aired; the next preroll is owed.
	stageAfterMid
)

// stageForKind maps the kind of the last kept entry of the continuous item to
// the stage the generation resumes from.
func stageForKind(kind domain.EpgEntryKind) breakStage {
	switch kind {
	case domain.EpgProgram:
		return stageAfterProgram
	case domain.EpgPreRoll:
		return stageAfterPre
	case domain.EpgPostRoll:
		return stageAfterPost
	case domain.EpgMidRoll:
		return stageAfterMid
	}
	return stageFresh
}

// breakLoop sequences the fillers around the programs of an endless sequence
// (a continuous item, or a dynamic block under a timed item):
//
//	[preroll] program [postroll] [midroll] [preroll] program [postroll] ...
//
// Each program is a whole "unit" with its own preroll and postroll, and the
// midroll sits between two consecutive programs. A unit is aired only if all
// of it fits before the limit, so no filler is ever left orphaned at the end of
// a span; whatever is left over then is the tail's (and the fallback's) to fill.
// Fillers are whole pool items picked by the existing rules, so selection
// strategies, block-over-channel scoping and weights apply unchanged.
type breakLoop struct {
	pre, mid, post *midrollSource

	// What is still owed before/after the next program.
	owePre, oweMid, owePost bool

	tenantID, channelID string
	generatedAt         int64
	sourceID            *string
}

func newBreakLoop(pre, mid, post *midrollSource, stage breakStage, tenantID, channelID string, generatedAt int64, sourceID *string) *breakLoop {
	b := &breakLoop{pre: pre, mid: mid, post: post, tenantID: tenantID, channelID: channelID, generatedAt: generatedAt, sourceID: sourceID}
	switch stage {
	case stageAfterPre:
		// The program is next; nothing else owed.
	case stageAfterProgram:
		b.owePost, b.oweMid, b.owePre = true, true, true
	case stageAfterPost:
		b.oweMid, b.owePre = true, true
	case stageAfterMid:
		b.owePre = true
	default:
		b.owePre = true
	}
	return b
}

func (b *breakLoop) fillerEntry(kind domain.EpgEntryKind, item domain.FillerPoolItem, start time.Time, durMs int64, rot int) domain.EpgEntry {
	r := rot
	return domain.EpgEntry{
		ID:                   uuid.NewString(),
		TenantID:             b.tenantID,
		ChannelID:            b.channelID,
		Kind:                 kind,
		CatalogItemID:        item.CatalogItemID,
		StartAt:              start.UnixMilli(),
		EndAt:                start.Add(time.Duration(durMs) * time.Millisecond).UnixMilli(),
		SourceScheduleItemID: b.sourceID,
		GeneratedAt:          b.generatedAt,
		RotationNext:         &r,
	}
}

// flushPost airs the postroll a kept head left owed. ok is false when it does
// not fit before limit, which ends the fill.
func (b *breakLoop) flushPost(cursor, limit time.Time, rot int) (entries []domain.EpgEntry, end time.Time, ok bool) {
	if !b.owePost {
		return nil, cursor, true
	}
	b.owePost = false
	item, dur, picked := b.post.pick()
	if !picked {
		return nil, cursor, true
	}
	end = cursor.Add(time.Duration(dur) * time.Millisecond)
	if end.After(limit) {
		return nil, cursor, false
	}
	return []domain.EpgEntry{b.fillerEntry(domain.EpgPostRoll, item, cursor, dur, rot)}, end, true
}

// unit airs the owed midroll and preroll, the program and its postroll,
// starting at cursor, provided the whole unit ends by limit. plan builds the
// program entry for the start it will really have (after the prefix); it may
// refuse. rotBefore is the rotation position stored on the prefix fillers.
// progIdx is the program's index within the returned entries; its postroll, if
// any, is the next one, and the caller stores the rotation position the pair
// leaves behind (setRotation).
func (b *breakLoop) unit(cursor, limit time.Time, rotBefore int, plan func(start time.Time) (domain.EpgEntry, bool)) (entries []domain.EpgEntry, end time.Time, progIdx int, ok bool) {
	type pick struct {
		item domain.FillerPoolItem
		dur  int64
		ok   bool
	}
	var mid, pre pick
	if b.oweMid {
		mid.item, mid.dur, mid.ok = b.mid.pick()
	}
	if b.owePre {
		pre.item, pre.dur, pre.ok = b.pre.pick()
	}
	start := cursor
	if mid.ok {
		start = start.Add(time.Duration(mid.dur) * time.Millisecond)
	}
	if pre.ok {
		start = start.Add(time.Duration(pre.dur) * time.Millisecond)
	}
	prog, planned := plan(start)
	if !planned {
		return nil, cursor, 0, false
	}
	post := pick{}
	post.item, post.dur, post.ok = b.post.pick()
	end = time.UnixMilli(prog.EndAt)
	if post.ok {
		end = end.Add(time.Duration(post.dur) * time.Millisecond)
	}
	if end.After(limit) {
		return nil, cursor, 0, false
	}

	at := cursor
	if mid.ok {
		entries = append(entries, b.fillerEntry(domain.EpgMidRoll, mid.item, at, mid.dur, rotBefore))
		at = at.Add(time.Duration(mid.dur) * time.Millisecond)
	}
	if pre.ok {
		entries = append(entries, b.fillerEntry(domain.EpgPreRoll, pre.item, at, pre.dur, rotBefore))
	}
	progIdx = len(entries)
	entries = append(entries, prog)
	if post.ok {
		entries = append(entries, b.fillerEntry(domain.EpgPostRoll, post.item, time.UnixMilli(prog.EndAt), post.dur, rotBefore))
	}
	b.owePre, b.oweMid, b.owePost = true, true, false
	return entries, end, progIdx, true
}

// setRotation stores rot on the program at progIdx and on its postroll, the
// rotation position that is current once the program has aired.
func setRotation(entries []domain.EpgEntry, progIdx, rot int) {
	r := rot
	entries[progIdx].RotationNext = &r
	if progIdx+1 < len(entries) {
		p := rot
		entries[progIdx+1].RotationNext = &p
	}
}

// breakSources resolves the preroll, midroll and postroll sources of a block
// as the channel and block filler rules define them.
func (in channelInput) breakSources(blockID string, state *selectionState) (pre, mid, post *midrollSource) {
	src := func(kind domain.FillerKind) *midrollSource {
		return &midrollSource{rule: resolveFillerRule(in.FillerRules, kind, blockID, in.ChannelID), pools: in.PoolItems, durations: in.CatalogDuration, state: state}
	}
	return src(domain.FillerPreRoll), src(domain.FillerMidRoll), src(domain.FillerPostRoll)
}

// warnLoopCap logs, once per fill, that the guard above stopped it.
func warnLoopCap(channelID string, sourceID *string) {
	id := ""
	if sourceID != nil {
		id = *sourceID
	}
	log.Printf("materializer: looping fill capped: schedule item %s on channel %s stopped after %d entries (items or fillers this short are almost certainly bad catalog data)", id, channelID, maxLoopEntries)
}
