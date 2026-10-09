// E2E: Settings -> Preload lead is changeable with ONLY the six baseline
// buttons (Up/Down/Left/Right/Select/Back), is stored for the next run and
// becomes the lead the player uses; a test override still wins.
//
// One real app boot.
import 'package:client/main.dart';
import 'package:client/preload_lead.dart';
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

  group('Settings -> Preload lead with six buttons (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
      await seedChannel(backend, number: 93, prefix: 'LEADA', items: 4, itemMs: 60000);
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

    // True when the focused widget is inside the row with this key.
    bool tileFocused(String key) {
      var found = false;
      FocusManager.instance.primaryFocus?.context?.visitAncestorElements((a) {
        if (a.widget.key == Key(key)) {
          found = true;
          return false;
        }
        return true;
      });
      return found;
    }

    testWidgets('Select steps the lead, it is stored and used', (tester) async {
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
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-tile-settings')).evaluate().isNotEmpty, failMessage: 'no Settings tile in the mosaic');

      await press(tester, LogicalKeyboardKey.arrowUp);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty, failMessage: 'Settings did not open');

      // Down until the Preload lead row has focus (the rows before it depend
      // on the platform).
      for (var i = 0; i < 8 && !tileFocused('settings-preload-lead-tile'); i++) {
        await press(tester, LogicalKeyboardKey.arrowDown);
      }
      expect(tileFocused('settings-preload-lead-tile'), isTrue, reason: 'Down never reached the Preload lead row');

      expect(text(tester, 'settings-preload-lead-value'), '20 seconds', reason: 'the default');
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => text(tester, 'settings-preload-lead-value') == '30 seconds', failMessage: 'Select did not step to 30 seconds: ${text(tester, 'settings-preload-lead-value')}');

      final settings = await SettingsService.create();
      expect(settings.preloadLeadSeconds, 30, reason: 'stored for the next run');
      expect(AppConfig.configuredPreloadLead(settings), const Duration(seconds: 30), reason: 'the player uses the saved choice');

      // Every choice is reachable and the list wraps.
      for (var i = 0; i < preloadLeadChoicesSeconds.length; i++) {
        await press(tester, LogicalKeyboardKey.enter);
      }
      expect(text(tester, 'settings-preload-lead-value'), '30 seconds');

      // Back leaves Settings; the choice survives.
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isEmpty, failMessage: 'Back did not leave Settings');
      expect((await SettingsService.create()).preloadLeadSeconds, 30);
    });
  });
}
