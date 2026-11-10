// Which tiles the mosaic shows. The ring and the phone's column read the same
// inputs and must answer differently: the frame can hold twelve and fills its
// empty cells at random, a column holds every channel in a readable order.
import 'dart:math';

import 'package:client/mosaic_nav.dart';
import 'package:client/mosaic_tiles.dart';
import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';

PlayheadChannel ch(int n) => PlayheadChannel(id: 'c$n', number: n, name: 'Channel $n');

List<PlayheadChannel> channels(int n) => [for (var i = 1; i <= n; i++) ch(i)];

List<String> idsOf(List<MosaicTile> tiles) => [for (final t in tiles) t.isSettings ? 'settings' : t.channel!.id];

Random seeded() => Random(7);

void main() {
  group('ring (television)', () {
    test('the featured tile is the most recently watched channel', () {
      final built = buildMosaicTiles(
        channels: channels(20),
        recentIds: ['c9', 'c3'],
        mode: MosaicLayoutMode.ring,
        random: seeded(),
      );
      expect(built.featured, 0);
      expect(built.tiles[0].channel!.id, 'c9');
      expect(built.tiles[1].channel!.id, 'c3', reason: 'then the rest of the recent history');
    });

    test('a history that names a channel that is gone falls back to the lowest number', () {
      final built = buildMosaicTiles(
        channels: channels(4),
        recentIds: ['deleted', 'c3'],
        mode: MosaicLayoutMode.ring,
        random: seeded(),
      );
      expect(built.tiles[0].channel!.id, 'c1');
      expect(built.tiles[1].channel!.id, 'c3');
    });

    test('it caps at the frame and ends with Settings', () {
      final built = buildMosaicTiles(
        channels: channels(40),
        recentIds: const [],
        mode: MosaicLayoutMode.ring,
        random: seeded(),
      );
      expect(built.tiles.length, mosaicMaxTiles);
      expect(built.tiles.last.isSettings, isTrue);
      expect(built.tiles.where((t) => !t.isSettings).length, mosaicRingSlots.length);
      // Never the same channel twice: the frame is a set, not a list.
      expect(idsOf(built.tiles).toSet().length, built.tiles.length);
    });

    test('the same seed fills the frame the same way', () {
      List<String> run() => idsOf(
            buildMosaicTiles(
              channels: channels(30),
              recentIds: const [],
              mode: MosaicLayoutMode.ring,
              random: Random(11),
            ).tiles,
          );
      expect(run(), run());
    });
  });

  group('list (phone)', () {
    test('it holds every channel, no cap, Settings last', () {
      final built = buildMosaicTiles(
        channels: channels(40),
        recentIds: const [],
        mode: MosaicLayoutMode.list,
        random: seeded(),
      );
      expect(built.tiles.where((t) => !t.isSettings).length, 40);
      expect(built.tiles.last.isSettings, isTrue);
      expect(idsOf(built.tiles).toSet().length, built.tiles.length);
    });

    test('the featured channel comes first, then the rest by number', () {
      final built = buildMosaicTiles(
        channels: channels(5),
        recentIds: const ['c4'],
        mode: MosaicLayoutMode.list,
        random: seeded(),
      );
      expect(idsOf(built.tiles), ['c4', 'c1', 'c2', 'c3', 'c5', 'settings']);
    });

    test('the recent history keeps its order before the numbered tail', () {
      final built = buildMosaicTiles(
        channels: channels(6),
        recentIds: const ['c5', 'c2'],
        mode: MosaicLayoutMode.list,
        random: seeded(),
      );
      expect(idsOf(built.tiles), ['c5', 'c2', 'c1', 'c3', 'c4', 'c6', 'settings']);
    });

    test('ids the channel list no longer has are skipped, not invented', () {
      final built = buildMosaicTiles(
        channels: [ch(2), ch(7)],
        recentIds: const ['gone', 'c7', 'also-gone'],
        mode: MosaicLayoutMode.list,
      );
      // The *head* of the history is the only one that can be featured (the
      // frame has always read it that way), and it points at a channel that is
      // gone, so the featured channel falls back to the lowest number; c7 is
      // still recent enough to come next.
      expect(idsOf(built.tiles), ['c2', 'c7', 'settings']);
    });

    test('a channel list that does not shuffle: two runs are identical', () {
      List<String> run() => idsOf(
            buildMosaicTiles(channels: channels(9), recentIds: const [], mode: MosaicLayoutMode.list).tiles,
          );
      expect(run(), run());
    });
  });

  test('no channels means no tiles at all, not a lone Settings row', () {
    for (final mode in MosaicLayoutMode.values) {
      final built = buildMosaicTiles(channels: const [], recentIds: const [], mode: mode);
      expect(built.tiles, isEmpty, reason: '$mode');
      expect(built.featured, 0);
    }
  });
}
