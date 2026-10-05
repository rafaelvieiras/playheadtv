// E2E: everything behind the video in the player is absolute black (0xFF000000),
// both on the loading screen and while playing. One real app boot.
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

  group('player background (real e2e)', () {
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

    // The colour the player's Scaffold actually paints: its own backgroundColor,
    // or else the theme's scaffold colour.
    Color effectiveBackground(WidgetTester tester) {
      final finder = find.descendant(of: find.byType(ChannelPlayerPage), matching: find.byType(Scaffold));
      final scaffold = tester.widget<Scaffold>(finder.first);
      return scaffold.backgroundColor ?? Theme.of(tester.element(finder.first)).scaffoldBackgroundColor;
    }

    testWidgets('the player background is pure black while loading and while playing', (tester) async {
      final channel = await seedChannel(backend, number: 51, prefix: 'BGA', items: 4, itemMs: 20000);
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

      // Tight pumps: the player page's very first frames are the loading
      // screen (the first item starts playing shortly after).
      final deadline = DateTime.now().add(const Duration(seconds: 20));
      while (find.byType(ChannelPlayerPage).evaluate().isEmpty) {
        if (DateTime.now().isAfter(deadline)) fail('the player page never showed');
        await tester.pump(const Duration(milliseconds: 16));
      }
      expect(effectiveBackground(tester), const Color(0xFF000000), reason: 'player page at boot (loading)');

      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'never started playing');
      expect(effectiveBackground(tester), const Color(0xFF000000), reason: 'while playing');
    });
  });
}
