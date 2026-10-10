// E2E: after a transition exactly one controller is audible. The outgoing item
// is silenced and disposed when its crossfade completes, whether the preload
// was ready in time or the player had to wait for a late one, and whether the
// item's file is longer than its schedule entry (the transition follows the
// entry, so the outgoing file still has material left when it is faded out).
//
// The fixture serves one 12s file for every item. Timings are shrunk through
// AppConfig.overrideForTest. Two real app boots.
import 'package:client/main.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:video_player/video_player.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

typedef Sample = ({DateTime at, String? title, List<({double volume, bool playing})> players});

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('audio after a transition (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start(clipSeconds: 12);
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

    Future<void> boot(WidgetTester tester, int number, String prefix, int itemMs) async {
      final channelId = await seedChannel(backend, number: number, prefix: prefix, items: 4, itemMs: itemMs, startOffset: const Duration(seconds: -1));
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

    Sample sample(WidgetTester tester) {
      final players = <({double volume, bool playing})>[];
      for (final element in find.byType(VideoPlayer).evaluate()) {
        final controller = (element.widget as VideoPlayer).controller;
        if (controller.value.isInitialized) players.add((volume: controller.value.volume, playing: controller.value.isPlaying));
      }
      return (at: DateTime.now(), title: osdTitle(tester), players: players);
    }

    // Samples until the OSD names [title], then keeps sampling for [after].
    Future<List<Sample>> sampleAcross(WidgetTester tester, String title, Duration after) async {
      final samples = <Sample>[];
      await pumpUntil(tester, () {
        samples.add(sample(tester));
        return samples.last.title == title;
      }, timeout: const Duration(seconds: 60), failMessage: 'never crossed over to $title');
      final until = DateTime.now().add(after);
      while (DateTime.now().isBefore(until)) {
        await tester.pump(const Duration(milliseconds: 100));
        samples.add(sample(tester));
      }
      return samples;
    }

    // Once the crossfade (800ms) is over, only the item on air may be heard.
    void expectOneAudible(List<Sample> samples, String title) {
      final settled = samples.where((s) => s.title == title).skip(10).toList();
      expect(settled, isNotEmpty);
      for (final s in settled) {
        final audible = s.players.where((p) => p.playing && p.volume > 0.01).length;
        expect(audible, lessThanOrEqualTo(1), reason: 'players ${s.players} while on air: $title');
        final playing = s.players.where((p) => p.playing).length;
        expect(playing, lessThanOrEqualTo(1), reason: 'players ${s.players} while on air: $title (the outgoing item still plays)');
      }
    }

    testWidgets('a file longer than its entry: only the new item is heard once the crossfade ends', (tester) async {
      // 7s entries over the 12s file: the entry ends 5s before the file does.
      await boot(tester, 93, 'LONG', 7000);
      final first = await sampleAcross(tester, 'LONG 1', const Duration(seconds: 2));
      expectOneAudible(first, 'LONG 1');
      final second = await sampleAcross(tester, 'LONG 2', const Duration(seconds: 2));
      expectOneAudible(second, 'LONG 2');
      // The next transition is timed from the moment the incoming item started
      // (the start of the crossfade), not from the end of the ramp, so the
      // 7s entry is not stretched by the crossfade and the handling around it.
      final arrivedAt1 = first.firstWhere((s) => s.title == 'LONG 1').at;
      final arrivedAt2 = second.firstWhere((s) => s.title == 'LONG 2').at;
      final entry = arrivedAt2.difference(arrivedAt1);
      expect(entry, lessThan(const Duration(milliseconds: 7500)), reason: 'a 7s entry took $entry from one transition to the next');
      expect(entry, greaterThan(const Duration(milliseconds: 6500)), reason: 'a 7s entry took $entry from one transition to the next');
    });

    testWidgets('a late preload: only the new item is heard once the crossfade ends', (tester) async {
      video.slowOpen('LATE-1', const Duration(seconds: 4));
      await boot(tester, 94, 'LATE', 8000);
      final samples = await sampleAcross(tester, 'LATE 1', const Duration(seconds: 2));
      video.slowOpen('LATE-1', Duration.zero);
      expectOneAudible(samples, 'LATE 1');
    });
  });
}
