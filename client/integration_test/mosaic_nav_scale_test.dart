// E2E for the mosaic's scaled frame and spatial navigation, one real boot (see
// mosaic_test.dart for the harness conventions: real backend + fake media
// server, keys through `pressKey`, never `pumpAndSettle`).
//
// Five channels and no history: the featured tile is channel 2 and the frame is
// partly filled — the four other channels in the top row (random order) and the
// Settings tile at the right end of the next row — so the occupied grid cells
// are known exactly even though the channel order is shuffled.
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:client/video/video_engine.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('mosaic scale and spatial navigation (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    const numbers = [2, 3, 4, 5, 6];

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      for (final number in numbers) {
        final channelId = await backend.createChannel(number: number, name: 'Scale Channel $number');
        final sourceId = await backend.createLocalFolderMediaSource(name: 'Scale source $number');
        final items = await backend.syncCatalogItems(sourceId, [
          {'external_id': 'scale-$number-a', 'kind': 'episode', 'title': 'SCALE PROGRAM $number', 'duration_ms': 1800000},
        ]);
        final blockId = await backend.createBlockWithItems(items);
        await backend.createScheduleOnce(
          channelId: channelId,
          blockId: blockId,
          startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 3)),
        );
      }
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
    });

    Future<void> pumpUntil(WidgetTester tester, bool Function() condition, {String? failMessage}) async {
      final deadline = DateTime.now().add(const Duration(seconds: 20));
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met in time');
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    Finder tiles() => find.byWidgetPredicate((w) {
          final k = w.key;
          return w is Container && k is ValueKey<String> && k.value.startsWith('mosaic-tile-');
        });

    // (row, col) of the focused tile in the 4x4 grid, read off the real layout
    // (the featured block reports its top-left cell, (1, 1)).
    (int, int) focusedCell(WidgetTester tester) {
      final frame = tester.getRect(find.byKey(const Key('mosaic-frame')));
      final cellW = frame.width / (4 + 3 * 8 / 160);
      final step = cellW * (1 + 8 / 160);
      for (final element in tiles().evaluate()) {
        // The focused tile is the one with the white (not white24) ring.
        final border = ((element.widget as Container).decoration! as BoxDecoration).border! as Border;
        if (border.top.color != Colors.white) continue;
        final box = element.renderObject! as RenderBox;
        final topLeft = box.localToGlobal(Offset.zero) - frame.topLeft;
        return ((topLeft.dy / (step * 90 / 160)).round(), (topLeft.dx / step).round());
      }
      fail('no mosaic tile has the focus');
    }

    Future<(int, int)> press(WidgetTester tester, LogicalKeyboardKey key) async {
      await pressKey(tester, key);
      await tester.pump(const Duration(milliseconds: 50));
      return focusedCell(tester);
    }

    testWidgets('frame fills the height at two sizes, the preview keeps playing, arrows reach every cell',
        (tester) async {
      tester.view.devicePixelRatio = 1.0;
      tester.view.physicalSize = const Size(1280, 720);
      addTearDown(tester.view.reset);

      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-frame')).evaluate().isNotEmpty,
          failMessage: 'mosaic frame did not appear');
      await pumpUntil(tester, () => find.byType(VideoSurface).evaluate().isNotEmpty,
          failMessage: 'the featured preview never started');

      double frameHeight() => tester.getSize(find.byKey(const Key('mosaic-frame'))).height;
      expect(frameHeight(), closeTo(720 * 0.96, 0.5));
      expect(tiles().evaluate().length, 6); // featured + 4 channels + Settings

      final controller = tester.widget<VideoSurface>(find.byType(VideoSurface)).playback;
      // The surface is in the tree before the engine reports playing, and a
      // device takes longer to get there than the desktop: wait for the state
      // instead of asserting on the frame right after the widget appeared.
      await pumpUntil(tester, () => controller.state.isPlaying,
          failMessage: 'the featured preview never started playing');

      // A 21:9 window: same height rule, same controller, still playing.
      tester.view.physicalSize = const Size(2560, 1080);
      await tester.pump(const Duration(milliseconds: 500));
      expect(frameHeight(), closeTo(1080 * 0.96, 0.5));
      expect(identical(tester.widget<VideoSurface>(find.byType(VideoSurface)).playback, controller), isTrue);
      final position = controller.state.position;
      await tester.pump(const Duration(seconds: 2));
      expect(controller.state.isPlaying, isTrue);
      expect(controller.state.position, greaterThan(position));
      expect(tester.takeException(), isNull);

      // Portrait: fits the width, no overflow.
      tester.view.physicalSize = const Size(600, 1000);
      await tester.pump(const Duration(milliseconds: 500));
      expect(tester.getSize(find.byKey(const Key('mosaic-frame'))).width, closeTo(600 * 0.96, 0.5));
      expect(tester.takeException(), isNull);
      tester.view.physicalSize = const Size(1280, 720);
      await tester.pump(const Duration(milliseconds: 500));

      // Spatial navigation, six buttons only. Occupied cells: the top row, the
      // Settings tile at (1, 3) and the featured block.
      final visited = <(int, int)>{focusedCell(tester)};
      expect(visited.single, (1, 1));
      Future<void> expectMove(LogicalKeyboardKey key, (int, int) expected) async {
        final cell = await press(tester, key);
        expect(cell, expected, reason: '$key');
        visited.add(cell);
      }

      await expectMove(LogicalKeyboardKey.arrowUp, (0, 1));
      await expectMove(LogicalKeyboardKey.arrowLeft, (0, 0));
      await expectMove(LogicalKeyboardKey.arrowLeft, (0, 0)); // nothing further: stays
      await expectMove(LogicalKeyboardKey.arrowRight, (0, 1));
      await expectMove(LogicalKeyboardKey.arrowRight, (0, 2));
      await expectMove(LogicalKeyboardKey.arrowRight, (0, 3));
      await expectMove(LogicalKeyboardKey.arrowRight, (0, 3));
      await expectMove(LogicalKeyboardKey.arrowDown, (1, 3)); // Settings
      await expectMove(LogicalKeyboardKey.arrowDown, (1, 1)); // nothing below: toward the centre
      await expectMove(LogicalKeyboardKey.arrowUp, (1, 3)); // reversible
      await expectMove(LogicalKeyboardKey.arrowUp, (0, 3));
      await expectMove(LogicalKeyboardKey.arrowDown, (1, 3));
      await expectMove(LogicalKeyboardKey.arrowLeft, (1, 1)); // the featured block
      await expectMove(LogicalKeyboardKey.arrowDown, (1, 1)); // empty below: stays
      expect(visited, {(1, 1), (0, 0), (0, 1), (0, 2), (0, 3), (1, 3)});

      // Select on the Settings tile still opens Settings.
      await expectMove(LogicalKeyboardKey.arrowRight, (1, 3));
      await pressKey(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty,
          failMessage: 'Select on the Settings tile did not open Settings');
    });
  });
}
