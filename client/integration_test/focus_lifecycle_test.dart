// E2E: the player page gets keyboard focus back after the window regains OS
// focus. When the window loses focus Flutter drops primary focus to the root
// scope; without the page's lifecycle observer the next key (Select) would be
// lost. One real app boot.
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('player focus across window focus changes (real e2e)', () {
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
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-focus');
    });

    testWidgets('Select still shows the OSD after a focus loss and regain', (tester) async {
      await seedChannel(backend, number: 61, prefix: 'FOCUS', items: 6, itemMs: 30000);
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: await backend.createDeviceToken(),
      );
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'channel never tuned');
      await pumpUntil(tester, () => !osdVisible(tester), timeout: const Duration(seconds: 10), failMessage: 'OSD never auto-hid');

      final playerNode = tester.widget<KeyboardListener>(find.byType(KeyboardListener).first).focusNode;
      expect(playerNode.hasPrimaryFocus, isTrue, reason: 'precondition: the player holds focus');

      // The window loses and regains OS focus. Flutter itself restores the
      // suspended node only when nothing else asked for focus in between, so
      // model the case where it did not: after the round trip primary focus
      // sits on the root scope, and the app reports `resumed` again.
      final binding = tester.binding;
      binding.handleAppLifecycleStateChanged(AppLifecycleState.inactive);
      await tester.pump();
      binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
      await tester.pump();
      FocusManager.instance.primaryFocus?.unfocus(disposition: UnfocusDisposition.scope);
      await tester.pump();
      expect(playerNode.hasPrimaryFocus, isFalse, reason: 'precondition: focus was dropped to the scope');

      binding.handleAppLifecycleStateChanged(AppLifecycleState.resumed);
      await tester.pump();
      expect(playerNode.hasPrimaryFocus, isTrue, reason: 'the player did not take focus back on resume');

      // Raw key, no helper that repairs focus: the page must receive it.
      await tester.sendKeyEvent(LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Select did not show the OSD after the focus round trip');
    });
  });
}
