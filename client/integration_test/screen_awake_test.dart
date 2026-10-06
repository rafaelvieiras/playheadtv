// E2E: the player holds the screen awake while a channel plays and releases it
// on leaving for the mosaic. Uses a fake platform (independent of OS state).
// One real app boot.
import 'package:client/main.dart';
import 'package:client/services/screen_awake.dart';
import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

class _Recorder implements ScreenAwake {
  final calls = <String>[];

  @override
  Future<void> enable() async => calls.add('enable');

  @override
  Future<void> disable() async => calls.add('disable');
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('screen awake (real e2e)', () {
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

    testWidgets('held while playing, released on Back to the mosaic', (tester) async {
      final recorder = _Recorder();
      screenAwakeOverride = recorder;
      addTearDown(() => screenAwakeOverride = const WakelockPlusScreenAwake());

      final channel = await seedChannel(backend, number: 61, prefix: 'AWK', items: 4, itemMs: 20000);
      await backend.waitForEpgEntries(channel, 4);
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
      );
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channel);
      await tester.pumpWidget(const PlayheadSpikeApp());

      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'never started playing');
      expect(recorder.calls, ['enable']);

      await pressKey(tester, LogicalKeyboardKey.escape);
      await tester.pump();
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-page')).evaluate().isNotEmpty, failMessage: 'Back did not reach the mosaic');
      // The replaced player route is disposed once its exit transition ends.
      await pumpUntil(tester, () => find.byType(ChannelPlayerPage).evaluate().isEmpty, failMessage: 'the player page was never disposed');
      expect(recorder.calls, ['enable', 'disable']);
    });
  });
}
