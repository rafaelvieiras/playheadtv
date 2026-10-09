// E2E: a late or failing preload never leaves the channel stopped. The next
// item is prepared a configurable lead before the current one ends; when its
// source opens slowly (a network- or cloud-backed library) or a first attempt
// fails, the player keeps what is on air, retries with a back-off and crosses
// over once the preload is ready. It used to report "preload of ... did not
// finish in time" and stop.
//
// Timings are shrunk through AppConfig.overrideForTest (a 2s lead, a 200ms
// retry base); nothing waits real minutes. Two real app boots.
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

  group('late and failing preloads (real e2e)', () {
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

    bool errorBannerShown() => find.byKey(const Key('error-banner')).evaluate().isNotEmpty;

    // Boots the app on a fresh channel of 20s items and plays until the first
    // item is on the OSD. The 20s item leaves room for a preload that takes
    // several seconds without the schedule running past the second item.
    Future<void> bootOn(WidgetTester tester, int number, String prefix) async {
      final channelId = await seedChannel(backend, number: number, prefix: prefix, items: 4, itemMs: 20000, startOffset: const Duration(seconds: -1));
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelId); // the earlier test's channel exists too
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
        preloadLead: const Duration(seconds: 2),
        preloadRetryBase: const Duration(milliseconds: 200),
      );
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) == '$prefix 0', failMessage: 'never landed on $prefix 0');
    }

    // Pumps until the OSD names [title], failing if the error banner ever
    // shows (the channel must not stop) before then.
    Future<void> expectCrossover(WidgetTester tester, String title) async {
      var sawError = false;
      await pumpUntil(
        tester,
        () {
          if (errorBannerShown()) sawError = true;
          return osdTitle(tester) == title;
        },
        timeout: const Duration(seconds: 60),
        failMessage: 'never crossed over to $title (error banner shown: $sawError)',
      );
      expect(sawError, isFalse, reason: 'the error banner showed while waiting for the preload');
    }

    testWidgets('a source that opens slower than the lead is waited for, not an error', (tester) async {
      video.slowOpen('LATE-1', const Duration(seconds: 3));
      await bootOn(tester, 91, 'LATE');
      await expectCrossover(tester, 'LATE 1');
      expect(video.videoRequests.where((p) => p.contains('LATE-1')), isNotEmpty);
      video.slowOpen('LATE-1', Duration.zero);
    });

    testWidgets('a preload that fails once is retried and the channel carries on', (tester) async {
      video.failNext('FLAKY-1', 2);
      await bootOn(tester, 92, 'FLAKY');
      await expectCrossover(tester, 'FLAKY 1');
      expect(
        video.videoRequests.where((p) => p.contains('FLAKY-1')).length,
        greaterThan(2),
        reason: 'the refused requests were followed by a retry that succeeded',
      );
    });
  });
}
