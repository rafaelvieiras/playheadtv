// E2E suite for the responsive EPG layout (time header, now marker, vertical
// scroll following the focused row, lazy horizontal fetch). Same harness as
// epg_overlay_test.dart (read it first). At most 2 real app boots in this file
// (see AGENTS.md, "Several real PlayheadSpikeApp boots ...").
import 'package:client/main.dart';
import 'package:client/ui/ui_scale.dart';
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

  group('responsive EPG layout (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    // Many channels, so the rows cannot all fit on screen. Channel 1 (the one
    // tuned at boot) has 12 programs of 30 min (6h), longer than the grid's
    // initial window, to exercise the lazy fetch past its end.
    const channelCount = 14;
    late Map<int, String> channelIds;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      channelIds = {};
      for (var number = 1; number <= channelCount; number++) {
        final channelId = await backend.createChannel(number: number, name: 'Layout Channel $number');
        channelIds[number] = channelId;
        final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Layout source $number');
        final itemCount = number == 1 ? 12 : 2;
        final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
          for (var i = 0; i < itemCount; i++)
            {
              'external_id': 'layout-$number-$i',
              'kind': 'episode',
              'title': 'LAYOUT $number ITEM $i',
              'duration_ms': 1800000,
            },
        ]);
        final blockId = await backend.createBlockWithItems(catalogItemIds);
        await backend.createScheduleOnce(
          channelId: channelId,
          blockId: blockId,
          startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 3)),
        );
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
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-layout');
    });

    Future<void> pumpUntil(
      WidgetTester tester,
      bool Function() condition, {
      Duration timeout = const Duration(seconds: 15),
      String? failMessage,
    }) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) {
          fail(failMessage ?? 'condition not met within $timeout');
        }
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    String? osdChannelNumberText(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-channel-number'));
      if (finder.evaluate().isEmpty) return null;
      return tester.widget<Text>(finder).data;
    }

    bool osdVisible(WidgetTester tester) {
      final finder = find.ancestor(
        of: find.byKey(const Key('channel-osd')),
        matching: find.byType(AnimatedOpacity),
      );
      if (finder.evaluate().isEmpty) return false;
      return tester.widget<AnimatedOpacity>(finder).opacity == 1;
    }

    Future<void> openEpg(WidgetTester tester) async {
      await pumpUntil(tester, () => osdChannelNumberText(tester) == '1', failMessage: 'channel 1 never tuned');
      await pumpUntil(
        tester,
        () => !osdVisible(tester),
        timeout: const Duration(seconds: 8),
        failMessage: 'OSD never hid on its own after the boot auto-show',
      );
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Enter (OSD hidden) did not show the OSD');
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-details-panel')).evaluate().isNotEmpty,
        failMessage: 'EPG never finished loading (epg-details-panel did not appear)',
      );
    }

    String detailsTitle(WidgetTester tester) {
      final panel = find.byKey(const Key('epg-details-panel'));
      return tester.widget<Text>(find.descendant(of: panel, matching: find.byType(Text)).first).data ?? '';
    }

    testWidgets('shows the time header, the now marker, the playing channel and follows the focused row vertically', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await openEpg(tester);

      expect(find.byKey(const Key('epg-time-header')), findsOneWidget);
      expect(find.byKey(const Key('epg-date-label')), findsOneWidget);
      // 3h visible span with a tick every 30 min: at least 6 ticks on screen.
      final ticks = find.byWidgetPredicate((w) => w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-tick-'));
      expect(ticks.evaluate().length, greaterThanOrEqualTo(6));
      expect(find.byKey(const Key('epg-now-marker')), findsOneWidget);
      expect(find.byKey(Key('epg-playing-${channelIds[1]}')), findsOneWidget);
      expect(find.byKey(const Key('epg-details-progress')), findsOneWidget, reason: 'the live program shows a progress bar');

      // Program cell width is proportional to its duration: the 30 min live
      // cell is about a sixth of the 3h span.
      final rows = find.byKey(const Key('epg-rows'));
      final cellRect = tester.getRect(find.byWidgetPredicate((w) => w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-cell-${channelIds[1]}-')).first);
      final rowsRect = tester.getRect(rows);
      final channelColumn = 220 * UiScale.of(tester.element(rows));
      final expectedWidth = (rowsRect.width - channelColumn) / 6;
      expect((cellRect.width - expectedWidth).abs(), lessThan(expectedWidth * 0.2));

      // Reach the last channel using only Down: the list has to scroll.
      for (var i = 1; i < channelCount; i++) {
        await pressKey(tester, LogicalKeyboardKey.arrowDown);
        await tester.pump(const Duration(milliseconds: 250));
      }
      await tester.pump(const Duration(milliseconds: 400));
      final controller = tester.widget<ListView>(rows).controller!;
      expect(controller.offset, greaterThan(0), reason: 'focus moved past the fold, the rows must have scrolled');
      final lastRow = tester.getRect(find.byKey(Key('epg-row-${channelIds[channelCount]}')));
      expect(lastRow.bottom, lessThanOrEqualTo(rowsRect.bottom + 0.5));
      expect(lastRow.top, greaterThanOrEqualTo(rowsRect.top - 0.5));
    });

    testWidgets('moving right past the loaded window fetches more of the schedule', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await openEpg(tester);

      expect(detailsTitle(tester), 'LAYOUT 1 ITEM 0');
      final liveHeight = tester.getSize(find.byKey(const Key('epg-details-panel'))).height;
      // Item 9 starts 4.5h from now, past the initial window (4h ahead).
      for (var i = 0; i < 9; i++) {
        await pressKey(tester, LogicalKeyboardKey.arrowRight);
        await tester.pump(const Duration(milliseconds: 300));
      }
      await pumpUntil(
        tester,
        () => detailsTitle(tester) == 'LAYOUT 1 ITEM 9',
        failMessage: 'focus never reached the program beyond the initial window (lazy fetch failed)',
      );
      expect(find.byKey(const Key('epg-details-progress')), findsNothing, reason: 'a future program has no progress bar');
      expect(tester.getSize(find.byKey(const Key('epg-details-panel'))).height, liveHeight, reason: 'the progress slot keeps the panel height stable');
      // The focused cell is scrolled into view.
      await tester.pump(const Duration(milliseconds: 400));
      final rowsRect = tester.getRect(find.byKey(const Key('epg-rows')));
      final channelColumn = 220 * UiScale.of(tester.element(find.byKey(const Key('epg-rows'))));
      final cell = find.byWidgetPredicate((w) => w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-cell-${channelIds[1]}-') && ((w as Container).decoration as BoxDecoration).border!.top.color == Colors.white);
      expect(cell, findsOneWidget);
      final rect = tester.getRect(cell);
      expect(rect.left, greaterThanOrEqualTo(rowsRect.left + channelColumn - 1));
      expect(rect.right, lessThanOrEqualTo(rowsRect.right + 1));
    });
  });
}
