// E2E: "Start in full screen" (Settings, desktop). Fresh start (nothing saved)
// asks the window for full screen before the first frame; turning it off in
// Settings applies at once, persists, and the next start stays windowed; turning
// it back on applies at once. Everything through the six baseline buttons. The
// window is a fake (the real one is not touched).
//
// Two real app boots.
import 'package:client/main.dart';
import 'package:client/services/app_window.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

class _RecordingWindow implements AppWindow {
  final calls = <bool>[];

  @override
  bool get supportsFullScreen => true;

  @override
  Future<void> setFullScreen(bool on) async => calls.add(on);
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('start in full screen (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
      await seedChannel(backend, number: 91, prefix: 'FSA', items: 4, itemMs: 60000);
      await seedChannel(backend, number: 92, prefix: 'FSB', items: 4, itemMs: 60000);
    });

    tearDownAll(() async {
      await (await SharedPreferences.getInstance()).clear(); // the real store: leave nothing behind
      await backend.stop();
      await video.stop();
    });

    Future<void> press(WidgetTester tester, LogicalKeyboardKey key) async {
      await pressKey(tester, key);
      await tester.pump();
    }

    String value(WidgetTester tester) => tester.widget<Text>(find.byKey(const Key('settings-fullscreen-value'))).data ?? '';

    // Boots the app on the mosaic (no history) and opens Settings with the six
    // buttons, down to the full screen row.
    Future<void> bootToFullScreenRow(WidgetTester tester) async {
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
        cacheSyncInterval: const Duration(minutes: 5),
      );
      await applyStartupFullScreen();
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-tile-settings')).evaluate().isNotEmpty, failMessage: 'no Settings tile in the mosaic');
      // Up goes from the featured video to the Settings tile (spatial
      // navigation), Select opens it.
      await press(tester, LogicalKeyboardKey.arrowUp);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty, failMessage: 'Settings did not open');
      await pumpUntil(tester, () => find.byKey(const Key('settings-fullscreen-tile')).evaluate().isNotEmpty, failMessage: 'no full screen row');
      // The page exists before it holds focus, and on a device the first presses
      // land in the gap and are swallowed (the row then still reads On after
      // Enter). Wait for focus to reach the page, and for it to reach the row.
      await pumpUntil(tester, () => focusIsInside(const Key('settings-page')), failMessage: 'Settings never took focus');
      // Walk until the row has focus instead of counting presses: where the page
      // starts its focus is not this test's business, and a device does not
      // answer the same count as the desktop.
      Future<void> walkToFullScreenRow() async {
        final row = const Key('settings-fullscreen-tile');
        for (final key in [LogicalKeyboardKey.arrowDown, LogicalKeyboardKey.arrowUp]) {
          for (var i = 0; i < 12 && !focusIsInside(row); i++) {
            await press(tester, key);
          }
          if (focusIsInside(row)) return;
        }
      }

      await walkToFullScreenRow();
      await pumpUntil(tester, () => focusIsInside(const Key('settings-fullscreen-tile')), failMessage: 'focus never reached the full screen row');
    }

    testWidgets('a fresh start is full screen; turning it off applies now and is saved', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      final window = _RecordingWindow();
      appWindowOverride = window;
      addTearDown(() => appWindowOverride = const NativeAppWindow());

      await bootToFullScreenRow(tester);
      expect(window.calls, [true], reason: 'nothing saved: full screen at startup, before the app shows anything');
      expect(value(tester), 'On');

      await press(tester, LogicalKeyboardKey.enter);
      expect(value(tester), 'Off');
      expect(window.calls, [true, false], reason: 'the toggle applies immediately');
      expect((await SharedPreferences.getInstance()).getBool('start_fullscreen'), isFalse);
    });

    testWidgets('the next start honours the saved choice (windowed); turning it on applies now', (tester) async {
      final window = _RecordingWindow();
      appWindowOverride = window;
      addTearDown(() => appWindowOverride = const NativeAppWindow());
      expect((await SharedPreferences.getInstance()).getBool('start_fullscreen'), isFalse, reason: 'left by the previous test');

      await bootToFullScreenRow(tester);
      expect(window.calls, [false], reason: 'a saved Off keeps the next start windowed');
      expect(value(tester), 'Off');

      await press(tester, LogicalKeyboardKey.enter);
      expect(value(tester), 'On');
      expect(window.calls, [false, true]);
      expect((await SharedPreferences.getInstance()).getBool('start_fullscreen'), isTrue);
    });
  });
}
