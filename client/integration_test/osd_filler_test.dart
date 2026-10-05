// E2E suite for the OSD while filler airs: a filler is part of the program it
// is attributed to, so the OSD must keep describing that program (title, times,
// progress over the program plus its filler) and never show the filler's own
// title. Same harness as epg_filler_test.dart (real Go backend + fake video
// server, no `pumpAndSettle`). Two real app boots in this file on purpose (see
// AGENTS.md on boot counts).
//
// The fixture video is 8s long, so the lineup is built from 8s items. The
// schedule plays [pre] A [mid] B [post]: A's displayed span is preroll + A +
// midroll (0-24s) and B's is B + postroll (24-40s).
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/video_fixture.dart';

// One sample of what the OSD shows, with the time it was taken at.
typedef Sample = ({double elapsed, String title, String times, double progress});

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('OSD while filler airs (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    const second = 1000;
    // Displayed spans: PROG A is 0-24s (preroll, program, midroll), PROG B
    // 24-40s (program, postroll).
    const spanASeconds = 24;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      final deviceToken = await backend.createDeviceToken();
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
      );
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    // A channel whose schedule started [startedAgo] ago.
    Future<(String, DateTime)> seedChannel(int number, Duration startedAgo) async {
      final channelId = await backend.createChannel(number: number, name: 'Osd Filler $number');
      final source = await backend.createLocalFolderMediaSource(name: 'Osd filler source $number');
      final ids = await backend.syncCatalogItems(source, [
        for (final (key, title, seconds) in [
          ('a', 'PROG A', 8),
          ('b', 'PROG B', 8),
          ('pre', 'FILLER pre', 8),
          ('mid', 'FILLER mid', 8),
          ('post', 'FILLER post', 8),
        ])
          {'external_id': 'osdf-$number-$key', 'kind': 'episode', 'title': title, 'duration_ms': seconds * second},
      ]);
      final block = await backend.createBlockWithItems([ids[0], ids[1]]);
      // A second block a minute later ends the first one's span: without it
      // the list would repeat for the whole schedule window.
      final next = await backend.createBlockWithItems([ids[0]]);
      await backend.createFillerRule(channelId: channelId, kind: 'preroll', catalogItemIds: [ids[2]]);
      await backend.createFillerRule(channelId: channelId, kind: 'midroll', catalogItemIds: [ids[3]]);
      await backend.createFillerRule(channelId: channelId, kind: 'postroll', catalogItemIds: [ids[4]]);
      final start = DateTime.now().toUtc().subtract(startedAgo);
      await backend.createScheduleWithItems(
        channelId: channelId,
        items: [(blockId: block, startAt: start), (blockId: next, startAt: start.add(const Duration(minutes: 1)))],
      );
      await backend.waitForEpgEntries(channelId, 5);
      return (channelId, start);
    }

    Future<void> pumpUntil(
      WidgetTester tester,
      bool Function() condition, {
      Duration timeout = const Duration(seconds: 20),
      String? failMessage,
    }) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met within $timeout');
        await tester.pump(const Duration(milliseconds: 100));
      }
    }

    String? osdTitle(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-item-title'));
      return finder.evaluate().isEmpty ? null : tester.widget<Text>(finder).data;
    }

    String? osdTimes(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-program-times'));
      return finder.evaluate().isEmpty ? null : tester.widget<Text>(finder).data;
    }

    double? osdProgress(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-progress'));
      return finder.evaluate().isEmpty ? null : tester.widget<LinearProgressIndicator>(finder).value;
    }

    Sample? sample(WidgetTester tester, DateTime scheduleStart) {
      final title = osdTitle(tester);
      final times = osdTimes(tester);
      final progress = osdProgress(tester);
      if (title == null || times == null || progress == null) return null;
      final elapsed = DateTime.now().toUtc().difference(scheduleStart).inMilliseconds / 1000;
      return (elapsed: elapsed, title: title, times: times, progress: progress);
    }

    testWidgets('a program keeps its title and progresses across its filler (preroll, midroll, postroll)', (tester) async {
      final (channelId, start) = await seedChannel(31, Duration.zero);
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      await prefs.setString('last_watched_channel_id', channelId);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'the OSD never appeared');

      final samples = <Sample>[];
      await pumpUntil(
        tester,
        () {
          final s = sample(tester, start);
          if (s != null) samples.add(s);
          return s != null && s.elapsed >= 39;
        },
        timeout: const Duration(seconds: 60),
        failMessage: 'playback never reached the postroll of PROG B',
      );

      expect(samples.where((s) => s.title.contains('FILLER')), isEmpty, reason: 'a filler title reached the OSD');
      bool during(String title, double from, double to) =>
          samples.any((s) => s.title == title && s.elapsed >= from && s.elapsed <= to);
      // Preroll (0-8s), program (8-16s) and midroll (16-24s) are all PROG A;
      // program (24-32s) and postroll (32-40s) are PROG B.
      expect(during('PROG A', 0, 7), isTrue, reason: 'no PROG A sample during the preroll');
      expect(during('PROG A', 9, 15), isTrue, reason: 'no PROG A sample during the program');
      expect(during('PROG A', 17, 23), isTrue, reason: 'no PROG A sample during the midroll');
      expect(during('PROG B', 28, 31), isTrue, reason: 'no PROG B sample during the program');
      expect(during('PROG B', 33, 39), isTrue, reason: 'no PROG B sample during the postroll');
      expect(samples.where((s) => s.elapsed <= 22).every((s) => s.title == 'PROG A'), isTrue,
          reason: 'the OSD left PROG A while its filler aired');
      expect(samples.where((s) => s.elapsed >= 30 && s.elapsed <= 39).every((s) => s.title == 'PROG B'), isTrue,
          reason: 'the OSD left PROG B while its postroll aired');

      // One constant displayed span per program, and progress running across
      // all of it (24s for A, 16s for B), on the wall clock.
      final onA = samples.where((s) => s.title == 'PROG A').toList();
      final onB = samples.where((s) => s.title == 'PROG B' && s.elapsed >= 28).toList();
      expect(onA.map((s) => s.times).toSet(), hasLength(1));
      expect(onB.map((s) => s.times).toSet(), hasLength(1));
      for (final s in onA.where((s) => s.elapsed <= 22)) {
        expect(s.progress, closeTo(s.elapsed / spanASeconds, 0.08), reason: 'PROG A progress at ${s.elapsed}s');
      }
      for (final s in onB.where((s) => s.elapsed <= 39)) {
        expect(s.progress, closeTo((s.elapsed - 24) / 16, 0.08), reason: 'PROG B progress at ${s.elapsed}s');
      }
      expect(onA.last.progress, greaterThan(0.8));
      expect(onB.last.progress, greaterThan(0.8));
    });

    testWidgets('tuning in during a midroll shows the previous program it belongs to', (tester) async {
      // Boots about 19s into the schedule: inside the midroll (16-24s), whose
      // program ended before the first fetched entry. The backend only keeps
      // what it materialized, so the app starts late instead of the schedule
      // starting early.
      final (channelId, start) = await seedChannel(32, Duration.zero);
      final wait = const Duration(seconds: 17) - DateTime.now().toUtc().difference(start);
      if (!wait.isNegative) await Future<void>.delayed(wait);
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      await prefs.setString('last_watched_channel_id', channelId);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'the OSD never appeared');

      final samples = <Sample>[];
      final first = sample(tester, start)!;
      samples.add(first);
      expect(first.elapsed, lessThan(23), reason: 'booted too late to be inside the midroll');
      expect(first.title, 'PROG A', reason: 'a midroll on air is described as the program before it');
      expect(first.elapsed, greaterThan(16), reason: 'booted too early to be inside the midroll');
      expect(first.progress, greaterThan(0.6));
      await pumpUntil(
        tester,
        () {
          final s = sample(tester, start);
          if (s != null) samples.add(s);
          return s != null && s.title == 'PROG B';
        },
        timeout: const Duration(seconds: 30),
        failMessage: 'the OSD never switched to PROG B',
      );
      expect(samples.where((s) => s.title.contains('FILLER')), isEmpty);
      expect(samples.where((s) => s.elapsed < 22).every((s) => s.title == 'PROG A'), isTrue);
    });
  });
}
