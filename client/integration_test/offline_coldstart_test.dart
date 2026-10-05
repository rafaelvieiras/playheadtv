// E2E: a cold start while the backend is down. The first boot is online and
// fills the cache (schedule, channel list and poster art); the app is then torn
// down, the backend stopped, and a second boot — a new runtime opening the
// persisted SQLite file and art folder, as a new process would — plays from
// disk alone. Two real app boots (see AGENTS.md on keeping the count low).
import 'dart:io';

import 'package:client/cache/offline_runtime.dart';
import 'package:client/cache/schedule_sync.dart';
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('cold start with the backend down (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;
    late String channelId;
    late Directory cacheDir;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
      channelId = await seedChannel(
        backend,
        number: 51,
        prefix: 'COLD',
        items: 12,
        extraFields: {'image_primary_tag': 'cold-tag'},
      );
      await backend.waitForEpgEntries(channelId, 12);
      cacheDir = Directory.systemTemp.createTempSync('playheadtv_cold_cache_');
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
      try {
        cacheDir.deleteSync(recursive: true);
      } catch (_) {}
    });

    void configure() => AppConfig.overrideForTest(
          jellyfinUrl: video.baseUrl,
          jellyfinApiKey: 'fake-test-key',
          playheadBaseUrl: backend.baseUrl,
          playheadDeviceToken: deviceToken,
          cacheSyncInterval: const Duration(seconds: 2),
          cacheDir: cacheDir.path,
        );

    testWidgets('boot 1 online fills the cache, boot 2 with the backend down plays from the persisted copy', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      await prefs.setString('last_watched_channel_id', channelId);

      // --- boot 1: online ---
      configure();
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => (osdTitle(tester) ?? '').startsWith('COLD '), failMessage: 'never started playing online');
      var runtime = OfflineRuntime.current!;
      await pumpUntil(tester, () => runtime.cache.lastSyncAt != null, failMessage: 'first sync never finished');
      await runtime.syncer.artIdle;
      final firstSync = runtime.cache.lastSyncAt!;
      final entriesBefore = runtime.cache.allEntries(channelId).length;
      expect(entriesBefore, greaterThanOrEqualTo(12));
      // The poster of a program on air is on disk (fetched from the media server).
      final posterUrl = jellyfinImageUrl('COLD-0', 'cold-tag');
      expect(runtime.art.cached(posterUrl), isNotNull, reason: 'posters are prefetched; media server requests: ${video.imageRequests}');
      expect(video.imageRequests.where((u) => u.contains('/Items/COLD-0/Images/Primary')), isNotEmpty);

      // --- the app goes away and so does the backend ---
      await tester.pumpWidget(const SizedBox());
      await tester.pump(const Duration(milliseconds: 500));
      await backend.stopServer();

      // --- boot 2: cold start, backend down, same cache location ---
      configure(); // a new runtime: opens the SQLite file and art folder from disk
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(
        tester,
        () => (osdTitle(tester) ?? '').startsWith('COLD '),
        timeout: const Duration(seconds: 30),
        failMessage: 'a cold start with the backend down did not play from the persisted cache',
      );
      runtime = OfflineRuntime.current!;
      expect(runtime.cache.lastSyncAt, firstSync, reason: 'the schedule came from disk, not from a new sync');
      expect(runtime.cache.allEntries(channelId).length, entriesBefore);
      await pumpUntil(
        tester,
        () => runtime.syncer.status.value.lastOutcome == SyncOutcome.offline,
        failMessage: 'the syncer should report the backend as unreachable',
      );
      expect(runtime.cache.lastSyncAt, firstSync, reason: 'a failed sync leaves the cache alone');
      expect(runtime.art.cached(posterUrl), isNotNull, reason: 'the poster survives on disk');

      // The guide is served from the persisted cache too.
      await openEpg(tester);
      expect(epgCellsOf(channelId), isNotEmpty);
    });
  });
}
