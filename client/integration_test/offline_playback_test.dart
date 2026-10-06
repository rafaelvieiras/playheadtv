// E2E: the backend goes away in the middle of a channel. The client keeps
// playing from its local copy of the schedule (it only ever talks to the media
// server for video), keeps transitioning across several items, and the guide,
// channel switching and the mosaic all still work, read from the cache.
//
// The backend is a real Go process stopped for real (connection refused from
// then on), not a mock. One real app boot; everything after it is navigation,
// done only with the six baseline buttons (Up/Down/Left/Right/Select/Back =
// Enter, Escape).
import 'package:client/cache/offline_runtime.dart';
import 'package:client/cache/schedule_sync.dart';
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('backend stopped mid-channel (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;
    late String channelA;
    late String channelB;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
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

    int itemIndex(WidgetTester tester, String prefix) {
      final t = osdTitle(tester);
      if (t == null || !t.startsWith('$prefix ')) return -1;
      return int.parse(t.substring(prefix.length + 1));
    }

    testWidgets('keeps playing across items; guide, channel switching and the mosaic work from the cache', (tester) async {
      // Seeded right before the boot (the items start "now"): ten 20s
      // items each, so the cached schedule outlasts the whole test.
      channelA = await seedChannel(backend, number: 41, prefix: 'OFFA', items: 10, itemMs: 20000);
      channelB = await seedChannel(backend, number: 42, prefix: 'OFFB', items: 10, itemMs: 20000);
      await backend.waitForEpgEntries(channelA, 10);
      await backend.waitForEpgEntries(channelB, 10);
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
        cacheSyncInterval: const Duration(seconds: 2),
      );
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelA);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => itemIndex(tester, 'OFFA') >= 0, failMessage: 'never started playing channel A');

      // The cache is complete (both channels, every item) before the cut.
      final runtime = OfflineRuntime.current!;
      await pumpUntil(
        tester,
        () => runtime.cache.lastSyncAt != null && runtime.cache.allEntries(channelB).length >= 10,
        failMessage: 'the cache was never filled',
      );

      // --- the backend goes away ---
      final indexAtCut = itemIndex(tester, 'OFFA');
      await backend.stopServer();
      await expectLater(http.get(Uri.parse('${backend.baseUrl}/healthz')), throwsA(anything), reason: 'the backend must really be unreachable');
      await pumpUntil(
        tester,
        () => runtime.syncer.status.value.lastOutcome == SyncOutcome.offline,
        timeout: const Duration(seconds: 20),
        failMessage: 'the syncer never noticed the backend was gone',
      );

      // Playback carries on and transitions across several more items.
      await pumpUntil(
        tester,
        () => itemIndex(tester, 'OFFA') >= indexAtCut + 2,
        timeout: const Duration(seconds: 90),
        failMessage: 'did not keep transitioning offline (stuck on "${osdTitle(tester)}", started at $indexAtCut)',
      );

      // The guide is read from the cache: every channel has its cells.
      await openEpg(tester);
      expect(epgCellsOf(channelA), isNotEmpty);
      expect(epgCellsOf(channelB), isNotEmpty);
      await pressKey(tester, LogicalKeyboardKey.escape);
      await tester.pump();
      await pumpUntil(tester, () => find.byKey(const Key('epg-details-panel')).evaluate().isEmpty, failMessage: 'Back did not close the guide');

      // Channel switching (Up = next channel) loads the other channel from the cache.
      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump();
      await pumpUntil(
        tester,
        () => osdChannelNumber(tester) == '42' && itemIndex(tester, 'OFFB') >= 0,
        failMessage: 'could not switch to channel 42 offline (OSD: ${osdChannelNumber(tester)} / ${osdTitle(tester)})',
      );

      // Back goes to the mosaic, which lists the cached channels.
      await pressKey(tester, LogicalKeyboardKey.escape);
      await tester.pump();
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-page')).evaluate().isNotEmpty, failMessage: 'Back did not reach the mosaic');
      await pumpUntil(
        tester,
        () => find.byKey(Key('mosaic-tile-$channelA')).evaluate().isNotEmpty && find.byKey(Key('mosaic-tile-$channelB')).evaluate().isNotEmpty,
        failMessage: 'the mosaic did not list the cached channels',
      );

      // Select on the featured tile (the last watched channel) plays it again, still offline.
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('mosaic-page')).evaluate().isEmpty && osdChannelNumber(tester) == '42' && itemIndex(tester, 'OFFB') >= 0,
        failMessage: 'the mosaic could not start a channel offline',
      );
    });
  });
}
