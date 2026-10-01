// E2E coverage for issue #9 (honor EPG start/end times instead of playing
// entries back to back): same pattern as channel_navigation_test.dart/
// mosaic_test.dart (read those first) — boots the real app against a real Go
// backend (BackendFixture) and a fake video server (VideoFixture), and never
// uses `tester.pumpAndSettle()` (the OSD's 500ms ticker would never settle),
// every wait-for-condition uses a manual `pumpUntil`.
//
// Builds a channel with two `once` ScheduleItems and NO filler/fallback rule
// configured, so the materializer leaves a real, un-filled interior gap
// between the first item's nominal content end and the second item's
// start_at (see `fillFallbackGap` in `backend/internal/materializer/
// expand.go`) — exactly the "interior hole in the materialized window" issue
// #9 describes. This is real wall-clock time, not simulated: the gap is kept
// short (a few seconds) so the suite stays fast, but still well above
// `epgSyncTolerance` (2s, `client/lib/main.dart`).
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('EPG gap handling (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    const channelNumber = 4;
    // Item A started 3s before the backend fixtures are created below runs
    // (same "- 3s" convention as the other suites, guarding against the
    // small amount of real time that elapses during async setUpAll before
    // the app actually boots and fetches GET /epg) and is 8s long, so ~5s of
    // real playback remain once the app boots — just above
    // minTransitionInterval (5s) so it isn't floored. Item B starts 8s after
    // item A's nominal content end (16s after item A's own start_at),
    // leaving an un-filled 8s gap for the client to honor.
    final itemAStart = DateTime.now().toUtc().subtract(const Duration(seconds: 3));
    const itemADurationMs = 8000;
    final itemBStart = itemAStart.add(const Duration(milliseconds: itemADurationMs)).add(const Duration(seconds: 8));

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();

      final channelId = await backend.createChannel(number: channelNumber, name: 'Channel $channelNumber');
      final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Channel $channelNumber source');
      final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
        {'external_id': 'gap-a', 'kind': 'episode', 'title': 'PROGRAM A', 'duration_ms': itemADurationMs},
        {'external_id': 'gap-b', 'kind': 'episode', 'title': 'PROGRAM B', 'duration_ms': 5000},
      ]);
      final blockA = await backend.createBlockWithItems([catalogItemIds[0]]);
      final blockB = await backend.createBlockWithItems([catalogItemIds[1]]);
      await backend.createScheduleWithItems(
        channelId: channelId,
        items: [(blockId: blockA, startAt: itemAStart), (blockId: blockB, startAt: itemBStart)],
      );

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
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-gap');
    });

    Future<void> pumpUntil(
      WidgetTester tester,
      bool Function() condition, {
      Duration timeout = const Duration(seconds: 20),
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

    String? itemTitleText(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-item-title'));
      if (finder.evaluate().isEmpty) return null;
      return tester.widget<Text>(finder).data;
    }

    testWidgets('holds on the slate through an interior EPG gap instead of starting the next item early', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());

      await pumpUntil(
        tester,
        () => itemTitleText(tester) == 'PROGRAM A',
        failMessage: 'never landed on PROGRAM A at boot',
      );

      // Item A has ~5s left at boot; the gap after it is 8s wide. 7s in,
      // still well inside the gap (A's content ended ~2s ago at most) — item
      // B must NOT be showing yet, proving the client waited instead of
      // cutting straight to it.
      await Future<void>.delayed(const Duration(seconds: 7));
      await tester.pump();
      expect(
        itemTitleText(tester),
        'PROGRAM A',
        reason: 'client advanced to the next item before its scheduled start_at — the EPG gap was not honored',
      );

      // The gap closes ~6s after this point (5s remaining of A + 8s gap - 7s
      // already waited above = 6s) — generous timeout past that for the
      // slate->item crossfade to finish and the resumed schedule's own OSD
      // call to land.
      await pumpUntil(
        tester,
        () => itemTitleText(tester) == 'PROGRAM B',
        timeout: const Duration(seconds: 15),
        failMessage: 'never advanced to PROGRAM B after the EPG gap elapsed',
      );
    });
  });
}
