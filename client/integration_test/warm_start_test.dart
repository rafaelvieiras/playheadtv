// E2E: a warm start plays from the on-disk cache without waiting for the network.
// Boot 1 fills the cache against a healthy backend. Boot 2 is a new runtime over the
// same cache with a backend that answers every request only after several seconds:
// the channel must be on air long before that, and the late answer must not
// disturb it. Two real app boots (see AGENTS.md on keeping the count low).
import 'dart:io';

import 'package:client/cache/offline_runtime.dart';
import 'package:client/main.dart';
import 'package:client/services/perf_trace.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/request_proxy.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('warm start (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late RequestProxy proxy;
    late String deviceToken;
    late String channelId;
    late Directory cacheDir;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      proxy = RequestProxy(backendBaseUrl: backend.baseUrl);
      await proxy.start();
      deviceToken = await backend.createDeviceToken();
      channelId = await seedChannel(backend, number: 61, prefix: 'WARM', items: 12);
      await backend.waitForEpgEntries(channelId, 12);
      cacheDir = Directory.systemTemp.createTempSync('playheadtv_warm_cache_');
    });

    tearDownAll(() async {
      await proxy.stop();
      await backend.stop();
      await video.stop();
      try {
        cacheDir.deleteSync(recursive: true);
      } catch (_) {}
    });

    void configure() => AppConfig.overrideForTest(
          jellyfinUrl: video.baseUrl,
          jellyfinApiKey: 'fake-test-key',
          playheadBaseUrl: proxy.baseUrl,
          playheadDeviceToken: deviceToken,
          cacheSyncInterval: const Duration(seconds: 2),
          cacheDir: cacheDir.path,
        );

    testWidgets('boot 2 tunes from the cache while the backend takes seconds to answer', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      await prefs.setString('last_watched_channel_id', channelId);

      // --- boot 1: online, a cold cache. Nothing is cached, so it waits for the sync. ---
      BootTrace.reset();
      configure();
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => (osdTitle(tester) ?? '').startsWith('WARM '), failMessage: 'never started playing online');
      expect(BootTrace.marks.map((m) => m.$1), contains('sync_done'), reason: 'a cold cache waits for the first sync');
      expect(BootTrace.marks.map((m) => m.$1), isNot(contains('warm_start')));
      var runtime = OfflineRuntime.current!;
      await pumpUntil(tester, () => runtime.cache.lastSyncAt != null, failMessage: 'first sync never finished');
      await runtime.syncer.artIdle;

      // --- the app goes away; the backend now answers slowly ---
      await tester.pumpWidget(const SizedBox());
      await tester.pump(const Duration(milliseconds: 500));
      proxy.delay = const Duration(seconds: 8);
      proxy.clear();

      // --- boot 2: warm cache, slow backend ---
      BootTrace.reset();
      configure();
      final clock = Stopwatch()..start();
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(
        tester,
        () => (osdTitle(tester) ?? '').startsWith('WARM '),
        timeout: const Duration(seconds: 7),
        failMessage: 'a warm start waited for the slow backend instead of playing from the cache',
      );
      final tunedAfter = clock.elapsedMilliseconds;
      expect(tunedAfter, lessThan(7000), reason: 'the backend needs 8s per request; the picture must not');
      expect(BootTrace.marks.map((m) => m.$1), contains('warm_start'));
      expect(BootTrace.marks.map((m) => m.$1), isNot(contains('sync_done')), reason: 'no wait for the sync on a warm start');
      runtime = OfflineRuntime.current!;
      expect(runtime.cache.allEntries(channelId), isNotEmpty);

      // The refresh still happens in the background and does not disturb what plays.
      proxy.delay = null;
      await pumpUntil(
        tester,
        () => proxy.exact('/epg').isNotEmpty || proxy.to('/epg/meta').isNotEmpty,
        timeout: const Duration(seconds: 20),
        failMessage: 'the background refresh never reached the backend',
      );
      expect((osdTitle(tester) ?? '').startsWith('WARM '), isTrue, reason: 'the channel stays on air');
    });
  });
}
