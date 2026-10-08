// E2E: Settings -> Offline cache is reachable and changeable with ONLY the six
// baseline buttons (Up/Down/Left/Right/Select/Back = Enter, Escape): no tap, no
// pointer, no remapping. This is the non-accelerator path the 6-button rule
// asks to be exercised: mosaic -> Settings tile -> Offline cache -> change the
// "when the schedule runs out" setting and the window limit, sync, clear the
// cache behind its confirmation, and Back out to the mosaic.
//
// One real app boot.
import 'package:client/cache/offline_runtime.dart';
import 'package:client/main.dart';
import 'package:client/services/settings_service.dart';
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

  group('Settings -> Offline cache with six buttons (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
      await seedChannel(backend, number: 81, prefix: 'SETA', items: 4, itemMs: 60000);
      await seedChannel(backend, number: 82, prefix: 'SETB', items: 4, itemMs: 60000);
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    Future<void> press(WidgetTester tester, LogicalKeyboardKey key) async {
      await pressKey(tester, key);
      await tester.pump();
    }

    String text(WidgetTester tester, String key) => tester.widget<Text>(find.byKey(Key(key))).data ?? '';

    testWidgets('the exhausted-cache setting, the window limit, sync and clear all work with the six buttons', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear(); // no history: boot lands on the mosaic
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
        cacheSyncInterval: const Duration(minutes: 5),
      );
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-page')).evaluate().isNotEmpty, failMessage: 'no mosaic on boot');
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-tile-settings')).evaluate().isNotEmpty, failMessage: 'no Settings tile in the mosaic');

      // Mosaic: Up goes from the featured video to the nearest frame tile
      // above it, the Settings tile; Select opens it.
      await press(tester, LogicalKeyboardKey.arrowUp);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty, failMessage: 'Select on the Settings tile did not open Settings');

      // Settings: the first row has focus; Down moves to "Offline cache".
      await press(tester, LogicalKeyboardKey.arrowDown);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('offline-cache-page')).evaluate().isNotEmpty, failMessage: 'Select on "Offline cache" did not open it');
      final runtime = OfflineRuntime.current!;

      // Row 1 has focus on entry: Select flips what happens when the schedule runs out.
      expect(text(tester, 'offline-exhausted-value'), 'Loop the last known programming', reason: 'the default');
      await press(tester, LogicalKeyboardKey.enter);
      expect(text(tester, 'offline-exhausted-value'), 'Hold the slate');
      expect((await SettingsService.create()).exhaustedBehavior, ExhaustedBehavior.slate);
      await press(tester, LogicalKeyboardKey.enter);
      expect(text(tester, 'offline-exhausted-value'), 'Loop the last known programming');
      await press(tester, LogicalKeyboardKey.enter);
      expect((await SettingsService.create()).exhaustedBehavior, ExhaustedBehavior.slate, reason: 'stored for the next run');

      // Down to the window limit: Select steps to the next choice.
      await press(tester, LogicalKeyboardKey.arrowDown);
      expect(text(tester, 'offline-window-value'), startsWith('48 hours'));
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => text(tester, 'offline-window-value').startsWith('72 hours'), failMessage: 'the window limit did not step to 72h: ${text(tester, 'offline-window-value')}');
      expect((await SettingsService.create()).cacheWindowLimit, const Duration(hours: 72));
      expect(runtime.cache.clientLimit, const Duration(hours: 72));
      expect(runtime.cache.window, const Duration(hours: 48), reason: 'the effective window is still capped by the backend\'s 48h');
      // Back to 24h (72 -> 168 -> 6 -> 12 -> 24) to see the client cap bite.
      for (var i = 0; i < 4; i++) {
        await press(tester, LogicalKeyboardKey.enter);
      }
      await pumpUntil(tester, () => runtime.cache.clientLimit == const Duration(hours: 24), failMessage: 'the limit never reached 24h');
      expect(runtime.cache.window, const Duration(hours: 24));
      expect(text(tester, 'offline-window-value'), contains('in use: 24 hours'));

      // Down to "Sync now": Select refreshes and reports the last sync.
      await press(tester, LogicalKeyboardKey.arrowDown);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => text(tester, 'offline-sync-value').contains('Last sync'), failMessage: 'no sync status: ${text(tester, 'offline-sync-value')}');
      expect(text(tester, 'offline-sync-value'), contains('Can play with no backend for about'));

      // Down to "Clear cache": Select asks first; Back cancels and keeps everything.
      await press(tester, LogicalKeyboardKey.arrowDown);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('offline-clear-dialog')).evaluate().isNotEmpty, failMessage: 'no confirmation dialog');
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('offline-clear-dialog')).evaluate().isEmpty, failMessage: 'Back did not close the dialog');
      expect(runtime.cache.channels(), hasLength(2), reason: 'cancelled: nothing cleared');

      // Again, this time Right to "Clear" and Select: the cache is emptied, then refilled from the backend.
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('offline-clear-dialog')).evaluate().isNotEmpty, failMessage: 'no confirmation dialog the second time');
      await press(tester, LogicalKeyboardKey.arrowRight);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('offline-clear-dialog')).evaluate().isEmpty, failMessage: 'the dialog stayed open after confirming');
      // clearCache() empties synchronously and kicks a sync off; the refill is the proof it ran.
      await pumpUntil(tester, () => runtime.cache.channels().length == 2 && runtime.cache.lastSyncAt != null, failMessage: 'the cache was not refilled after clearing');

      // Back, Back: Offline cache -> Settings -> mosaic.
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(
        tester,
        () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty && find.byKey(const Key('offline-cache-page')).evaluate().isEmpty,
        failMessage: 'Back did not return from Offline cache to Settings',
      );
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(
        tester,
        () => find.byKey(const Key('mosaic-page')).evaluate().isNotEmpty && find.byKey(const Key('settings-page')).evaluate().isEmpty,
        failMessage: 'Back did not return from Settings to the mosaic',
      );
    });
  });
}
