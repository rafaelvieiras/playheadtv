// E2E suite for the channel OSD (issue #16): layout elements and focus mode.
// Same harness as track_selector_test.dart (VideoFixture with 2 audio tracks).
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('channel OSD (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String channelId;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start(audioTracks: 2);
      await backend.start();

      channelId = await backend.createChannel(number: 4, name: 'Osd Channel 4');
      final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Osd source 4');
      final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
        {'external_id': 'osd-4-a', 'kind': 'episode', 'title': 'OSD PROGRAM 4 A', 'duration_ms': 1800000},
        {'external_id': 'osd-4-b', 'kind': 'other', 'title': 'OSD COMMERCIAL 4', 'duration_ms': 1800000},
      ]);
      final blockId = await backend.createBlockWithItems(catalogItemIds);
      await backend.createScheduleOnce(
        channelId: channelId,
        blockId: blockId,
        startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 3)),
      );

      final deviceToken = await backend.createDeviceToken();

      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
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
      // Always lands on the player (not the mosaic) with the lowest-number
      // channel, same pattern as channel_navigation_test.dart.
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-osd');
    });

    Future<void> pumpUntil(
      WidgetTester tester,
      bool Function() condition, {
      Duration timeout = const Duration(seconds: 15),
      String? failMessage,
    }) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) {
          fail(failMessage ?? 'condition not met within $timeout');
        }
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    bool osdVisible(WidgetTester tester) {
      final finder = find.ancestor(
        of: find.byKey(const Key('channel-osd')),
        matching: find.byType(AnimatedOpacity),
      );
      if (finder.evaluate().isEmpty) return false;
      return tester.widget<AnimatedOpacity>(finder).opacity == 1;
    }

    Future<void> bootAndHideOsd(WidgetTester tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'OSD never auto-showed at boot');
      await pumpUntil(
        tester,
        () => !osdVisible(tester),
        timeout: const Duration(seconds: 8),
        failMessage: 'OSD never hid on its own after the boot auto-show',
      );
    }

    testWidgets('OSD shows title, channel line, clock, end time, disabled transport controls and the audio/Channels buttons', (tester) async {
      await bootAndHideOsd(tester);
      await tester.sendKeyEvent(LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Enter did not show the OSD');

      expect(tester.widget<Text>(find.byKey(const Key('osd-item-title'))).data, 'OSD PROGRAM 4 A');
      expect(tester.widget<Text>(find.byKey(const Key('osd-channel-number'))).data, '4');
      expect(find.byKey(const Key('osd-clock')), findsOneWidget);
      expect(tester.widget<Text>(find.byKey(const Key('osd-ends-at'))).data, startsWith('Ends '));
      for (final k in ['play-pause', 'previous', 'rewind', 'fast-forward', 'next']) {
        expect(find.byKey(Key('osd-btn-$k')), findsOneWidget, reason: 'transport control $k missing');
      }
      // The fixture clip has 2 audio tracks and no subtitle track; media info
      // loads asynchronously, so wait for the audio button to appear.
      await pumpUntil(
        tester,
        () => find.byKey(const Key('osd-btn-audio')).evaluate().isNotEmpty,
        failMessage: 'audio button never appeared in the OSD',
      );
      expect(find.byKey(const Key('osd-btn-audio')), findsOneWidget);
      expect(find.byKey(const Key('osd-btn-captions')), findsNothing);
      expect(find.byKey(const Key('osd-btn-channels')), findsOneWidget);
    });

    testWidgets('in focus mode Up/Down move OSD focus instead of switching channel, and Back hides the OSD', (tester) async {
      await bootAndHideOsd(tester);
      await tester.sendKeyEvent(LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Enter did not show the OSD');

      await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown); // audio -> Channels
      await tester.pump();
      await tester.sendKeyEvent(LogicalKeyboardKey.arrowUp); // back to audio
      await tester.pump();
      expect(tester.widget<Text>(find.byKey(const Key('osd-channel-number'))).data, '4',
          reason: 'arrow keys in focus mode must not switch channel (only one channel exists, so also check the OSD stayed)');
      expect(osdVisible(tester), isTrue);

      await tester.sendKeyEvent(LogicalKeyboardKey.escape);
      await tester.pump();
      await pumpUntil(tester, () => !osdVisible(tester), failMessage: 'Back did not hide the OSD in focus mode');
      expect(find.byKey(const Key('osd-channel-number')), findsOneWidget, reason: 'Back must not leave the player');
    });
  });
}
