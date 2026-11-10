// E2E on a phone: everything the picture answers to a finger.
//
// The unit tests pin the gesture layer and the phone views on their own; this
// suite presses the real app with them, on the phone profile of the lab, where
// the form factor comes from the platform (nothing is overridden here):
//
//  - the home is the phone's column, not the television's ring;
//  - a tap on a channel row opens that channel;
//  - a tap on the picture hides the OSD, and another shows it;
//  - a horizontal fling switches channel, both ways;
//  - a swipe up opens the guide, and its Back closes it.
//
// One real app boot (budget: see AGENTS.md), phone profile only
// (`run_android_suites.sh -p phone`, listed in PHONE_ONLY): the television's
// layout is the same code with different metrics, and it has its own suites.
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

  group('phone touch (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    // Two channels, so a switch has somewhere to go, and the first one has the
    // lower number so the boot lands on it.
    const channelNumbers = [2, 7];
    late Map<int, String> channelIds;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      channelIds = {};
      for (final number in channelNumbers) {
        final channelId = await backend.createChannel(number: number, name: 'Touch Channel $number');
        channelIds[number] = channelId;
        final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Touch source $number');
        final itemIds = await backend.syncCatalogItems(mediaSourceId, [
          {'external_id': 'touch-$number-a', 'kind': 'episode', 'title': 'TOUCH PROGRAM $number', 'duration_ms': 1800000},
        ]);
        final blockId = await backend.createBlockWithItems(itemIds);
        await backend.createScheduleOnce(
          channelId: channelId,
          blockId: blockId,
          startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 3)),
        );
      }
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

    bool osdVisible(WidgetTester tester) {
      final finder = find.ancestor(of: find.byKey(const Key('channel-osd')), matching: find.byType(AnimatedOpacity));
      return finder.evaluate().isNotEmpty && tester.widget<AnimatedOpacity>(finder).opacity == 1;
    }

    // The OSD draws the channel name upper-cased (its own typography), so the
    // assertions read it the same way instead of pinning a display style.
    String osdChannel(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-channel-name'));
      if (finder.evaluate().isEmpty) return '';
      return (tester.widget<Text>(finder).data ?? '').toUpperCase();
    }

    bool guideOpen() => find.byKey(const Key('epg-details-panel')).evaluate().isNotEmpty;

    // A point on the picture that no overlay covers: the OSD is a bottom sheet
    // on a phone and the clock sits in the top-right corner.
    Offset picturePoint(WidgetTester tester) {
      final size = tester.view.physicalSize / tester.view.devicePixelRatio;
      return Offset(size.width / 2, size.height * 0.35);
    }

    testWidgets('the column opens a channel, and the picture answers taps and flings', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear(); // no history: the boot lands on the home
      await configure();
      await tester.pumpWidget(const PlayheadSpikeApp());

      // The home is the column, not the ring.
      await pumpUntil(tester, () => find.byKey(PhoneMosaicView.listKey).evaluate().isNotEmpty,
          failMessage: 'the phone home never became the list '
              '(form factor: ${ClientFormFactorState.notifier.value}, '
              'ring: ${find.byKey(const Key('mosaic-frame')).evaluate().isNotEmpty})');
      expect(find.byKey(const Key('mosaic-frame')), findsNothing, reason: 'the ring is the television layout');
      final second = find.byKey(Key('mosaic-tile-${channelIds[7]}'));
      await pumpUntil(tester, () => second.evaluate().isNotEmpty, failMessage: 'channel 7 is not in the column');

      // A tap on a row opens that channel — no keys involved.
      await tester.ensureVisible(second);
      await tester.pump(const Duration(milliseconds: 200));
      await tester.tap(second);
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-page')).evaluate().isEmpty && osdVisible(tester),
          failMessage: 'a tap on the row did not open channel 7');
      expect(osdChannel(tester), contains('TOUCH CHANNEL 7'));

      await pumpUntil(tester, () => !osdVisible(tester), failMessage: 'the OSD never auto-hid');

      // Tap on the picture: show, tap again: hide.
      await tester.tapAt(picturePoint(tester));
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'a tap on the picture did not show the OSD');
      await tester.tapAt(picturePoint(tester));
      await pumpUntil(tester, () => !osdVisible(tester), failMessage: 'a second tap did not hide the OSD');

      // A fling goes to the next channel (`1` is next), and the other way back.
      await tester.flingFrom(picturePoint(tester), const Offset(-320, 0), 1200);
      await pumpUntil(tester, () => osdChannel(tester).isNotEmpty, failMessage: 'the fling did not switch channel');
      await pumpUntil(tester, () => osdChannel(tester).contains('TOUCH CHANNEL 2'),
          failMessage: 'a left fling did not move to channel 2 (OSD: ${osdChannel(tester)})');

      await pumpUntil(tester, () => !osdVisible(tester), failMessage: 'the OSD never auto-hid again');
      await tester.flingFrom(picturePoint(tester), const Offset(320, 0), 1200);
      await pumpUntil(tester, () => osdChannel(tester).contains('TOUCH CHANNEL 7'),
          failMessage: 'a right fling did not come back to channel 7 (OSD: ${osdChannel(tester)})');

      // A swipe up opens the guide; its cells are the same view the television
      // reads, and Back closes it.
      await pumpUntil(tester, () => !osdVisible(tester), failMessage: 'the OSD never auto-hid before the swipe');
      await tester.flingFrom(picturePoint(tester), const Offset(0, -320), 1200);
      await pumpUntil(tester, guideOpen, failMessage: 'a swipe up did not open the guide');
      expect(find.byKey(const Key('player-gestures')), findsOneWidget);
      final cells = find.byWidgetPredicate(
        (w) => w.key is ValueKey<String> && (w.key! as ValueKey<String>).value.startsWith('epg-cell-'),
      );
      expect(cells, findsWidgets, reason: 'the guide drew no cells');

      await pressKey(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => !guideOpen(), failMessage: 'Back did not close the guide');
    });
  });
}
