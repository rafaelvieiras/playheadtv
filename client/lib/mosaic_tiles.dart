// Which tiles the mosaic shows, and in what order (pure: no widgets).
//
// The two arrangements the client has need different answers from the same
// inputs, so the choice lives here and the page only picks a view:
//
//  - [MosaicLayoutMode.ring] (television, `widgets/mosaic_frame.dart`): the
//    featured channel plus up to 11 surrounding ones, the most recently watched
//    first and then a random fill, which is what the frame has always done. The
//    cap is the frame itself: 12 cells and one of them is Settings.
//  - [MosaicLayoutMode.list] (phone, `widgets/phone_mosaic_view.dart`): every
//    channel, no cap, because a column has no cells to run out of. The same
//    "recent first" order tells a viewer something there — the channels they
//    were just watching are the top of the list — so the ring's order is kept
//    and only the truncation goes away.
//
// Index 0 is the featured tile in both, and the Settings tile is always last.
import 'dart:math';

import 'mosaic_nav.dart';
import 'playhead_api.dart';

/// One tile: a channel, or the fixed Settings entry.
class MosaicTile {
  final PlayheadChannel? channel;
  final bool isSettings;

  const MosaicTile.forChannel(PlayheadChannel this.channel) : isSettings = false;
  const MosaicTile.forSettings()
      : channel = null,
        isSettings = true;
}

/// [tiles] for [mode], and the index of the featured channel among them (always
/// 0 today, kept explicit because the views read it rather than assume it).
({List<MosaicTile> tiles, int featured}) buildMosaicTiles({
  required List<PlayheadChannel> channels,
  required List<String> recentIds,
  required MosaicLayoutMode mode,
  Random? random,
}) {
  if (channels.isEmpty) return (tiles: const <MosaicTile>[], featured: 0);

  final byId = {for (final c in channels) c.id: c};
  final sortedByNumber = [...channels]..sort((a, b) => a.number.compareTo(b.number));

  // The featured channel is the most recently watched one that still exists,
  // else the lowest channel number: the same choice the player's own fallback
  // makes for "where to start".
  PlayheadChannel featured;
  Iterable<String> recentRest;
  if (recentIds.isNotEmpty && byId.containsKey(recentIds.first)) {
    featured = byId[recentIds.first]!;
    recentRest = recentIds.skip(1);
  } else {
    featured = sortedByNumber.first;
    recentRest = recentIds;
  }

  final shown = <String>{featured.id};
  final surrounding = <PlayheadChannel>[];
  final limit = mode == MosaicLayoutMode.ring ? mosaicRingSlots.length - 1 : channels.length;
  for (final id in recentRest) {
    if (surrounding.length >= limit) break;
    final candidate = byId[id];
    if (candidate != null && shown.add(candidate.id)) surrounding.add(candidate);
  }
  if (surrounding.length < limit) {
    final remaining = channels.where((c) => !shown.contains(c.id)).toList();
    if (mode == MosaicLayoutMode.ring) {
      // The frame fills its empty cells at random: a waiting screen that shows
      // the same eleven channels in the same order every time reads like a
      // menu, not like a satellite screen.
      remaining.shuffle(random ?? Random());
    } else {
      // A list is read, not glanced at: the rest of the channels go in the
      // order the guide uses, so a viewer can find one by number.
      remaining.sort((a, b) => a.number.compareTo(b.number));
    }
    for (final candidate in remaining) {
      if (surrounding.length >= limit) break;
      if (shown.add(candidate.id)) surrounding.add(candidate);
    }
  }

  return (
    tiles: [
      MosaicTile.forChannel(featured),
      for (final c in surrounding) MosaicTile.forChannel(c),
      const MosaicTile.forSettings(),
    ],
    featured: 0,
  );
}
