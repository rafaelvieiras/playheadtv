// E2E: the exit flow. Back on the mosaic opens "Exit Playhead TV?" (Cancel has
// the focus); Back or Cancel closes it; Exit tears down (wake lock released,
// preview disposed) and only then terminates. From the player, Back keeps its
// chain (to the mosaic, no dialog): exiting is always one more Back from the
// mosaic. Only the six baseline buttons are used. The process exit is a fake
// (a real one would end the test run).
//
// Two real app boots.
import 'package:client/main.dart';
import 'package:client/services/app_exit.dart';
import 'package:client/services/screen_awake.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

/// One ordered log for both fakes, so the teardown order is observable.
final _log = <String>[];

class _FakeExit implements AppExit {
  @override
  Future<void> terminate() async => _log.add('terminate');
}

class _FakeAwake implements ScreenAwake {
  @override
  Future<void> enable() async => _log.add('wake lock taken');

  @override
  Future<void> disable() async => _log.add('wake lock released');
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('exit flow (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;
    late String channelId;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
      channelId = await seedChannel(backend, number: 71, prefix: 'EXA', items: 4, itemMs: 60000);
      await seedChannel(backend, number: 72, prefix: 'EXB', items: 4, itemMs: 60000);
      await backend.waitForEpgEntries(channelId, 4);
    });

    tearDownAll(() async {
      await (await SharedPreferences.getInstance()).clear();
      await backend.stop();
      await video.stop();
    });

    setUp(() {
      _log.clear();
      appExitOverride = _FakeExit();
      screenAwakeOverride = _FakeAwake();
      addTearDown(() {
        appExitOverride = const ProcessAppExit();
        screenAwakeOverride = const WakelockPlusScreenAwake();
      });
    });

    Future<void> press(WidgetTester tester, LogicalKeyboardKey key) async {
      await pressKey(tester, key);
      await tester.pump();
    }

    bool onScreen(String key) => find.byKey(Key(key)).evaluate().isNotEmpty;

    // The widget that owns the current primary focus is the button with [key].
    bool focusIsOn(String key) {
      final context = FocusManager.instance.primaryFocus?.context;
      return context?.findAncestorWidgetOfExactType<TextButton>()?.key == Key(key);
    }

    Future<void> boot(WidgetTester tester, {String? lastWatched}) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      if (lastWatched != null) await prefs.setString('last_watched_channel_id', lastWatched);
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
        cacheSyncInterval: const Duration(minutes: 5),
      );
      await tester.pumpWidget(const PlayheadSpikeApp());
    }

    testWidgets('Back on the mosaic asks first; Back and Cancel close it; Exit tears down, then terminates', (tester) async {
      await boot(tester);
      await pumpUntil(tester, () => onScreen('mosaic-page') && onScreen('mosaic-tile-settings'), failMessage: 'no mosaic on boot');
      expect(onScreen('exit-dialog'), isFalse);

      // Back opens the confirmation, with Cancel focused.
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => onScreen('exit-dialog'), failMessage: 'Back did not open the exit dialog');
      expect(find.text('Exit Playhead TV?'), findsOneWidget);
      expect(focusIsOn('exit-cancel'), isTrue, reason: 'Cancel is the default focus');

      // Back again closes it, and the app is still there.
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => !onScreen('exit-dialog'), failMessage: 'Back did not close the dialog');
      expect(_log, isEmpty);

      // Select on Cancel closes it too.
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => onScreen('exit-dialog'), failMessage: 'Back did not reopen the dialog');
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => !onScreen('exit-dialog'), failMessage: 'Select on Cancel did not close the dialog');
      expect(_log, isEmpty, reason: 'cancelling never exits');
      expect(onScreen('mosaic-page'), isTrue);

      // Right moves to Exit, Select confirms: wake lock released, then exit.
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => onScreen('exit-dialog'), failMessage: 'Back did not reopen the dialog');
      await press(tester, LogicalKeyboardKey.arrowRight);
      expect(focusIsOn('exit-confirm'), isTrue);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => _log.contains('terminate'), failMessage: 'Exit did not terminate: $_log');
      expect(_log, ['wake lock released', 'terminate']);
      expect(onScreen('exit-dialog'), isFalse);
    });

    testWidgets("the player's Back chain is unchanged: Back reaches the mosaic, one more Back asks to exit", (tester) async {
      await boot(tester, lastWatched: channelId);
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'never started playing');
      expect(_log, ['wake lock taken']);

      // Back from the player: the mosaic, with no dialog.
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => onScreen('mosaic-page'), failMessage: 'Back did not reach the mosaic');
      await tester.pump(const Duration(seconds: 1));
      expect(onScreen('exit-dialog'), isFalse, reason: 'the player never exits directly');
      expect(_log, isNot(contains('terminate')));

      // One more Back from the mosaic asks.
      await pumpUntil(tester, () => onScreen('mosaic-tile-settings'), failMessage: 'mosaic never loaded');
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => onScreen('exit-dialog'), failMessage: 'Back on the mosaic did not ask');
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => !onScreen('exit-dialog'), failMessage: 'Back did not close the dialog');
      expect(_log, isNot(contains('terminate')));
    });
  });
}
