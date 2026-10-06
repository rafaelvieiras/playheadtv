// E2E suite for hiding filler in the guide — same harness as
// epg_overlay_test.dart (read that first): the real app against a real Go
// backend (BackendFixture) and a fake video server (VideoFixture), driven with
// the six D-pad keys only (arrows, Enter, Escape), never `pumpAndSettle`.
//
// Channel 3 airs a 5-minute preroll, program A, a midroll, program B, a
// postroll and a tail up to program C, so "now" sits INSIDE the hidden preroll
// when the app boots. Channel 8 has no preroll, so program A is on air and the
// first thing after it is a midroll (the OSD "Up next" line must skip it).
// Two real app boots in this file on purpose (see AGENTS.md on boot counts).
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

  group('hidden filler in the guide (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late Map<int, String> channelIds;
    late DateTime scheduleStart;

    const minute = 60000;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      channelIds = {};
      scheduleStart = DateTime.now().toUtc().subtract(const Duration(seconds: 3));

      Future<List<String>> items(String sourceId, String prefix, List<(String, String, int)> specs) =>
          backend.syncCatalogItems(sourceId, [
            for (final (key, title, minutes) in specs)
              {'external_id': '$prefix-$key', 'kind': 'episode', 'title': title, 'duration_ms': minutes * minute},
          ]);

      for (final number in [3, 8]) {
        final channelId = await backend.createChannel(number: number, name: 'Lineup Channel $number');
        channelIds[number] = channelId;
        final source = await backend.createLocalFolderMediaSource(name: 'Filler source $number');
        final ids = await items(source, 'f$number', [
          ('a', 'PROG A', 30),
          ('b', 'PROG B', 30),
          ('c', 'PROG C', 30),
          ('pre', 'FILLER pre', 5),
          ('mid', 'FILLER mid', 5),
          ('post', 'FILLER post', 5),
          ('t1', 'FILLER tail 1', 10),
          ('t2', 'FILLER tail 2', 10),
          ('t3', 'FILLER tail 3', 10),
        ]);
        final blockAB = await backend.createBlockWithItems([ids[0], ids[1]]);
        final blockC = await backend.createBlockWithItems([ids[2]]);
        if (number == 3) {
          await backend.createFillerRule(channelId: channelId, kind: 'preroll', catalogItemIds: [ids[3]]);
        }
        await backend.createFillerRule(channelId: channelId, kind: 'midroll', catalogItemIds: [ids[4]]);
        await backend.createFillerRule(channelId: channelId, kind: 'postroll', catalogItemIds: [ids[5]]);
        await backend.createFillerRule(channelId: channelId, kind: 'tail', catalogItemIds: [ids[6], ids[7], ids[8]]);
        await backend.createScheduleWithItems(
          channelId: channelId,
          items: [
            (blockId: blockAB, startAt: scheduleStart),
            (blockId: blockC, startAt: scheduleStart.add(const Duration(hours: 2))),
          ],
        );
        await backend.waitForEpgEntries(channelId, 8);
      }

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
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-filler');
    });

    Future<void> pumpUntil(
      WidgetTester tester,
      bool Function() condition, {
      Duration timeout = const Duration(seconds: 15),
      String? failMessage,
    }) async {
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

    String? osdChannelNumber(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-channel-number'));
      return finder.evaluate().isEmpty ? null : tester.widget<Text>(finder).data;
    }

    Future<void> waitForChannelNumber(WidgetTester tester, int number) => pumpUntil(
          tester,
          () => osdChannelNumber(tester) == '$number',
          failMessage: 'expected OSD showing channel $number, but it stayed on "${osdChannelNumber(tester)}"',
        );

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

    Iterable<Element> rowCells(String channelId) => find
        .byWidgetPredicate((w) => w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-cell-$channelId-'))
        .evaluate();

    String hhmm(DateTime t) {
      final l = t.toLocal();
      return '${l.hour.toString().padLeft(2, '0')}:${l.minute.toString().padLeft(2, '0')}';
    }

    Finder inEpg(Finder f) => find.descendant(of: find.byKey(const Key('epg-overlay')), matching: f);

    Finder detailsTitle(String title) =>
        find.descendant(of: find.byKey(const Key('epg-details-panel')), matching: find.text(title));

    testWidgets('the EPG shows programs only, extends them over the hidden filler and keeps navigation on them', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 3);
      await openEpg(tester);

      // One cell per program on each row, none for any of the fillers.
      expect(rowCells(channelIds[3]!), hasLength(3));
      expect(rowCells(channelIds[8]!), hasLength(3));
      expect(inEpg(find.textContaining('FILLER')), findsNothing);
      expect(inEpg(find.text('PROG A')), findsWidgets);
      expect(inEpg(find.text('PROG C')), findsWidgets);

      // "Now" is inside the 5-minute preroll, which belongs to program A: it
      // is the live cell, focused first, and spans preroll + A + midroll.
      expect(detailsTitle('PROG A'), findsOneWidget);
      final range = '${hhmm(scheduleStart)}–${hhmm(scheduleStart.add(const Duration(minutes: 40)))}';
      expect(find.descendant(of: find.byKey(const Key('epg-details-panel')), matching: find.textContaining(range)), findsOneWidget);
      expect(find.byKey(const Key('epg-details-progress')), findsOneWidget, reason: 'a live filler counts as its program being on air');

      // Right/Left step over programs only: A, B, C and back.
      await pressKey(tester, LogicalKeyboardKey.arrowRight);
      await tester.pump();
      expect(detailsTitle('PROG B'), findsOneWidget);
      await pressKey(tester, LogicalKeyboardKey.arrowRight);
      await tester.pump();
      expect(detailsTitle('PROG C'), findsOneWidget, reason: 'the postroll and the tail are not stops');
      await pressKey(tester, LogicalKeyboardKey.arrowLeft);
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.arrowLeft);
      await tester.pump();
      expect(detailsTitle('PROG A'), findsOneWidget);

      // Select on the live cell switches to that channel (it is already
      // playing, so the EPG just closes) even though the live entry is hidden.
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-overlay')).evaluate().isEmpty,
        failMessage: 'Select on the live program did not close the EPG',
      );
      expect(osdChannelNumber(tester), '3');
    });

    testWidgets('the OSD "Up next" line skips filler and shows the next real program', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelIds[8]!);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 8);

      // Program A is on air; right after it comes the midroll, then B.
      await pumpUntil(
        tester,
        () => find.text('Up next: PROG B').evaluate().isNotEmpty,
        failMessage: 'the OSD never showed "Up next: PROG B"',
      );
      expect(find.textContaining('FILLER'), findsNothing);
    });
  });
}
