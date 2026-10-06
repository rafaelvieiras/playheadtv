// E2E suite for the cap on how long a tail is attributed to the program before
// it (lib/epg_guide.dart): the OSD and the EPG overlay must agree. Within the
// cap both name the program; past it the OSD shows the channel's own name (no
// program details, no progress) and the guide shows a gap (no live cell);
// neither ever shows a tail clip's title.
//
// Same harness as osd_filler_test.dart (real Go backend + fake video server,
// no `pumpAndSettle`). The cap is shrunk to 20s for the run
// (`AppConfig.overrideForTest(tailAttributionCap:)`) and the fixture video is
// 8s, so the schedule is: PROG A (0-8s), then a tail of 8s clips until PROG C
// at 90s. A's attribution of the tail ends at 8s + 20s = 28s. Two real app
// boots in this file on purpose (see AGENTS.md on boot counts).
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

  group('tail attribution cap (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    const second = 1000;
    const tailStart = 8;
    const capSeconds = 20;
    const capEnd = tailStart + capSeconds;

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
        tailAttributionCap: const Duration(seconds: capSeconds),
      );
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    Future<(String, String, DateTime)> seedChannel(int number) async {
      final name = 'Tail Cap $number';
      final channelId = await backend.createChannel(number: number, name: name);
      final source = await backend.createLocalFolderMediaSource(name: 'Tail cap source $number');
      final ids = await backend.syncCatalogItems(source, [
        for (final (key, title) in [
          ('a', 'PROG A'),
          ('c', 'PROG C'),
          ('t1', 'FILLER tail 1'),
          ('t2', 'FILLER tail 2'),
          ('t3', 'FILLER tail 3'),
        ])
          {'external_id': 'tailcap-$number-$key', 'kind': 'episode', 'title': title, 'duration_ms': 8 * second},
      ]);
      final blockA = await backend.createBlockWithItems([ids[0]]);
      final blockC = await backend.createBlockWithItems([ids[1]]);
      await backend.createFillerRule(channelId: channelId, kind: 'tail', catalogItemIds: [ids[2], ids[3], ids[4]]);
      final start = DateTime.now().toUtc();
      await backend.createScheduleWithItems(
        channelId: channelId,
        items: [(blockId: blockA, startAt: start), (blockId: blockC, startAt: start.add(const Duration(seconds: 90)))],
      );
      await backend.waitForEpgEntries(channelId, 4);
      return (channelId, name, start);
    }

    Future<void> pumpUntil(
      WidgetTester tester,
      bool Function() condition, {
      Duration timeout = const Duration(seconds: 30),
      String? failMessage,
    }) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met within $timeout');
        await tester.pump(const Duration(milliseconds: 100));
      }
    }

    double elapsed(DateTime start) => DateTime.now().toUtc().difference(start).inMilliseconds / 1000;

    Future<void> pumpUntilElapsed(WidgetTester tester, DateTime start, num seconds) =>
        pumpUntil(tester, () => elapsed(start) >= seconds, timeout: const Duration(seconds: 90), failMessage: 'clock did not reach ${seconds}s');

    String? osdTitle(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-item-title'));
      return finder.evaluate().isEmpty ? null : tester.widget<Text>(finder).data;
    }

    bool osdVisible(WidgetTester tester) {
      final finder = find.ancestor(of: find.byKey(const Key('channel-osd')), matching: find.byType(AnimatedOpacity));
      if (finder.evaluate().isEmpty) return false;
      return tester.widget<AnimatedOpacity>(finder).opacity == 1;
    }

    Future<void> openEpg(WidgetTester tester) async {
      await pumpUntil(tester, () => !osdVisible(tester), timeout: const Duration(seconds: 8), failMessage: 'OSD never hid');
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Enter did not show the OSD');
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-details-panel')).evaluate().isNotEmpty,
        failMessage: 'EPG never finished loading',
      );
    }

    Future<void> closeEpg(WidgetTester tester) async {
      await pressKey(tester, LogicalKeyboardKey.escape);
      await tester.pump();
      await pumpUntil(tester, () => find.byKey(const Key('epg-overlay')).evaluate().isEmpty, failMessage: 'Back did not close the EPG');
    }

    Iterable<Element> rowCells(String channelId) => find
        .byWidgetPredicate((w) => w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-cell-$channelId-'))
        .evaluate();

    Finder inEpg(Finder f) => find.descendant(of: find.byKey(const Key('epg-overlay')), matching: f);

    Finder detailsTitle(String title) =>
        find.descendant(of: find.byKey(const Key('epg-details-panel')), matching: find.text(title));

    void expectChannelOnlyOsd(WidgetTester tester, String channelName) {
      expect(osdTitle(tester), channelName, reason: 'past the cap the OSD names the channel');
      expect(find.byKey(const Key('osd-program-times')), findsNothing);
      expect(find.byKey(const Key('osd-ends-at')), findsNothing);
      expect(tester.widget<LinearProgressIndicator>(find.byKey(const Key('osd-progress'))).value, 0);
    }

    testWidgets('within the cap the OSD and the guide name the program, past it the OSD names the channel and the guide shows a gap',
        (tester) async {
      final (channelId, name, start) = await seedChannel(41);
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      await prefs.setString('last_watched_channel_id', channelId);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'the OSD never appeared');

      // The tail has started and is inside its cap (8-28s).
      await pumpUntilElapsed(tester, start, tailStart + 2);
      expect(elapsed(start), lessThan(capEnd - 6), reason: 'booted too late to observe the tail inside its cap');
      expect(osdTitle(tester), 'PROG A', reason: 'a tail inside its cap is described as the program before it');
      await openEpg(tester);
      expect(elapsed(start), lessThan(capEnd), reason: 'the guide opened after the cap');
      expect(detailsTitle('PROG A'), findsOneWidget);
      expect(find.byKey(const Key('epg-details-progress')), findsOneWidget, reason: 'the tail inside its cap is PROG A on air');
      expect(inEpg(find.textContaining('FILLER')), findsNothing);
      await closeEpg(tester);

      // Past the cap, with the tail still airing (it runs to 90s).
      await pumpUntilElapsed(tester, start, capEnd + 3);
      expectChannelOnlyOsd(tester, name);
      expect(find.textContaining('FILLER'), findsNothing);
      await openEpg(tester);
      expect(rowCells(channelId), hasLength(2), reason: 'PROG A and PROG C only: no cell for the tail');
      expect(detailsTitle('PROG A'), findsOneWidget);
      expect(find.byKey(const Key('epg-details-progress')), findsNothing, reason: 'nothing is live in the gap');
      expect(inEpg(find.textContaining('FILLER')), findsNothing);
      await pressKey(tester, LogicalKeyboardKey.arrowRight);
      await tester.pump();
      expect(detailsTitle('PROG C'), findsOneWidget, reason: 'the tail is not a stop');
    });

    testWidgets('tuning in past the cap: the OSD names the channel and the guide has no live cell', (tester) async {
      final (channelId, name, start) = await seedChannel(42);
      final wait = const Duration(seconds: capEnd + 4) - DateTime.now().toUtc().difference(start);
      if (!wait.isNegative) await Future<void>.delayed(wait);
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      await prefs.setString('last_watched_channel_id', channelId);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'the OSD never appeared');

      expect(elapsed(start), lessThan(80), reason: 'booted after the tail ended');
      expectChannelOnlyOsd(tester, name);
      expect(find.textContaining('FILLER'), findsNothing);
      await openEpg(tester);
      expect(rowCells(channelId), hasLength(2));
      expect(find.byKey(const Key('epg-details-progress')), findsNothing, reason: 'nothing is live in the gap');
      expect(inEpg(find.textContaining('FILLER')), findsNothing);
    });
  });
}
