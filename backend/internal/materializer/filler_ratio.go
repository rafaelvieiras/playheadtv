package materializer

import (
	"fmt"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// fillerRatioLimit is the share of a program's duration the fillers
// configured between two programs may take before a warning is raised.
const fillerRatioLimit = 0.5

// unfilledSlotMin is the shortest uncovered stretch at the end of a slot worth
// an unfilled-slot warning (the guide ignores seams under a minute too).
const unfilledSlotMin = time.Minute

// unfilledSlotWarnings flags the blocks whose slot can end with a stretch no
// entry covers: whole items, the tail rule and then the channel's fallback
// rule (none applies, or this wouldn't be reported) leave it empty, and the
// client runs ahead of the guide through it. It expands a week of the
// channel's configuration read-only, so it reports what a regeneration would
// really leave, per block with the longest stretch seen. Nothing is blocked.
func (in channelInput) unfilledSlotWarnings(now time.Time) []Warning {
	var found []residual
	in.Residuals = &found
	expandChannel(in, now, now, now.Add(7*24*time.Hour), newSelectionState())
	longest := map[string]time.Duration{}
	var order []string
	for _, r := range found {
		if _, ok := longest[r.BlockID]; !ok {
			order = append(order, r.BlockID)
		}
		if r.Length > longest[r.BlockID] {
			longest[r.BlockID] = r.Length
		}
	}
	var out []Warning
	for _, blockID := range order {
		out = append(out, Warning{
			Kind:    WarningUnfilledSlot,
			BlockID: blockID,
			Message: fmt.Sprintf("its slot can end with up to %s of empty time: nothing in the block, its tail filler or the channel fallback fills the rest before the next scheduled item. Add a tail filler with more fit attempts or a channel fallback rule.", formatMs(longest[blockID].Milliseconds())),
		})
	}
	return out
}

// shortestProgram is the shortest program a block can air: a catalog item, or
// an episode of a series, criteria or dynamic walk (0 when unknown).
func (in channelInput) shortestProgram(blockID string) int64 {
	var shortest int64
	consider := func(d int64) {
		if d > 0 && (shortest == 0 || d < shortest) {
			shortest = d
		}
	}
	for _, bi := range in.BlockItems[blockID] {
		if bi.IsWalked() {
			if run := in.Series[bi.ID]; run != nil {
				for _, ep := range run.episodes {
					consider(ep.DurationMs)
				}
			}
			continue
		}
		consider(effectiveDuration(bi, in.CatalogDuration))
	}
	return shortest
}

// fillerRatioWarnings flags the blocks that air as an endless sequence (the
// continuous item's, and dynamic blocks under a timed item) whose configured
// fillers between two programs, in the worst case (the longest item of each of
// the preroll, midroll and postroll pools; a break template counts the sum of
// its steps, a fill step at its target), exceed fillerRatioLimit of their
// shortest program. Nothing is limited; this only reports it.
func (in channelInput) fillerRatioWarnings() []Warning {
	var out []Warning
	seen := map[string]bool{}
	for _, si := range append(append([]domain.ScheduleItem(nil), in.ScheduleItems...), in.BaseScheduleItems...) {
		endless := si.Recurrence == domain.RecurrenceContinuous || in.DynamicBlockIDs[si.BlockID]
		if !endless || seen[si.BlockID] {
			continue
		}
		seen[si.BlockID] = true
		var filler int64
		for _, kind := range []domain.FillerKind{domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll} {
			filler += in.breakSource(kind, si.BlockID, nil).worstCase()
		}
		program := in.shortestProgram(si.BlockID)
		if filler == 0 || program == 0 || float64(filler) <= fillerRatioLimit*float64(program) {
			continue
		}
		out = append(out, Warning{
			Kind:    WarningFillerRatio,
			BlockID: si.BlockID,
			Message: fmt.Sprintf("the preroll, midroll and postroll configured for this block can add up to %s between two programs, more than %d%% of its shortest program (%s)", formatMs(filler), int(fillerRatioLimit*100), formatMs(program)),
		})
	}
	return out
}

func formatMs(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d >= time.Minute {
		return fmt.Sprintf("%.1f min", d.Minutes())
	}
	return fmt.Sprintf("%d s", int(d.Seconds()))
}
