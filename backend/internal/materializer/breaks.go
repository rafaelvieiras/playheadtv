package materializer

import (
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
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

	// cuts, when set, splits a program into segments with a midroll between
	// them (see segmenter); nil plays every program whole.
	cuts    CutPlanner
	blockID string // the block the loop plays, for the planner (see CutItem)

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

// partEntries turns the parts of one break into back-to-back entries of kind
// starting at start.
func (b *breakLoop) partEntries(kind domain.EpgEntryKind, parts breakParts, start time.Time, rot int) []domain.EpgEntry {
	out := make([]domain.EpgEntry, 0, len(parts))
	at := start
	for _, p := range parts {
		out = append(out, b.fillerEntry(kind, p.item, at, p.dur, rot))
		at = at.Add(time.Duration(p.dur) * time.Millisecond)
	}
	return out
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
	parts := b.post.pickBreak()
	if len(parts) == 0 {
		return nil, cursor, true
	}
	end = cursor.Add(time.Duration(parts.total()) * time.Millisecond)
	if end.After(limit) {
		return nil, cursor, false
	}
	return b.partEntries(domain.EpgPostRoll, parts, cursor, rot), end, true
}

// unit airs the owed midroll and preroll, the program and its postroll,
// starting at cursor, provided the whole unit ends by limit. Each of the
// three breaks is a plain rule's single item or a break template's steps (see
// pickBreak). plan builds the program entry for the start it will really have
// (after the prefix); it may refuse. rotBefore is the rotation position stored
// on the prefix fillers. progIdx is the program's index within the returned
// entries; everything after it is its postroll, and the caller stores the
// rotation position the program and its postroll leave behind (setRotation).
func (b *breakLoop) unit(cursor, limit time.Time, rotBefore int, plan func(start time.Time) (domain.EpgEntry, bool)) (entries []domain.EpgEntry, end time.Time, progIdx int, ok bool) {
	var mid, pre breakParts
	if b.oweMid {
		mid = b.mid.pickBreak()
	}
	if b.owePre {
		pre = b.pre.pickBreak()
	}
	start := cursor.Add(time.Duration(mid.total()+pre.total()) * time.Millisecond)
	prog, planned := plan(start)
	if !planned {
		return nil, cursor, 0, false
	}
	// A program cut into segments (cuts.go) is one unit: its segments and the
	// breaks between them sit between the preroll and the postroll.
	progEntries := segmenter{planner: b.cuts, mid: b.mid, channelID: b.channelID, blockID: b.blockID}.split(prog)
	progEnd := spanEnd(progEntries)
	post := b.post.pickBreak()
	end = progEnd.Add(time.Duration(post.total()) * time.Millisecond)
	if end.After(limit) {
		return nil, cursor, 0, false
	}

	entries = append(entries, b.partEntries(domain.EpgMidRoll, mid, cursor, rotBefore)...)
	entries = append(entries, b.partEntries(domain.EpgPreRoll, pre, cursor.Add(time.Duration(mid.total())*time.Millisecond), rotBefore)...)
	progIdx = len(entries)
	entries = append(entries, progEntries...)
	entries = append(entries, b.partEntries(domain.EpgPostRoll, post, progEnd, rotBefore)...)
	b.owePre, b.oweMid, b.owePost = true, true, false
	return entries, end, progIdx, true
}

// setRotation stores rot on the program at progIdx and on its postroll (every
// entry after it), the rotation position that is current once the program has
// aired.
func setRotation(entries []domain.EpgEntry, progIdx, rot int) {
	for i := progIdx; i < len(entries); i++ {
		r := rot
		entries[i].RotationNext = &r
	}
}

// breakSources resolves the preroll, midroll and postroll sources of a block:
// a break template or a filler rule per kind (see resolveBreak).
func (in channelInput) breakSources(blockID string, state *selectionState) (pre, mid, post *midrollSource) {
	return in.breakSource(domain.FillerPreRoll, blockID, state), in.breakSource(domain.FillerMidRoll, blockID, state), in.breakSource(domain.FillerPostRoll, blockID, state)
}

// warnLoopCap logs, once per fill, that the guard above stopped it.
func warnLoopCap(channelID string, sourceID *string) {
	id := ""
	if sourceID != nil {
		id = *sourceID
	}
	logging.Warnf("materializer: looping fill capped: schedule item %s on channel %s stopped after %d entries (items or fillers this short are almost certainly bad catalog data)", id, channelID, maxLoopEntries)
}
