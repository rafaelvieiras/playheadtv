// E2E suite for the full-screen EPG — same pattern as mosaic_test.dart/
// channel_navigation_test.dart (read those two first): boots the real app
// against a real Go backend (BackendFixture) and a fake video server
// (VideoFixture, playing the role of "Jellyfin"), drives keyboard navigation
// through the real `integration_test` binding, and never uses
// `tester.pumpAndSettle()` (the OSD's 500ms ticker would never settle) — every
// wait-for-condition uses a manual `pumpUntil`.
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

  group('full-screen EPG (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    // 2 channels (3 and 8): each with 2 items of ~30min (duration_ms
    // 1800000), the first starting ~3s in the past — the exact same pattern as
    // mosaic_test.dart/channel_navigation_test.dart. This keeps the FIRST item
    // of each channel "live" for the whole test, and the SECOND item (which
    // starts ~30min later) as a future program — used by the long-press
    // test.
    const channelNumbers = [3, 8];
    late Map<int, String> channelIds;
    late Map<int, String> channelNames;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();

      channelIds = {};
      channelNames = {
        for (final n in channelNumbers) n: 'EPG Channel $n',
      };

      for (final number in channelNumbers) {
        final channelId = await backend.createChannel(number: number, name: channelNames[number]!);
        channelIds[number] = channelId;
        final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'EPG source $number');
        final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
          {
            'external_id': 'epg-$number-a',
            'kind': 'episode',
            'title': 'EPG PROGRAM $number A (LIVE)',
            'duration_ms': 1800000,
          },
          {
            'external_id': 'epg-$number-b',
            'kind': 'episode',
            'title': 'EPG PROGRAM $number B (FUTURE)',
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
      // Always lands on the player (not the mosaic) with the lowest-number
      // channel, same pattern as channel_navigation_test.dart.
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-epg');
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

    Future<void> waitForChannelNumber(
      WidgetTester tester,
      int expectedNumber, {
      Duration timeout = const Duration(seconds: 15),
    }) async {
      await pumpUntil(
        tester,
        () => osdChannelNumberText(tester) == '$expectedNumber',
        timeout: timeout,
        failMessage: 'expected OSD showing channel $expectedNumber, but it stayed on "${osdChannelNumberText(tester)}"',
      );
    }

    // Wait for the OSD to auto-hide + Enter + Enter, same as the OSD/EPG test
    // in channel_navigation_test.dart.
    Future<void> openEpg(WidgetTester tester) async {
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
        () => find.byKey(const Key('epg-overlay')).evaluate().isNotEmpty,
        failMessage: 'second Enter (OSD already visible) did not open the EPG',
      );
      // Fetching the grid (GET /epg + GET /catalog-items, see
      // EpgOverlayState._load) is asynchronous — `epg-overlay` (the Stack
      // root) already exists from the first frame (with the loading spinner),
      // but `moveRow`/`moveColumn`/`handleSelectKeyEvent` refuse to act while
      // `_loading` is true. Wait for the details panel (only rendered after
      // the load) before sending any navigation/selection key to the EPG.
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-details-panel')).evaluate().isNotEmpty,
        failMessage: 'EPG never finished loading (epg-details-panel did not appear)',
      );
    }

    // Regression test for a real bug: on real hardware, a physical press of
    // the Enter key that opens the EPG is never instantaneous — the KeyUp
    // arrives much later (by then, the `EpgOverlay` is not only mounted but
    // has already finished loading the grid, `_loading == false`).
    // `tester.sendKeyEvent` (used in the `openEpg` helper above) sends Down+Up
    // in the same frame, with no time for the overlay to even mount — so it
    // doesn't reproduce this race condition. This test sends the Down, waits
    // for the load to really finish (`epg-details-panel`, like `openEpg`
    // does) and only THEN sends the Up — reproducing the exact moment where
    // the EPG would "flash and close": `handleSelectKeyEvent` refuses to act
    // while `_loading` is true, so it only fails when the Up arrives after the
    // load.
    testWidgets('opening the EPG does not close it when the Enter KeyUp arrives after the overlay has loaded', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 3);

      await pumpUntil(
        tester,
        () => !osdVisible(tester),
        timeout: const Duration(seconds: 8),
        failMessage: 'OSD never hid on its own after the boot auto-show',
      );
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Enter (OSD hidden) did not show the OSD');

      await ensurePlayerFocus(tester);
      await tester.sendKeyDownEvent(LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-details-panel')).evaluate().isNotEmpty,
        failMessage: 'EPG never finished loading (epg-details-panel did not appear) before the KeyUp',
      );
      await tester.sendKeyUpEvent(LogicalKeyboardKey.enter);
      await tester.pump();

      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-overlay')).evaluate().isNotEmpty,
        failMessage: 'EPG closed by itself right after opening (orphan KeyUp treated as a tap on the overlay)',
      );
      // Wait a bit longer (real time, not just "exists in this frame") to
      // confirm it isn't just the opening animation not having been
      // interrupted yet — the overlay must really stay mounted.
      await tester.pump(const Duration(milliseconds: 500));
      expect(find.byKey(const Key('epg-overlay')), findsOneWidget, reason: 'EPG should still be open');
      expect(osdChannelNumberText(tester), '3');
    });

    // The channel whose row holds the focused (3px white border) EPG cell.
    String? focusedEpgChannelNumber(WidgetTester tester) {
      for (final entry in channelIds.entries) {
        final prefix = 'epg-cell-${entry.value}-';
        for (final element in find.byWidgetPredicate((w) => w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith(prefix)).evaluate()) {
          final decoration = (element.widget as Container).decoration as BoxDecoration;
          if ((decoration.border as Border).top.width == 3) return '${entry.key}';
        }
      }
      return null;
    }

    // Regression test for a real bug: Up moved DOWN and Down moved UP because
    // the EPG reused the channelNext/channelPrevious bindings. Also asserts
    // that remapping channelNext does not change the EPG's row navigation.
    testWidgets('opens the EPG and selects the live program of another channel', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setInt('key_binding_channelNext', LogicalKeyboardKey.keyK.keyId);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 3);

      await openEpg(tester);

      // The current channel (3) is row 0 (lowest-number channel). Down moves to
      // the channel 8 row, Up moves back, and the remapped channelNext key
      // (K) does nothing inside the EPG.
      expect(focusedEpgChannelNumber(tester), '3');
      await pressKey(tester, LogicalKeyboardKey.keyK);
      await tester.pump();
      expect(focusedEpgChannelNumber(tester), '3', reason: 'remapped channelNext must not move EPG rows');
      await pressKey(tester, LogicalKeyboardKey.arrowDown);
      await tester.pump();
      expect(focusedEpgChannelNumber(tester), '8', reason: 'Down must move to the next row');
      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump();
      expect(focusedEpgChannelNumber(tester), '3', reason: 'Up must move to the previous row');
      await pressKey(tester, LogicalKeyboardKey.arrowDown);
      await tester.pump();
      // Activate (short Enter tap) the channel 8 live cell — program A
      // (column index 0) is the "live now" one.
      await pressKey(tester, LogicalKeyboardKey.enter); // short tap: quick down+up
      await tester.pump();

      await waitForChannelNumber(tester, 8);
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-overlay')).evaluate().isEmpty,
        failMessage: 'epg-overlay stayed mounted after selecting channel 8',
      );
    });

    testWidgets('long-press on the future program expands details without switching channel', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 3);

      await openEpg(tester);

      // Initial focus: row of the current channel (3), column of the live
      // program (A, index 0). Move to the column of the future program (B,
      // index 1).
      await pressKey(tester, LogicalKeyboardKey.arrowRight); // moveColumn(1)
      await tester.pump();

      await ensurePlayerFocus(tester);
      await tester.sendKeyDownEvent(LogicalKeyboardKey.enter);
      await tester.pump(const Duration(milliseconds: 700));
      await tester.sendKeyUpEvent(LogicalKeyboardKey.enter);
      await tester.pump();

      expect(find.byKey(const Key('epg-details-panel-expanded')), findsOneWidget);
      // Channel did not change: still channel 3, and the EPG stays open.
      expect(find.byKey(const Key('epg-overlay')), findsOneWidget);
      expect(osdChannelNumberText(tester), '3');
    });

    testWidgets('Escape closes the EPG without switching channel', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 3);

      await openEpg(tester);

      await pressKey(tester, LogicalKeyboardKey.escape);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-overlay')).evaluate().isEmpty,
        failMessage: 'Esc did not close the EPG',
      );
      expect(osdChannelNumberText(tester), '3');
    });
  });
}
