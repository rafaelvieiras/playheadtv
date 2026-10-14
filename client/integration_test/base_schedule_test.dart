// E2E suite for a channel with a pinned schedule plus a base schedule
// (`channel.base_schedule_id`, docs/ARCHITECTURE.md "Base schedule"). The
// client only reads the materialized guide, so what this proves is that the
// entries the backend lays down for a base layer (a program cut at the pinned
// occurrence, then resumed with `item_offset_ms`) are guided and played
// correctly. Same harness as segment_playback_test.dart (real Go backend +
// fake video server, no `pumpAndSettle`). One real app boot in this file (see
// AGENTS.md on boot counts).
//
// Lineup, relative to P (the pinned occurrence's start), with one 160s file
// served for every item:
//
//   P-70s .. P      BASE MOVIE (150s item), cut at the pinned start
//   P     .. P+8s   PINNED SHOW (8s item)
//   P+8s  .. P+80s  BASE MOVIE again, item_offset_ms = 78s (72s left of it,
//                   above the one minute the backend keeps)
//   P+80s .. P+100s BASE NEXT (a second base item, 20s)
import 'package:client/cache/offline_runtime.dart';
import 'package:client/epg_guide.dart';
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:video_player/video_player.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

// One sample of the player: seconds since P, what the OSD shows, and the
// position (seconds into the file) of every initialized controller with
// whether it is playing.
typedef Sample = ({double elapsed, String? title, List<({double position, bool playing})> players});

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('base schedule under a pinned occurrence (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;

    const second = 1000;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start(clipSeconds: 160);
      await backend.start();
      deviceToken = await backend.createDeviceToken();
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
    });

    Sample sample(WidgetTester tester, DateTime pinnedStart) {
      final players = <({double position, bool playing})>[];
      for (final element in find.byType(VideoPlayer).evaluate()) {
        final controller = (element.widget as VideoPlayer).controller;
        try {
          if (controller.value.isInitialized) {
            players.add((position: controller.value.position.inMilliseconds / 1000, playing: controller.value.isPlaying));
          }
        } catch (_) {
          // a controller disposed between the find and the read.
        }
      }
      return (
        elapsed: DateTime.now().toUtc().difference(pinnedStart).inMilliseconds / 1000,
        title: osdTitle(tester),
        players: players,
      );
    }

    testWidgets('guide follows the pinned occurrence and the base around it; the resumed program seeks to its offset', (tester) async {
      final channelId = await backend.createChannel(number: 71, name: 'Base schedule 71');
      final source = await backend.createLocalFolderMediaSource(name: 'Base schedule source');
      final ids = await backend.syncCatalogItems(source, [
        for (final (key, title, seconds) in [('movie', 'BASE MOVIE', 150), ('next', 'BASE NEXT', 20), ('pinned', 'PINNED SHOW', 8)])
          {'external_id': 'base-$key', 'kind': 'movie', 'title': title, 'duration_ms': seconds * second},
      ]);
      final baseMovie = await backend.createBlockWithItems([ids[0]]);
      final baseNext = await backend.createBlockWithItems([ids[1]]);
      final pinned = await backend.createBlockWithItems([ids[2]]);

      // The pinned occurrence is in the near future so the cut part of the
      // base program is still in the guide when the app boots.
      // Whole milliseconds, the precision of the API.
      final p = DateTime.fromMillisecondsSinceEpoch(DateTime.now().add(const Duration(seconds: 60)).millisecondsSinceEpoch, isUtc: true);
      final baseStart = p.subtract(const Duration(seconds: 70));
      await backend.createScheduleWithItems(
        channelId: channelId,
        asBase: true,
        items: [(blockId: baseMovie, startAt: baseStart), (blockId: baseNext, startAt: baseStart.add(const Duration(seconds: 150)))],
      );
      await backend.createScheduleWithItems(channelId: channelId, items: [(blockId: pinned, startAt: p)]);
      await backend.waitForEpgEntries(channelId, 4);

      // The backend's side: a contiguous lineup, no hole, the base program cut
      // at the pinned start and resumed at its offset.
      final raw = await backend.getEpg(channelId, fromMs: baseStart.millisecondsSinceEpoch - 60000);
      final entries = raw.take(4).toList();
      expect([for (final e in entries) e['title']], ['BASE MOVIE', 'PINNED SHOW', 'BASE MOVIE', 'BASE NEXT'], reason: '$raw');
      expect([for (final e in entries) e['kind']], everyElement('program'));
      for (var i = 1; i < entries.length; i++) {
        expect(entries[i]['start_at'], entries[i - 1]['end_at'], reason: 'a hole before entry $i: $raw');
      }
      expect(entries[0]['end_at'], p.millisecondsSinceEpoch);
      expect(entries[1]['start_at'], p.millisecondsSinceEpoch);
      expect(entries[1]['end_at'], p.millisecondsSinceEpoch + 8 * second);
      expect(entries[0]['item_offset_ms'] ?? 0, 0);
      expect(entries[2]['item_offset_ms'], 78 * second);
      expect(entries[2]['duration_ms'], 150 * second);
      expect((entries[2]['end_at'] as int) - (entries[2]['start_at'] as int), 72 * second);
      expect(entries[0]['split_group'], isNull);
      expect(entries[2]['split_group'], isNull);

      final sinceP = DateTime.now().toUtc().difference(p);
      expect(sinceP, lessThan(const Duration(seconds: -15)), reason: 'setup took too long: the pinned occurrence is $sinceP away');

      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
      );
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelId);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'the OSD never appeared');

      // The guide as the client holds it: three programs, the base one shown
      // only up to the pinned start, then again after it.
      final runtime = await OfflineRuntime.ensure();
      await pumpUntil(tester, () => runtime.cache.allEntries(channelId).length >= 4, failMessage: 'the cache was never filled');
      final cells = buildGuideCells(runtime.cache.allEntries(channelId));
      expect([for (final c in cells) c.entry.title], ['BASE MOVIE', 'PINNED SHOW', 'BASE MOVIE', 'BASE NEXT']);
      expect(cells[0].displayEnd.toUtc(), p, reason: 'the cut base program is shown past the pinned start');
      expect(cells[1].displayStart.toUtc(), p);
      expect(cells[1].displayEnd.toUtc(), p.add(const Duration(seconds: 8)));
      expect(cells[2].displayStart.toUtc(), p.add(const Duration(seconds: 8)));
      expect(cells[2].displayEnd.toUtc(), p.add(const Duration(seconds: 80)));
      for (var i = 1; i < cells.length; i++) {
        expect(cells[i].displayStart, cells[i - 1].displayEnd, reason: 'a false hole before cell $i');
      }

      // Sample the player through the whole lineup.
      final samples = <Sample>[];
      await pumpUntil(
        tester,
        () {
          samples.add(sample(tester, p));
          return samples.last.elapsed >= 90;
        },
        timeout: const Duration(seconds: 160),
        failMessage: 'playback never got past the base program that resumes',
      );

      Iterable<double> playing(Sample s) => s.players.where((x) => x.playing).map((x) => x.position);
      Iterable<Sample> within(double from, double to) => samples.where((s) => s.elapsed >= from && s.elapsed <= to);
      double? firstElapsed(String title, double after) {
        for (final s in samples.where((s) => s.elapsed >= after && s.title == title)) {
          return s.elapsed;
        }
        return null;
      }

      // Before the pinned start: the base program from its own position in
      // the file (tuned in about 70 - 60 seconds into it).
      final early = within(-45, -2).where((s) => playing(s).isNotEmpty).toList();
      expect(early, isNotEmpty, reason: 'nothing played before the pinned start');
      expect(early.every((s) => s.title == null || s.title == 'BASE MOVIE'), isTrue);
      for (final s in early.take(4)) {
        final expected = 70 + s.elapsed;
        expect(playing(s).any((x) => (x - expected).abs() < 5), isTrue, reason: 'position ${s.players} at ${s.elapsed}s, expected about $expected');
      }

      // The cut base program hands over to the pinned occurrence at its own
      // clipDuration (the pinned start), not at the item's 150s.
      final pinnedAt = firstElapsed('PINNED SHOW', -5);
      expect(pinnedAt, isNotNull, reason: 'the pinned occurrence never reached the OSD');
      expect(pinnedAt, inInclusiveRange(-1.5, 4), reason: 'the pinned occurrence appeared at $pinnedAt s from its start');
      expect(within(1, 6).any((s) => playing(s).any((x) => x < 9)), isTrue, reason: 'the pinned item did not play from the start of the file');

      // The resumed base program: the file is entered at offset + the time
      // elapsed in the entry (78s at P+8s), never at zero.
      final resumed = within(9.8, 12.5).where((s) => playing(s).isNotEmpty).toList();
      expect(resumed, isNotEmpty, reason: 'nothing was playing in the resumed base program');
      for (final s in resumed) {
        final expected = 78 + (s.elapsed - 8);
        expect(playing(s).any((x) => (x - expected).abs() < 4), isTrue,
            reason: 'position ${s.players} at ${s.elapsed}s, expected about $expected (the item offset was missed)');
      }
      expect(within(9, 12).any((s) => s.title == 'BASE MOVIE'), isTrue);

      // Its transition fires at the entry's own end (P+80s), neither earlier
      // nor at the item's own remaining time.
      expect(within(15, 74).where((s) => s.title != null).every((s) => s.title == 'BASE MOVIE'), isTrue,
          reason: 'the resumed program was replaced early');
      final nextAt = firstElapsed('BASE NEXT', 60);
      expect(nextAt, isNotNull, reason: 'BASE NEXT never reached the OSD');
      expect(nextAt, inInclusiveRange(76, 86), reason: 'BASE NEXT appeared at $nextAt s from the pinned start, expected about 80');

      // The guide itself through the real EPG: a cell for the resumed program
      // and the next one, each its own.
      await openEpg(tester);
      final cellKeys = [for (final w in epgCellsOf(channelId)) (w.key as ValueKey<String>).value];
      expect(cellKeys, contains('epg-cell-$channelId-${entries[2]['id']}'), reason: 'cells: $cellKeys');
      expect(cellKeys, contains('epg-cell-$channelId-${entries[3]['id']}'), reason: 'cells: $cellKeys');
    });
  });
}
