package materializer

import (
	"fmt"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
)

// fillerRatioLimit is the share of a program's duration the fillers
// configured between two programs may take before a warning is raised.
const fillerRatioLimit = 0.5

// poolMaxDuration is the longest item of a rule's pool (0 for no rule or no
// usable item): the worst case one pick can add.
func (in channelInput) poolMaxDuration(rule *domain.FillerRule) int64 {
	if rule == nil {
		return 0
	}
	var longest int64
	for _, it := range in.PoolItems[rule.FillerPoolID] {
		if d := in.CatalogDuration[it.CatalogItemID]; d > longest {
			longest = d
		}
	}
	return longest
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
// the preroll, midroll and postroll pools), exceed fillerRatioLimit of their
// shortest program. Nothing is limited; this only reports it.
func (in channelInput) fillerRatioWarnings() []Warning {
	var out []Warning
	seen := map[string]bool{}
	for _, si := range in.ScheduleItems {
		endless := si.Recurrence == domain.RecurrenceContinuous || in.DynamicBlockIDs[si.BlockID]
		if !endless || seen[si.BlockID] {
			continue
		}
		seen[si.BlockID] = true
		var filler int64
		for _, kind := range []domain.FillerKind{domain.FillerPreRoll, domain.FillerMidRoll, domain.FillerPostRoll} {
			filler += in.poolMaxDuration(resolveFillerRule(in.FillerRules, kind, si.BlockID, in.ChannelID))
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
