// E2E suite for the mosaic screen — same pattern as
// channel_navigation_test.dart (read that file and
// support/backend_fixture.dart/support/video_fixture.dart first): boots the
// real app against a real Go backend (BackendFixture) and a fake video server
// (VideoFixture, playing the role of "Jellyfin"), drives keyboard navigation
// through the real `integration_test` binding (never OS-level input tools)
// and never uses `tester.pumpAndSettle()` — while the OSD is visible, the
// `_OsdOverlay` ticker (main.dart) calls `setState` every 500ms forever, so
// pumpAndSettle would never settle. Every wait-for-condition uses manual
// polling (`pumpUntil`), same as the reference file.
//
// SharedPreferences isolation: in `integration_test -d linux` the app really
// runs on the desktop and `shared_preferences` writes to a real file of the
// user — it's not mockable as in a pure unit test. That's why each test in
// this suite explicitly clears the preferences before running (see `setUp`
// below), and the test that simulates "history already exists" writes
// directly to the same key `SettingsService` uses internally
// (`last_watched_channel_id`), avoiding duplicating the service's API here.
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

  group('mosaic screen (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    // Only 2 channels: a lower number (2) and a higher one (7). With no
    // history, the featured tile deterministically resolves to the one with
    // the lowest number (2) and the only remaining channel (7) becomes the
    // only surrounding tile — the geometry of the logical grid can be
    // predicted exactly, without depending on randomness.
    const channelNumbers = [2, 7];
    late Map<int, String> channelIds;
    late Map<int, String> channelNames;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();

      channelIds = {};
      channelNames = {
        for (final n in channelNumbers) n: 'Mosaic Channel $n',
      };

      for (final number in channelNumbers) {
        final channelId = await backend.createChannel(number: number, name: channelNames[number]!);
        channelIds[number] = channelId;
        final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Mosaic source $number');
        final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
          {
            'external_id': 'mosaic-$number-a',
            'kind': 'episode',
            'title': 'MOSAIC PROGRAM $number A',
            'duration_ms': 1800000,
          },
          {
            'external_id': 'mosaic-$number-b',
            'kind': 'other',
            'title': 'MOSAIC COMMERCIAL $number',
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
      // This app persists nothing beyond what SettingsService stores — a
      // blanket clear() is safe and simpler than removing specific keys.
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
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

    testWidgets('with no history, boot shows the mosaic', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(
        tester,
        () => find.byKey(const Key('mosaic-page')).evaluate().isNotEmpty,
        failMessage: 'mosaic did not appear on boot with no history',
      );
    });

    testWidgets('with history, boot jumps straight to the player without showing the mosaic', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelIds[2]!);

      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 2);

      expect(find.byKey(const Key('mosaic-page')), findsNothing);
    });

    testWidgets('selecting a tile in the mosaic enters the right channel', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());

      // Wait for the featured tile (channel 2, the lowest number — with no
      // history it's the deterministic fallback) to actually be mounted, not
      // just the page itself (which already exists with that key since the
      // loading/spinner state).
      final featuredTileKey = Key('mosaic-tile-${channelIds[2]}');
      await pumpUntil(
        tester,
        () => find.byKey(featuredTileKey).evaluate().isNotEmpty,
        failMessage: 'featured tile (channel 2) did not appear in the mosaic',
      );

      // Move down from the featured tile (row 0) to the only surrounding tile
      // (channel 7, row 1) and activate with Enter.
      await pressKey(tester, LogicalKeyboardKey.arrowDown);
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();

      await waitForChannelNumber(tester, 7);
      // `pushReplacement` animates the old route out — without
      // `pumpAndSettle` (forbidden in this suite, see the comment at the top
      // of the file), the mosaic can stay mounted (offstage) for a few frames
      // even with the new channel's OSD already visible. Wait for the
      // transition to really finish before confirming its absence.
      await pumpUntil(
        tester,
        () => find.byKey(const Key('mosaic-page')).evaluate().isEmpty,
        failMessage: 'mosaic stayed mounted after switching to channel 7',
      );
    });
  });
}
