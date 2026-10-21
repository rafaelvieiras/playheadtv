// E2E: the app leaves the screen (Android Home, a minimized window) and must
// stop playing: no audio, no decoders, no preload, no timers. Coming back
// resumes the channel. One real app boot.
//
// The signal that nothing plays or preloads is the media server's request log
// (VideoFixture.videoRequests): a player that keeps running keeps opening
// items, a suspended one asks for nothing until it is visible again.
import 'package:client/main.dart';
import 'package:client/util/controller_lifecycle.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('app in the background (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
    });

    testWidgets('hidden stops playback and preload, resumed plays again; inactive does not stop', (tester) async {
      final channel = await seedChannel(backend, number: 71, prefix: 'BGND', items: 12, itemMs: 6000);
      await backend.waitForEpgEntries(channel, 12);
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: await backend.createDeviceToken(),
      );
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channel);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'channel never tuned');

      final binding = tester.binding;

      // Losing focus with the window still visible must not stop anything.
      final beforeInactive = video.videoRequests.length;
      binding.handleAppLifecycleStateChanged(AppLifecycleState.inactive);
      await tester.pump(const Duration(milliseconds: 500));
      expect(osdTitle(tester), isNotNull, reason: 'inactive (focus lost, window visible) must keep the channel on air');
      expect(video.videoRequests.length, beforeInactive, reason: 'inactive must not reload the channel');
      binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
      await tester.pump();

      // Hidden. Like a real hidden app, the binding stops producing frames
      // (framesEnabled = false), so `tester.pump` renders nothing and cannot
      // be used to wait: the wait below is wall-clock only. The teardown is
      // pure Dart (timers, controllers), so it runs without frames.
      binding.handleAppLifecycleStateChanged(AppLifecycleState.hidden);
      expect(binding.framesEnabled, isFalse, reason: 'precondition: a hidden app draws no frames');

      // Let anything already in flight settle, then watch the server: a
      // suspended player requests nothing, not even a preload of the next item
      // (items are 6 s long, so a player that kept running would have to load
      // the next ones inside this window).
      await Future<void>.delayed(const Duration(seconds: 3));
      final settled = video.videoRequests.length;
      await Future<void>.delayed(const Duration(seconds: 12));
      expect(video.videoRequests.length, settled, reason: 'the media server was still being asked for video while the app was hidden');

      // Visible again: frames come back, the channel is reloaded and plays.
      binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
      expect(binding.framesEnabled, isTrue);
      // The stale OSD from before the hide is still in the tree until the page
      // rebuilds, so the proof of a real reload is the new video request.
      await pumpUntil(tester, () => video.videoRequests.length > settled,
          failMessage: 'resume did not open the item again (no new video request)');
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'the channel did not come back on resume');
    });

    testWidgets('the mosaic preview stops when hidden and comes back when resumed', (tester) async {
      final channel = await seedChannel(backend, number: 72, prefix: 'BGMO', items: 6, itemMs: 600000);
      await backend.waitForEpgEntries(channel, 6);
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: await backend.createDeviceToken(),
      );
      // No history: the app boots on the mosaic, whose featured tile plays a
      // preview with audio.
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-page')).evaluate().isNotEmpty, failMessage: 'the mosaic never showed');
      // The proof is the set of live playbacks, not the server log: the preview
      // buffers a long clip ahead and then goes quiet on the wire whether or
      // not it is still playing (and audible).
      await pumpUntil(tester, () => liveControllers.isNotEmpty, failMessage: 'the mosaic preview never opened');
      expect(liveControllers.length, 1, reason: 'the mosaic holds exactly its preview');

      final binding = tester.binding;
      binding.handleAppLifecycleStateChanged(AppLifecycleState.hidden);
      expect(binding.framesEnabled, isFalse);
      // The dispose goes through the GL queue and waits for a frame boundary;
      // wall-clock wait, since a hidden app draws nothing.
      final deadline = DateTime.now().add(const Duration(seconds: 15));
      while (liveControllers.isNotEmpty && DateTime.now().isBefore(deadline)) {
        await Future<void>.delayed(const Duration(milliseconds: 200));
      }
      expect(liveControllers, isEmpty, reason: 'the preview kept playing (audio, decoder) while the app was hidden');

      binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
      await pumpUntil(tester, () => liveControllers.length == 1, failMessage: 'the preview did not start again on resume');
    });
  });
}
