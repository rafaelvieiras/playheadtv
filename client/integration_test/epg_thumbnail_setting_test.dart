// E2E suite for the "Show channel thumbnail in the guide" setting; same harness as epg_overlay_test.dart.
// (Original header of that suite follows.)
// E2E suite for the full-screen EPG — same pattern as mosaic_test.dart/
// channel_navigation_test.dart (read those two first): boots the real app
// against a real Go backend (BackendFixture) and a fake video server
// (VideoFixture, playing the role of "Jellyfin"), drives keyboard navigation
// through the real `integration_test` binding, and never uses
// `tester.pumpAndSettle()` (the OSD's 500ms ticker would never settle) — every
// wait-for-condition uses a manual `pumpUntil`.
import 'package:client/epg_thumbnail.dart';
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

  group('guide thumbnail setting (real e2e)', () {
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

    // Where the guide's details panel starts: below the thumbnail when there is one, at the
    // top of the screen when there is none.
    double detailsTop(WidgetTester tester) => tester.getTopLeft(find.byKey(const Key('epg-details-panel'))).dy;

    testWidgets('on (the default) the guide keeps the preview in the panel; off, no slot at all', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 3);
      await openEpg(tester);
      // The preview is a slot inside the details panel, so the panel starts at
      // the top of the screen either way; what the setting changes is whether
      // the guide reserves a window for the picture.
      expect(detailsTop(tester), lessThan(100), reason: 'the panel is the first thing on screen');
      final withPreview = tester.getSize(find.byKey(const Key('epg-details-panel'))).height;
      expect(find.byKey(const Key('epg-details-preview')), findsOneWidget);
      await pressKey(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('epg-overlay')).evaluate().isEmpty, failMessage: 'Escape did not close the guide');

      // Turn it off the way Settings does, and open the guide again.
      EpgThumbnailSetting.enabled.value = false;
      await openEpg(tester);
      expect(find.byKey(const Key('epg-details-preview')), findsNothing, reason: 'no slot, so no hole either');
      // The panel's height is set by its own words (logo, title, chips,
      // synopsis), which are taller than the 16:9 slot: dropping the slot cannot
      // shrink the panel, and that is the point — the setting never makes the
      // guide jump.
      expect(tester.getSize(find.byKey(const Key('epg-details-panel'))).height, lessThanOrEqualTo(withPreview));
      // Still fully navigable.
      await pressKey(tester, LogicalKeyboardKey.arrowDown);
      await tester.pump(const Duration(milliseconds: 300));
      await pressKey(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('epg-overlay')).evaluate().isEmpty, failMessage: 'Escape did not close the guide');
      EpgThumbnailSetting.enabled.value = true;
    });

    testWidgets('a saved Off is read at boot', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setBool('epg_thumbnail', false);
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-epg');
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 3);
      expect(EpgThumbnailSetting.enabled.value, isFalse);
      await openEpg(tester);
      expect(detailsTop(tester), lessThan(100));
      expect(find.byKey(const Key('epg-details-preview')), findsNothing);
      EpgThumbnailSetting.enabled.value = true;
    });
  });
}
