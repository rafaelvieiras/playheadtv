// E2E: the cache runs out (the backend is away for longer than the schedule it
// handed over). The client's OWN setting decides what the channel does:
//
//  - loop the last known programming (the default), or
//  - hold the local slate,
//
// and when the backend returns with real programming the client leaves the
// fallback for it, like after an admin grid reset. The short schedules here
// (three 6s items) stand for "the window ran out"; nothing in the client
// depends on its length. Two real app boots.
import 'package:client/cache/offline_runtime.dart';
import 'package:client/cache/schedule_sync.dart';
import 'package:client/main.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:video_player/video_player.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('cache exhausted (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;

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

    Future<OfflineRuntime> bootOnChannel(WidgetTester tester, String channelId, {String? behavior}) async {
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
        cacheSyncInterval: const Duration(seconds: 2),
      );
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelId);
      if (behavior != null) await prefs.setString('cache_exhausted_behavior', behavior);
      await tester.pumpWidget(const PlayheadSpikeApp());
      return OfflineRuntime.ensure();
    }

    bool slateOnScreen(WidgetTester tester) => find
        .byType(VideoPlayer)
        .evaluate()
        .map((e) => (e.widget as VideoPlayer).controller)
        .any((c) => c.dataSource.contains('gap_slate'));

    testWidgets('default: loops the last known programming', (tester) async {
      final channelId = await seedChannel(backend, number: 71, prefix: 'LOOP', items: 3, itemMs: 10000);
      await backend.waitForEpgEntries(channelId, 3);
      final runtime = await bootOnChannel(tester, channelId);
      await pumpUntil(tester, () => (osdTitle(tester) ?? '').startsWith('LOOP '), failMessage: 'never started playing');
      await pumpUntil(tester, () => runtime.cache.allEntries(channelId).length >= 3, failMessage: 'the cache was never filled');

      await backend.stopServer();
      await pumpUntil(tester, () => runtime.syncer.status.value.lastOutcome == SyncOutcome.offline, failMessage: 'outage not noticed');

      // The three items end within about 18s; after the last one the channel
      // replays the programming it knew, starting again from LOOP 0.
      final seen = <String>[];
      await pumpUntil(
        tester,
        () {
          final t = osdTitle(tester);
          if (t != null && (seen.isEmpty || seen.last != t)) seen.add(t);
          final lastTwo = seen.lastIndexOf('LOOP 2');
          return lastTwo >= 0 && seen.skip(lastTwo + 1).contains('LOOP 0');
        },
        timeout: const Duration(seconds: 60),
        failMessage: 'the channel did not loop its last known programming (titles seen: $seen)',
      );
      expect(slateOnScreen(tester), isFalse);
    });

    testWidgets('slate setting: holds the slate, then leaves it for real programming when the backend returns', (tester) async {
      // The previous test left the backend stopped.
      if (!backend.isRunning) await backend.restartServer();
      final channelId = await seedChannel(backend, number: 72, prefix: 'SLATE', items: 3, itemMs: 10000);
      await backend.waitForEpgEntries(channelId, 3);
      final runtime = await bootOnChannel(tester, channelId, behavior: 'slate');
      await pumpUntil(tester, () => (osdTitle(tester) ?? '').startsWith('SLATE '), failMessage: 'never started playing');
      await pumpUntil(tester, () => runtime.cache.allEntries(channelId).length >= 3, failMessage: 'the cache was never filled');

      await backend.stopServer();
      await pumpUntil(tester, () => runtime.syncer.status.value.lastOutcome == SyncOutcome.offline, failMessage: 'outage not noticed');

      await pumpUntil(
        tester,
        () => slateOnScreen(tester),
        timeout: const Duration(seconds: 60),
        failMessage: 'the slate was never held once the schedule ran out (OSD: ${osdTitle(tester)})',
      );
      // It stays on the slate: nothing new plays while there is no schedule.
      await tester.pump(const Duration(seconds: 3));
      expect(slateOnScreen(tester), isTrue);

      // The backend returns with new programming on air: the client switches to it.
      await backend.restartServer();
      final source = await backend.createLocalFolderMediaSource(name: 'Slate return source');
      final ids = await backend.syncCatalogItems(source, [
        {'external_id': 'slate-back', 'kind': 'movie', 'title': 'BACK ON AIR', 'duration_ms': 600000},
      ]);
      final block = await backend.createBlockWithItems(ids);
      await backend.createScheduleOnce(channelId: channelId, blockId: block, startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 1)));
      await backend.waitForEpgEntries(channelId, 1);
      await pumpUntil(
        tester,
        () => osdTitle(tester) == 'BACK ON AIR',
        timeout: const Duration(seconds: 40),
        failMessage: 'the client never left the slate for the new programming (OSD: ${osdTitle(tester)})',
      );
      expect(slateOnScreen(tester), isFalse);
    });
  });
}
