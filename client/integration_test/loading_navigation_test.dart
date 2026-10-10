// E2E: a channel stuck on the loading spinner never blocks navigation. While
// the first open of a channel is stalled (a slow network- or cloud-backed
// source) the OSD opens, the EPG opens and a channel switch is accepted and
// abandons the pending load; the stalled open finishing late is disposed
// without disturbing the channel the viewer moved to.
//
// Timings are shrunk (a 1s stall bound, a 2s GL queue valve); nothing waits
// real minutes. Two real app boots.
import 'package:client/main.dart';
import 'package:client/util/gl_serial.dart';
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

  group('loading never blocks navigation (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;
    late String fastA;
    late String stalledB;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
      fastA = await seedChannel(backend, number: 71, prefix: 'FASTA', items: 3, itemMs: 60000, startOffset: const Duration(seconds: -1));
      stalledB = await seedChannel(backend, number: 72, prefix: 'STALLB', items: 3, itemMs: 60000, startOffset: const Duration(seconds: -1));
      await seedChannel(backend, number: 73, prefix: 'FASTC', items: 3, itemMs: 60000, startOffset: const Duration(seconds: -1));
      glOpQueueTimeout = const Duration(seconds: 2);
    });

    tearDownAll(() async {
      glOpQueueTimeout = defaultGlOpQueueTimeout;
      await backend.stop();
      await video.stop();
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
    });

    bool spinnerShown() => find.byKey(const Key('loading-indicator')).evaluate().isNotEmpty;
    bool loadingOsdShown() => find.byKey(const Key('loading-osd')).evaluate().isNotEmpty;
    bool epgOpen() => find.byKey(const Key('epg-overlay')).evaluate().isNotEmpty;

    Future<void> boot(WidgetTester tester, String lastWatched) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', lastWatched);
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
        channelLoadStallBound: const Duration(seconds: 1),
      );
      await tester.pumpWidget(const PlayheadSpikeApp());
    }

    // The whole acceptance: on the spinner, the OSD opens (Select), the EPG
    // opens (Select again), Back closes it, and Up switches to a channel that
    // plays.
    Future<void> navigateAwayFromSpinner(WidgetTester tester) async {
      await pumpUntil(tester, spinnerShown, failMessage: 'the stalled channel never showed the spinner');
      await pressKey(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, loadingOsdShown, failMessage: 'Select did not show the OSD over the spinner');
      await pressKey(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, epgOpen, failMessage: 'a second Select did not open the EPG over the spinner');
      await pressKey(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => !epgOpen(), failMessage: 'Back did not close the EPG');
      expect(spinnerShown(), isTrue, reason: 'still loading the stalled channel');

      await pressKey(tester, LogicalKeyboardKey.arrowUp); // next channel: FASTC
      await pumpUntil(tester, () => osdTitle(tester) == 'FASTC 0',
          timeout: const Duration(seconds: 40), failMessage: 'the switch off the stalled channel never reached FASTC');
    }

    testWidgets('a switch off a stalled channel load is accepted (OSD and EPG open too)', (tester) async {
      video.slowOpen('STALLB', const Duration(seconds: 14));
      await boot(tester, fastA);
      await pumpUntil(tester, () => osdTitle(tester) == 'FASTA 0', failMessage: 'never landed on FASTA 0');
      await pressKey(tester, LogicalKeyboardKey.arrowUp); // next channel: STALLB, which never opens in time
      await navigateAwayFromSpinner(tester);
      // The stalled open finishes late: its controller is dropped, FASTC stays.
      await tester.pump(const Duration(seconds: 16));
      expect(osdTitle(tester), 'FASTC 0');
      expect(find.byKey(const Key('error-banner')), findsNothing);
      video.slowOpen('STALLB', Duration.zero);
    });

    testWidgets('a channel that is stalled right at boot does not block navigation either', (tester) async {
      video.slowOpen('STALLB', const Duration(seconds: 14));
      await boot(tester, stalledB);
      await navigateAwayFromSpinner(tester);
      await tester.pump(const Duration(seconds: 16));
      expect(osdTitle(tester), 'FASTC 0');
      video.slowOpen('STALLB', Duration.zero);
    });
  });
}
