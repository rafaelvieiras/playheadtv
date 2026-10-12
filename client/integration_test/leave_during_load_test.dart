// E2E: leaving the player while a channel is still opening leaves nothing
// behind. The controller whose open finishes after the player page is gone is
// silenced and disposed (it is never attached to anything), and entering the
// channel again plays it.
//
// A slow-opening source stands in for a cold library file. One real app boot.
import 'package:client/main.dart';
import 'package:client/util/controller_lifecycle.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('leaving the player during a slow load (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;
    late String orphanChannel;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
      orphanChannel = await seedChannel(backend, number: 81, prefix: 'ORPH', items: 3, itemMs: 60000, startOffset: const Duration(seconds: -1));
      await seedChannel(backend, number: 82, prefix: 'OTHER', items: 3, itemMs: 60000, startOffset: const Duration(seconds: -1));
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
    });

    bool spinnerShown() => find.byKey(const Key('loading-indicator')).evaluate().isNotEmpty;
    bool mosaicShown() => find.byKey(const Key('mosaic-page')).evaluate().isNotEmpty;

    testWidgets('a controller that opens after the player is gone is disposed, and the channel plays on re-entry', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', orphanChannel);
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
      );
      video.slowOpen('ORPH', const Duration(seconds: 8));
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, spinnerShown, failMessage: 'the slow channel never showed the spinner');

      // The one controller the player created and is still opening.
      final inFlight = liveControllers.toList();
      expect(inFlight, hasLength(1), reason: 'the player holds exactly the controller being opened');

      // Back with the OSD hidden leaves for the mosaic while the open is pending.
      await pressKey(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, mosaicShown, failMessage: 'Back did not reach the mosaic');
      await pumpUntil(tester, () => !spinnerShown(), failMessage: 'the player page stayed mounted after Back');

      // The slow open finishes after the page is gone.
      await tester.pump(const Duration(seconds: 10));
      video.slowOpen('ORPH', Duration.zero);
      await pumpUntil(
        tester,
        () => !liveControllers.contains(inFlight.single),
        timeout: const Duration(seconds: 30),
        failMessage: 'the controller opened after leaving the player was never disposed',
      );
      expect(inFlight.single.value.isPlaying, isFalse, reason: 'an orphan controller must not keep playing');

      // Re-entering the channel (the featured tile) plays it.
      await pressKey(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => osdTitle(tester) == 'ORPH 0', timeout: const Duration(seconds: 30), failMessage: 'the channel did not play on re-entry');
      expect(find.byKey(const Key('error-banner')), findsNothing);
    });
  });
}
