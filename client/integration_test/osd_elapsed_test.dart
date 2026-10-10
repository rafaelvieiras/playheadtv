// E2E: right after a program change the OSD shows how far into the new program
// the picture is (about 0:00), not the wall-clock distance from the guide's
// start. Two real app boots.
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

Duration? _elapsed(WidgetTester tester) {
  final finder = find.byKey(const Key('osd-elapsed'));
  if (finder.evaluate().isEmpty) return null;
  final text = tester.widget<Text>(finder).data!;
  final parts = text.split(' / ').first.split(':').map(int.parse).toList();
  return parts.length == 2 ? Duration(minutes: parts[0], seconds: parts[1]) : Duration(hours: parts[0], minutes: parts[1], seconds: parts[2]);
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('OSD elapsed time at a program change (real e2e)', () {
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

    Future<void> boot(WidgetTester tester, int number, String prefix) async {
      final channelId = await seedChannel(backend, number: number, prefix: prefix, items: 6, itemMs: 12000, startOffset: const Duration(seconds: -1));
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelId);
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
        preloadLead: const Duration(seconds: 2),
        preloadRetryBase: const Duration(milliseconds: 200),
      );
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) == '$prefix 0', failMessage: 'never landed on $prefix 0');
    }

    // Pumps until the OSD names [title] and returns the elapsed readout of the
    // first frame that shows it.
    Future<Duration> elapsedAt(WidgetTester tester, String title) async {
      await pumpUntil(tester, () => osdTitle(tester) == title, timeout: const Duration(seconds: 60), failMessage: 'never reached $title');
      final e = _elapsed(tester)!;
      // ignore: avoid_print
      print('OSD-ELAPSED $title -> $e');
      return e;
    }

    testWidgets('a normal transition starts near 0:00', (tester) async {
      await boot(tester, 81, 'ELN');
      for (var i = 1; i <= 3; i++) {
        final e = await elapsedAt(tester, 'ELN $i');
        expect(e, lessThanOrEqualTo(const Duration(seconds: 2)), reason: 'OSD elapsed right after the change to ELN $i');
      }
    });

    testWidgets('a late transition shows the offset the picture really joined at', (tester) async {
      video.slowOpen('ELL-1', const Duration(seconds: 5));
      await boot(tester, 82, 'ELL');
      final e = await elapsedAt(tester, 'ELL 1');
      video.slowOpen('ELL-1', Duration.zero);
      // The preload took longer than the lead, the player seeked into the item
      // to catch up: the readout is that true offset, and never more.
      expect(e, lessThanOrEqualTo(const Duration(seconds: 7)));
    });
  });
}
