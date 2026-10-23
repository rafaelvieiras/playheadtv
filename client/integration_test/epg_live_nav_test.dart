// E2E suite for vertical navigation in the full-screen EPG: from a live
// program Up/Down land on the live program of the adjacent row, not on the
// (older) program covering the focused cell's start. Same harness as
// epg_layout_test.dart. One real app boot in this file (see AGENTS.md).
//
// The materializer discards entries that already ended when it writes, so the
// "older" programs are real short ones that end a few seconds after the
// schedule is seeded; the suite waits for them to be over before it boots.
// Fixture (seconds after the base time B), channel 1 is the one tuned at boot:
//   ch 1: ONE LIVE (3h) from B+14
//   ch 2: TWO LIVE (3h) from B+4
//   ch 3: THREE OLD B..B+8 (covers TWO LIVE's start), THREE LIVE from B+8
//   ch 4: FOUR OLDA B..B+6, FOUR OLDB B+6..B+14 (covers THREE LIVE's start),
//         FOUR LIVE from B+14
// The old rule would land Down from TWO LIVE on THREE OLD and Down from
// THREE LIVE on FOUR OLDB. Only Up/Down/Left/Right/Select/Back are used.
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('EPG live navigation (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    const second = 1000;
    const threeHours = 3 * 60 * 60 * second;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      final base = DateTime.now().toUtc().add(const Duration(seconds: 25));
      DateTime at(int seconds) => base.add(Duration(seconds: seconds));

      // (channel number, [(title, duration ms)], block start)
      final channels = <(int, List<(String, int)>, DateTime)>[
        (1, [('ONE LIVE', threeHours)], at(14)),
        (2, [('TWO LIVE', threeHours)], at(4)),
        (3, [('THREE OLD', 8 * second), ('THREE LIVE', threeHours)], at(0)),
        (4, [('FOUR OLDA', 6 * second), ('FOUR OLDB', 8 * second), ('FOUR LIVE', threeHours)], at(0)),
      ];
      for (final (number, programs, start) in channels) {
        final channelId = await backend.createChannel(number: number, name: 'Live Nav $number');
        final source = await backend.createLocalFolderMediaSource(name: 'Live nav source $number');
        final ids = await backend.syncCatalogItems(source, [
          for (final (title, duration) in programs)
            {'external_id': 'livenav-$title', 'kind': 'episode', 'title': title, 'duration_ms': duration},
        ]);
        final block = await backend.createBlockWithItems(ids);
        await backend.createScheduleOnce(channelId: channelId, blockId: block, startAt: start);
        await backend.waitForEpgEntries(channelId, programs.length);
      }
      if (!DateTime.now().toUtc().isBefore(base)) fail('seeding took longer than the lead time before the base');
      // Everything is live, the older programs are over; ch 1 started a moment ago.
      final wait = at(16).difference(DateTime.now().toUtc());
      if (!wait.isNegative) await Future<void>.delayed(wait);
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

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-live-nav');
    });

    Future<void> pumpUntil(WidgetTester tester, bool Function() condition, {Duration timeout = const Duration(seconds: 15), String? failMessage}) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met within $timeout');
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    bool osdVisible(WidgetTester tester) {
      final finder = find.ancestor(of: find.byKey(const Key('channel-osd')), matching: find.byType(AnimatedOpacity));
      if (finder.evaluate().isEmpty) return false;
      return tester.widget<AnimatedOpacity>(finder).opacity == 1;
    }

    Future<void> openEpg(WidgetTester tester) async {
      await pumpUntil(tester, () {
        final finder = find.byKey(const Key('osd-channel-number'));
        return finder.evaluate().isNotEmpty && tester.widget<Text>(finder).data == '1';
      }, failMessage: 'channel 1 never tuned');
      await pumpUntil(tester, () => !osdVisible(tester), timeout: const Duration(seconds: 8), failMessage: 'OSD never hid');
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Select (OSD hidden) did not show the OSD');
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => find.byKey(const Key('epg-details-panel')).evaluate().isNotEmpty, failMessage: 'EPG never finished loading');
    }

    String detailsTitle(WidgetTester tester) {
      final panel = find.byKey(const Key('epg-details-panel'));
      return tester.widget<Text>(find.descendant(of: panel, matching: find.byType(Text)).first).data ?? '';
    }

    Future<void> press(WidgetTester tester, LogicalKeyboardKey key) async {
      await pressKey(tester, key);
      await tester.pump();
    }

    testWidgets('Up/Down from a live program keep the live column; Left/Right re-anchor', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await openEpg(tester);
      expect(detailsTitle(tester), 'ONE LIVE');

      await press(tester, LogicalKeyboardKey.arrowDown);
      expect(detailsTitle(tester), 'TWO LIVE');
      await press(tester, LogicalKeyboardKey.arrowDown);
      expect(detailsTitle(tester), 'THREE LIVE', reason: 'not THREE OLD, which covers the start of TWO LIVE');
      await press(tester, LogicalKeyboardKey.arrowDown);
      expect(detailsTitle(tester), 'FOUR LIVE', reason: 'not FOUR OLDB, which covers the start of THREE LIVE');
      await press(tester, LogicalKeyboardKey.arrowUp);
      expect(detailsTitle(tester), 'THREE LIVE');
      await press(tester, LogicalKeyboardKey.arrowUp);
      expect(detailsTitle(tester), 'TWO LIVE');

      // Not live: the cell covering the anchor (the start of THREE OLD).
      await press(tester, LogicalKeyboardKey.arrowDown);
      await press(tester, LogicalKeyboardKey.arrowLeft);
      expect(detailsTitle(tester), 'THREE OLD');
      await press(tester, LogicalKeyboardKey.arrowDown);
      expect(detailsTitle(tester), 'FOUR OLDA');

      // Back to the live cell: the live column again.
      await press(tester, LogicalKeyboardKey.arrowRight);
      await press(tester, LogicalKeyboardKey.arrowRight);
      expect(detailsTitle(tester), 'FOUR LIVE');
      await press(tester, LogicalKeyboardKey.arrowUp);
      expect(detailsTitle(tester), 'THREE LIVE');

      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('epg-overlay')).evaluate().isEmpty, failMessage: 'Back did not close the EPG');
    });

    // Auto-repeat as a keyboard or a remote sends it: down, a repeat about every 35 ms, up.
    Future<void> hold(WidgetTester tester, LogicalKeyboardKey key, {int ms = 900}) async {
      await ensurePlayerFocus(tester);
      await tester.sendKeyDownEvent(key);
      final stop = DateTime.now().add(Duration(milliseconds: ms));
      while (DateTime.now().isBefore(stop)) {
        await tester.sendKeyRepeatEvent(key);
        await tester.pump(const Duration(milliseconds: 35));
      }
      await tester.sendKeyUpEvent(key);
      await tester.pump();
    }

    testWidgets('holding an arrow runs the focus along the grid, one axis at a time', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await openEpg(tester);
      expect(detailsTitle(tester), 'ONE LIVE');

      // A single tap still moves exactly one row.
      await press(tester, LogicalKeyboardKey.arrowDown);
      expect(detailsTitle(tester), 'TWO LIVE');

      // Holding Down runs through the remaining rows down to the last one.
      await hold(tester, LogicalKeyboardKey.arrowDown);
      expect(detailsTitle(tester), 'FOUR LIVE', reason: 'holding Down must keep moving down');
      // Holding Up runs back to the first row.
      await hold(tester, LogicalKeyboardKey.arrowUp);
      expect(detailsTitle(tester), 'ONE LIVE', reason: 'holding Up must keep moving up');

      // Same on the time axis, along the row of the last channel (OLDA, OLDB, LIVE).
      await hold(tester, LogicalKeyboardKey.arrowDown);
      expect(detailsTitle(tester), 'FOUR LIVE');
      await hold(tester, LogicalKeyboardKey.arrowLeft);
      expect(detailsTitle(tester), 'FOUR OLDA', reason: 'holding Left must keep moving left');
      await hold(tester, LogicalKeyboardKey.arrowRight);
      expect(detailsTitle(tester), 'FOUR LIVE', reason: 'holding Right must keep moving right');

      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('epg-overlay')).evaluate().isEmpty, failMessage: 'Back did not close the EPG');
    });
  });
}
