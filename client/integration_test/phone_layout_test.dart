// E2E on a phone: the screens a finger can reach, laid out so that nothing
// overflows and nothing sits under a system bar.
//
// This is the layout half of the phone work; `phone_touch_test` is the input
// half. It walks home -> Settings -> home -> player -> guide on the lab's phone
// profile and asserts, at each step, that the app drew without an overflow
// exception and that the surfaces drawn into corners stay inside the window.
//
// One real app boot (budget: see AGENTS.md), phone profile only
// (`run_android_suites.sh -p phone`, listed in PHONE_ONLY).
import 'package:client/main.dart';
import 'package:client/ui/form_factor.dart';
import 'package:client/widgets/phone_mosaic_view.dart';
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

  group('phone layout (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String channelId;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      channelId = await backend.createChannel(number: 2, name: 'Layout Channel');
      final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Layout source');
      final itemIds = await backend.syncCatalogItems(mediaSourceId, [
        {'external_id': 'layout-a', 'kind': 'episode', 'title': 'LAYOUT PROGRAM', 'duration_ms': 1800000},
      ]);
      final blockId = await backend.createBlockWithItems(itemIds);
      await backend.createScheduleOnce(
        channelId: channelId,
        blockId: blockId,
        startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 3)),
      );
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    Future<void> configure() async {
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: await backend.createDeviceToken(),
      );
    }

    Future<void> pumpUntil(WidgetTester tester, bool Function() condition, {String? failMessage}) async {
      final deadline = DateTime.now().add(const Duration(seconds: 20));
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met in time');
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    Size window(WidgetTester tester) => tester.view.physicalSize / tester.view.devicePixelRatio;

    /// Every corner of [finder]'s box is inside the window.
    void insideWindow(WidgetTester tester, Finder finder, String what) {
      expect(finder, findsWidgets, reason: '$what is not on screen');
      final size = window(tester);
      for (final element in finder.evaluate()) {
        final box = element.renderObject as RenderBox?;
        if (box == null || !box.hasSize) continue;
        final rect = box.localToGlobal(Offset.zero) & box.size;
        expect(rect.left, greaterThanOrEqualTo(-0.5), reason: '$what starts off the left edge: $rect');
        expect(rect.top, greaterThanOrEqualTo(-0.5), reason: '$what starts above the top: $rect');
        expect(rect.right, lessThanOrEqualTo(size.width + 0.5), reason: '$what runs past the right: $rect');
        expect(rect.bottom, lessThanOrEqualTo(size.height + 0.5), reason: '$what runs past the bottom: $rect');
      }
    }

    testWidgets('home, Settings, player and guide fit the phone', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear(); // no history: the boot lands on the home
      await configure();
      await tester.pumpWidget(const PlayheadSpikeApp());

      await pumpUntil(tester, () => find.byKey(PhoneMosaicView.listKey).evaluate().isNotEmpty,
          failMessage: 'the phone home never became the list '
              '(form factor: ${ClientFormFactorState.notifier.value}, '
              'ring: ${find.byKey(const Key('mosaic-frame')).evaluate().isNotEmpty})');
      expect(tester.takeException(), isNull, reason: 'the home drew an overflow');

      // The featured card keeps room for the rows, at whatever height this
      // device reports.
      final card = tester.getSize(find.byKey(Key('mosaic-tile-$channelId')));
      expect(card.height, lessThanOrEqualTo(window(tester).height * PhoneMosaicView.featuredMaxHeightFraction + 0.5));

      // Settings: rows inside the window, no overflow.
      final settingsRow = find.byKey(const Key('mosaic-tile-settings'));
      await tester.ensureVisible(settingsRow);
      await tester.pump(const Duration(milliseconds: 200));
      await tester.tap(settingsRow);
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty,
          failMessage: 'a tap on Settings did not open it');
      await tester.pump(const Duration(milliseconds: 400));
      expect(tester.takeException(), isNull, reason: 'Settings drew an overflow');
      insideWindow(tester, find.byKey(const Key('settings-page')), 'the Settings page');

      // Back to the home, then into the channel.
      await pressKey(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(PhoneMosaicView.listKey).evaluate().isNotEmpty,
          failMessage: 'Back did not return to the home');
      await pumpUntil(tester, () => find.byKey(Key('mosaic-tile-$channelId')).evaluate().isNotEmpty,
          failMessage: 'the channel row is gone');
      await tester.tap(find.byKey(Key('mosaic-tile-$channelId')));
      await pumpUntil(tester, () => find.byKey(const Key('channel-osd')).evaluate().isNotEmpty,
          failMessage: 'a tap on the channel row did not open the player');
      await tester.pump(const Duration(milliseconds: 600));
      expect(tester.takeException(), isNull, reason: 'the player drew an overflow');
      insideWindow(tester, find.byKey(const Key('channel-osd')), 'the OSD panel');
      insideWindow(tester, find.byKey(const Key('osd-clock')), 'the OSD clock');

      // The guide, from a swipe up: its header and body inside the window too.
      await tester.pump(const Duration(seconds: 1));
      final size = window(tester);
      await tester.flingFrom(Offset(size.width / 2, size.height * 0.35), const Offset(0, -320), 1200);
      await pumpUntil(tester, () => find.byKey(const Key('epg-time-header')).evaluate().isNotEmpty,
          failMessage: 'a swipe up did not open the guide');
      await tester.pump(const Duration(milliseconds: 400));
      expect(tester.takeException(), isNull, reason: 'the guide drew an overflow');
      insideWindow(tester, find.byKey(const Key('epg-time-header')), 'the guide header');
      final cells = find.byWidgetPredicate(
        (w) => w.key is ValueKey<String> && (w.key! as ValueKey<String>).value.startsWith('epg-cell-'),
      );
      insideWindow(tester, cells.first, 'a guide cell');
    });
  });
}
