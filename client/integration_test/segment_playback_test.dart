// E2E suite for an item played in segments (epg_entry item_offset_ms): one
// catalog item cut into two segments with a break between them. Same harness as
// osd_filler_test.dart (real Go backend + fake video server, no
// `pumpAndSettle`). Two real app boots in this file on purpose (see AGENTS.md
// on boot counts).
//
// How a split is triggered: each channel gets a cut rule set that cuts every
// item at 10s. The fixture video is one 24s file served
// for every item (each entry plays a window of it), so the lineup is:
//
//    0-10s   SPLIT MOVIE, segment 1 (file 0-10s)
//   10-18s   midroll, 8s (the file's first 8s again; hidden from the guide)
//   18-32s   SPLIT MOVIE, segment 2 (file 10-24s)
//
// What must hold: the same file resumes at its resume point after the break (a
// second controller, prepared and seeked while the break plays), the guide
// draws ONE program over the whole 0-32s, and the OSD never describes the
// break.
import 'package:client/cache/offline_runtime.dart';
import 'package:client/epg_guide.dart';
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:client/video/video_engine.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

// One sample of the player: seconds since the schedule start, what the OSD
// shows, and the position (seconds into the file) of every initialized
// controller with whether it is playing.
typedef Sample = ({double elapsed, String? title, String? times, double? progress, List<({double position, bool playing})> players});

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('segments of a split item (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;

    const second = 1000;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start(clipSeconds: 24);
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

    // A channel airing the 24s item whose schedule started [startedAgo] ago.
    Future<(String, DateTime)> seedChannel(int number, Duration startedAgo) async {
      final channelId = await backend.createChannel(number: number, name: 'Segments $number');
      final source = await backend.createLocalFolderMediaSource(name: 'Segments source $number');
      final ids = await backend.syncCatalogItems(source, [
        for (final (key, title, seconds) in [('movie', 'SPLIT MOVIE', 24), ('mid', 'FILLER break', 8)])
          {'external_id': 'seg-$number-$key', 'kind': 'movie', 'title': title, 'duration_ms': seconds * second},
      ]);
      final block = await backend.createBlockWithItems([ids[0]]);
      // A second block a minute later ends the first one's span.
      final next = await backend.createBlockWithItems([ids[0]]);
      await backend.createFillerRule(channelId: channelId, kind: 'midroll', catalogItemIds: [ids[1]]);
      await backend.createCutOffsetsRule(channelId: channelId, offsetsMs: [10 * second]);
      final start = DateTime.now().toUtc().subtract(startedAgo);
      await backend.createScheduleWithItems(
        channelId: channelId,
        items: [(blockId: block, startAt: start), (blockId: next, startAt: start.add(const Duration(minutes: 1)))],
      );
      return (channelId, start);
    }

    void bootConfig() {
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
      );
    }

    Future<void> boot(WidgetTester tester, String channelId) async {
      bootConfig();
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelId);
      await tester.pumpWidget(const PlayheadSpikeApp());
    }

    Sample sample(WidgetTester tester, DateTime scheduleStart) {
      final title = osdTitle(tester);
      final timesFinder = find.byKey(const Key('osd-program-times'));
      final progressFinder = find.byKey(const Key('osd-progress'));
      final players = <({double position, bool playing})>[];
      for (final element in find.byType(VideoSurface).evaluate()) {
        final controller = (element.widget as VideoSurface).playback;
        try {
          if (controller.state.isInitialized) {
            players.add((position: controller.state.position.inMilliseconds / 1000, playing: controller.state.isPlaying));
          }
        } catch (_) {
          // a controller disposed between the find and the read.
        }
      }
      return (
        elapsed: DateTime.now().toUtc().difference(scheduleStart).inMilliseconds / 1000,
        title: title,
        times: timesFinder.evaluate().isEmpty ? null : tester.widget<Text>(timesFinder).data,
        progress: progressFinder.evaluate().isEmpty ? null : tester.widget<LinearProgressIndicator>(progressFinder).value,
        players: players,
      );
    }

    Iterable<Element> rowCells(String channelId) => find
        .byWidgetPredicate((w) => w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-cell-$channelId-'))
        .evaluate();

    testWidgets('the same file resumes at its offset after the break; one program in the guide and the OSD', (tester) async {
      final (channelId, start) = await seedChannel(61, Duration.zero);
      await backend.waitForEpgEntries(channelId, 3);

      // The backend's side: two program segments of one group with the break
      // between them, the second one starting 10s into the item.
      final raw = await backend.getEpg(channelId, fromMs: start.millisecondsSinceEpoch - 60000);
      final first = raw.take(3).toList();
      expect([for (final e in first) e['kind']], ['program', 'midroll', 'program']);
      expect([for (final e in first) (e['end_at'] as int) - (e['start_at'] as int)], [10 * second, 8 * second, 14 * second]);
      expect(first[0].containsKey('item_offset_ms'), isFalse);
      expect(first[2]['item_offset_ms'], 10 * second);
      expect(first[0]['split_group'], isNotNull);
      expect(first[2]['split_group'], first[0]['split_group']);
      expect(first[1].containsKey('split_group'), isFalse);
      expect(first[2]['title'], 'SPLIT MOVIE');
      expect(first[2]['duration_ms'], 24 * second);

      await boot(tester, channelId);
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'the OSD never appeared');

      // The guide, as the client holds it: segments, break and all, but ONE
      // program cell over the whole airing.
      final runtime = await OfflineRuntime.ensure();
      await pumpUntil(tester, () => runtime.cache.allEntries(channelId).length >= 3, failMessage: 'the cache was never filled');
      final cells = buildGuideCells(runtime.cache.allEntries(channelId));
      final airing = cells.first;
      expect(airing.entry.title, 'SPLIT MOVIE');
      expect(airing.displayEnd.difference(airing.displayStart), const Duration(seconds: 32));
      expect(airing.absorbed.map((e) => e.kind), ['midroll', 'program']);
      expect(cells.where((c) => c.entry.title == 'FILLER break'), isEmpty, reason: 'the break got a guide cell');

      // Sample the player over the whole airing.
      final samples = <Sample>[];
      await pumpUntil(
        tester,
        () {
          samples.add(sample(tester, start));
          return samples.last.elapsed >= 26;
        },
        timeout: const Duration(seconds: 60),
        failMessage: 'playback never reached the second segment',
      );

      bool during(double from, double to, bool Function(Sample) test) => samples.where((s) => s.elapsed >= from && s.elapsed <= to).any(test);
      Iterable<double> playing(Sample s) => s.players.where((p) => p.playing).map((p) => p.position);

      // OSD: always the movie, one constant span, progress on the wall clock
      // over all 32s; the break never reaches it.
      final withOsd = samples.where((s) => s.title != null).toList();
      expect(withOsd, isNotEmpty);
      expect(withOsd.where((s) => s.title!.contains('FILLER')), isEmpty, reason: 'the break reached the OSD');
      expect(withOsd.every((s) => s.title == 'SPLIT MOVIE'), isTrue);
      expect(withOsd.map((s) => s.times).toSet(), hasLength(1));
      for (final s in withOsd.where((s) => s.progress != null && s.elapsed <= 26)) {
        expect(s.progress, closeTo(s.elapsed / 32, 0.1), reason: 'OSD progress at ${s.elapsed}s');
      }

      // Segment 1 plays the start of the file.
      expect(during(1, 8, (s) => playing(s).any((p) => p > 0.3 && p < 10)), isTrue, reason: 'segment 1 was not playing the start of the file');
      // The break plays the file from zero (the filler is the same fixture
      // file) while a second controller waits at the resume point.
      expect(during(11, 16, (s) => playing(s).isNotEmpty && playing(s).every((p) => p < 9)), isTrue, reason: 'the break did not play from zero');
      expect(during(13, 19.5, (s) => s.players.any((p) => !p.playing && p.position > 9.5 && p.position < 11.5)), isTrue,
          reason: 'no controller was prepared at the resume point (10s) while the break played');
      // Segment 2 starts the same file at the resume point (10s), not from zero:
      // the first seconds it plays sit at 10s and little after (checked only
      // for the first 3s: in this harness a controller that was preloaded and
      // then started stalls and jumps to the end of the file about 4s in, with
      // or without a split, which says nothing about the offset).
      final seg2 = samples.where((s) => s.elapsed >= 19.8 && s.elapsed <= 22.5 && playing(s).isNotEmpty).toList();
      expect(seg2, isNotEmpty, reason: 'nothing was playing in segment 2');
      for (final s in seg2) {
        expect(playing(s).every((p) => p >= 9.5 && p <= 15), isTrue, reason: 'position ${playing(s)} at ${s.elapsed}s, expected the file at about 10s');
      }

      // The guide itself, through the real EPG: one cell for the airing, none
      // for the second segment or the break.
      await openEpg(tester);
      final ids = {for (final e in first) e['id'] as String};
      final cellKeys = [for (final e in rowCells(channelId)) (e.widget.key as ValueKey<String>).value];
      expect(cellKeys.where((k) => k == 'epg-cell-$channelId-${first[0]['id']}'), hasLength(1), reason: 'cells: $cellKeys');
      expect(cellKeys.where((k) => k == 'epg-cell-$channelId-${first[1]['id']}' || k == 'epg-cell-$channelId-${first[2]['id']}'), isEmpty,
          reason: 'the break or the second segment got a cell: $cellKeys (ids $ids)');
    });

    testWidgets('tuning in inside the second segment seeks to the offset plus the time elapsed in it', (tester) async {
      // The schedule started 24s ago: segment 2 (18-32s) is 6s old, so the
      // file should be at about 16s. The backend only keeps entries that have
      // not ended, so the first segment and the break are already gone.
      final (channelId, start) = await seedChannel(62, const Duration(seconds: 24));
      await backend.waitForEpgEntries(channelId, 1);
      await boot(tester, channelId);
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'the OSD never appeared');

      final samples = <Sample>[];
      await pumpUntil(
        tester,
        () {
          final s = sample(tester, start);
          if (s.players.any((p) => p.playing)) samples.add(s);
          return samples.length >= 8;
        },
        timeout: const Duration(seconds: 30),
        failMessage: 'nothing played after tuning in',
      );
      expect(osdTitle(tester), 'SPLIT MOVIE');
      for (final s in samples) {
        final expected = 10 + (s.elapsed - 18);
        expect(s.players.where((p) => p.playing).any((p) => (p.position - expected).abs() < 4), isTrue,
            reason: 'position ${s.players} at ${s.elapsed}s, expected about $expected (a seek to the item offset was missed)');
      }
    });
  });
}
