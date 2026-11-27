// A channel change has a delay (Settings -> Channel switch delay, 3 s by
// default): the banner announces the target at once, the channel that was on air
// keeps playing, and the media server is only asked for the channel the viewer
// stops on. This suite pins all three, plus what happens when the delay is off.
//
// The proof that the server was not asked is `video.videoRequests`: a request for
// an item of a channel the viewer merely passed through is exactly the cost the
// delay exists to avoid.
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('channel switch delay (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    const channelNumbers = [1, 5, 9];

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();

      for (final number in channelNumbers) {
        final channelId = await backend.createChannel(number: number, name: 'Delay $number');
        final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Delay source $number');
        final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
          {
            'external_id': 'delay-$number-a',
            'kind': 'episode',
            'title': 'DELAY $number A',
            'duration_ms': 1800000,
            // Details the banner and the guide panel show.
            'overview': 'What channel $number is about.',
            'production_year': 2015,
            'content_rating': 'PG-13',
            'genres': 'Drama',
            'video_codec': 'h264',
            'audio_codec': 'aac',
            'audio_channels': 2,
            'height': 720,
          },
        ]);
        final blockId = await backend.createBlockWithItems(catalogItemIds);
        await backend.createScheduleOnce(
          channelId: channelId,
          blockId: blockId,
          startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 3)),
        );
      }

      final deviceToken = await backend.createDeviceToken();
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'chave-de-teste-fake',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
      );
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-delay');
    });

    Future<void> pumpUntil(
      WidgetTester tester,
      bool Function() condition, {
      Duration timeout = const Duration(seconds: 20),
      String? failMessage,
    }) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met within $timeout');
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    String? bannerNumber(WidgetTester tester) {
      final finder = find.byKey(const Key('channel-banner-number'));
      if (finder.evaluate().isEmpty) return null;
      return tester.widget<Text>(finder).data;
    }

    bool bannerShown() => find.byKey(const Key('channel-banner')).evaluate().isNotEmpty;

    // A request for the stream of one of channel `number`'s items.
    bool requestedChannel(int number) => video.videoRequests.any((path) => path.contains('delay-$number-'));

    Future<void> boot(WidgetTester tester, {int delaySeconds = 3}) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setInt('channel_switch_delay_seconds', delaySeconds);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => requestedChannel(1), failMessage: 'channel 1 never started');
      await pumpUntil(tester, () => bannerNumber(tester) == '1', failMessage: 'the banner never announced channel 1 at boot');
    }

    testWidgets('the banner announces the target at once and the picture only changes after the delay', (tester) async {
      await boot(tester);
      final before = video.videoRequests.length;

      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump(const Duration(milliseconds: 300));

      // Announced immediately, from the guide: number, program and synopsis.
      expect(bannerNumber(tester), '5');
      expect(find.text('DELAY 5 A'), findsWidgets);
      // Nothing was asked of the server yet, and the old picture is still the
      // one on screen.
      expect(video.videoRequests.length, before, reason: 'the delay must not open the next channel yet');
      expect(requestedChannel(5), isFalse);

      // After the delay the target is playing.
      await pumpUntil(tester, () => requestedChannel(5), failMessage: 'channel 5 never opened after the delay');
      expect(bannerNumber(tester), '5');
    });

    testWidgets('surfing three channels contacts the server once', (tester) async {
      // A long delay on purpose: the point of the test is that two presses inside
      // ONE window open nothing but the last target, and the live binding makes
      // `pump(400ms)` cost more or less real time than it says — with 10 s there
      // is no way for the presses to straddle the window's end.
      await boot(tester, delaySeconds: 10);
      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump(const Duration(milliseconds: 300));
      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump(const Duration(milliseconds: 300));

      // Two presses inside one window: the banner walked to channel 9 and only
      // it may be opened.
      expect(bannerNumber(tester), '9');
      await pumpUntil(tester, () => requestedChannel(9), timeout: const Duration(seconds: 40),
          failMessage: 'channel 9 never opened');
      expect(requestedChannel(5), isFalse, reason: 'a channel passed through must not be opened');
    });

    testWidgets('with the delay off the picture changes at once', (tester) async {
      await boot(tester, delaySeconds: 0);
      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await pumpUntil(tester, () => requestedChannel(5), timeout: const Duration(seconds: 3),
          failMessage: 'a zero delay must open the next channel immediately');
    });

    testWidgets('the banner goes away on its own', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setInt('channel_banner_seconds', 3);
      await boot(tester, delaySeconds: 1);
      expect(bannerShown(), isTrue);
      await pumpUntil(tester, () => !bannerShown(), timeout: const Duration(seconds: 30),
          failMessage: 'the banner never went away');
    });
  });
}
