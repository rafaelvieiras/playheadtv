// Real e2e suite for the Flutter client: boots the real app
// (ChannelPlayerPage) against a real Go backend (BackendFixture) and a fake
// video server (VideoFixture, playing the role of "Jellyfin"), and drives
// keyboard navigation through the real `integration_test` binding — not via
// OS-level input tools, so it doesn't depend on the desktop session's input
// state (see AGENTS.md).
//
// No `tester.pumpAndSettle()` here: while the OSD is visible, the ticker in
// `_OsdOverlay` (main.dart) calls `setState` every 500ms forever —
// pumpAndSettle would never settle. Every wait-for-condition below uses manual
// polling (`tester.pump` in a loop) with an explicit timeout.
//
// Mosaic: boot (`_InitialRouteResolver`, main.dart) reads
// `SettingsService.lastWatchedChannelId` to decide whether to jump straight to
// the last watched channel — and every successful channel switch writes that
// value via `recordWatched`. Since `shared_preferences` here is the REAL file
// of the user (not mockable in `integration_test -d linux`), without clearing
// between the tests of this suite the boot of one test would inherit the
// channel left by the previous test instead of always landing on the lowest
// number — every `waitForChannelNumber(tester, 1)` below assumes a clean
// boot. `setUp` below restores that isolation.
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

  group('channel navigation (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    // Non-sequential numbers on purpose (1/5/9): they prove that the jump by
    // number/arrow looks up by `number`, not by creation index/order.
    const channelNumbers = [1, 5, 9];
    late Map<int, String> channelNames;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();

      channelNames = {
        for (final n in channelNumbers) n: 'Channel $n',
      };

      for (final number in channelNumbers) {
        final channelId = await backend.createChannel(number: number, name: channelNames[number]!);
        final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Channel source $number');
        // duration_ms much larger than the real video clip (8s) and than the
        // total expected time of the whole suite (multiple testWidgets in the
        // same group, each restarting the app from scratch against the SAME
        // "once" schedule_entry created a single time here) — it only needs to
        // stay "on air" for the whole window of the tests, never actually
        // finishing the clip.
        final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
          {
            'external_id': 'vid-$number-a',
            'kind': 'episode',
            'title': 'PROGRAM $number A',
            'duration_ms': 1800000,
          },
          {
            'external_id': 'vid-$number-b',
            'kind': 'other',
            'title': 'COMMERCIAL $number',
            'duration_ms': 1800000,
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
      // A `last_watched_channel_id` that matches no channel of this suite
      // keeps boot going straight to the `ChannelPlayerPage` (instead of
      // landing on the `MosaicPage` the way it would with no history at all —
      // correct behavior, but out of scope for this suite) and still falls
      // back to the existing fallback to the lowest-number channel (see
      // `_bootstrapChannels`), which is the same premise every
      // `waitForChannelNumber(tester, 1)` below assumes.
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-navigation');
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

    String? osdChannelNumberText(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-channel-number'));
      if (finder.evaluate().isEmpty) return null;
      return tester.widget<Text>(finder).data;
    }

    String? osdChannelNameText(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-channel-name'));
      if (finder.evaluate().isEmpty) return null;
      return tester.widget<Text>(finder).data;
    }

    bool osdVisible(WidgetTester tester) {
      final finder = find.ancestor(
        of: find.byKey(const Key('channel-osd')),
        matching: find.byType(AnimatedOpacity),
      );
      if (finder.evaluate().isEmpty) return false;
      return tester.widget<AnimatedOpacity>(finder).opacity == 1;
    }

    Future<void> waitForChannelNumber(
      WidgetTester tester,
      int expectedNumber, {
      Duration timeout = const Duration(seconds: 15),
    }) async {
      await pumpUntil(
        tester,
        () => osdChannelNumberText(tester) == '$expectedNumber',
        timeout: timeout,
        failMessage:
            'expected OSD showing channel $expectedNumber, but it stayed on "${osdChannelNumberText(tester)}" '
            '(debug-status: ${_debugStatusText(tester)})',
      );
    }

    testWidgets('boot: loads channels and plays the lowest-number one', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 1);
      expect(osdChannelNameText(tester), channelNames[1]!.toUpperCase());
    });

    testWidgets('arrow up switches to the next channel by number', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 1);

      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump();
      await waitForChannelNumber(tester, 5);
      expect(osdChannelNameText(tester), channelNames[5]!.toUpperCase());

      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump();
      await waitForChannelNumber(tester, 9);
      expect(osdChannelNameText(tester), channelNames[9]!.toUpperCase());
    });

    testWidgets('arrow down switches back and wraps around the list', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 1);

      // From the lowest number (1), arrow down wraps around to the highest (9).
      await pressKey(tester, LogicalKeyboardKey.arrowDown);
      await tester.pump();
      await waitForChannelNumber(tester, 9);

      await pressKey(tester, LogicalKeyboardKey.arrowDown);
      await tester.pump();
      await waitForChannelNumber(tester, 5);
    });

    testWidgets('typing an existing channel number switches channel', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 1);

      // "5" followed by "9": does it hit channelInputMaxDigits (3)? No — only
      // 2 digits, so it would have to wait for the 1.5s debounce
      // (channelInputCommitDelay) to resolve "59" — which doesn't exist.
      // Instead, type only "5" and wait for the debounce to commit it as
      // channel 5.
      await pressKey(tester, LogicalKeyboardKey.digit5);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('channel-input-overlay')).evaluate().isNotEmpty,
        failMessage: 'digit overlay did not appear after typing "5"',
      );

      await waitForChannelNumber(tester, 5, timeout: const Duration(seconds: 5));
      expect(osdChannelNameText(tester), channelNames[5]!.toUpperCase());
    });

    testWidgets('typing a nonexistent channel number shows an error and does not switch channel', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 1);

      // "9" "9" "9": 3 digits hits channelInputMaxDigits, commits immediately
      // without waiting for the debounce. No configured channel has number
      // 999.
      await pressKey(tester, LogicalKeyboardKey.digit9);
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.digit9);
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.digit9);
      await tester.pump();

      await pumpUntil(
        tester,
        () => find.byKey(const Key('error-banner')).evaluate().isNotEmpty,
        failMessage: 'error banner did not appear after typing nonexistent channel "999"',
      );
      final errorText = tester.widget<Text>(
        find.descendant(of: find.byKey(const Key('error-banner')), matching: find.byType(Text)),
      );
      expect(errorText.data, contains('999'));
      expect(errorText.data, contains('not found'));

      // Still on channel 1 — did not switch.
      expect(osdChannelNumberText(tester), '1');
    });

    testWidgets('Enter shows the OSD, and another Enter (with the OSD visible) opens the EPG', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 1);

      // Boot already shows the OSD automatically (_showOsd in _startChannel) —
      // wait for the auto-hide (~5s, see _showOsd in main.dart) to test the
      // first stage ("Enter with the OSD hidden shows the OSD")
      // unambiguously, without confusing it with the boot auto-show.
      await pumpUntil(
        tester,
        () => !osdVisible(tester),
        timeout: const Duration(seconds: 8),
        failMessage: 'OSD never hid on its own after the boot auto-show',
      );

      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Enter (OSD hidden) did not show the OSD');

      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(
        tester,
        // 'epg-overlay' is the key of the EpgOverlay widget's root — its
        // content may still be loading at this point, but the root already
        // exists from the 1st frame.
        () => find.byKey(const Key('epg-overlay')).evaluate().isNotEmpty,
        failMessage: 'second Enter (OSD already visible) did not open the EPG',
      );

      await pressKey(tester, LogicalKeyboardKey.escape);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-overlay')).evaluate().isEmpty,
        failMessage: 'Esc (back action, default binding) did not close the EPG',
      );
    });
  });
}

String _debugStatusText(WidgetTester tester) {
  final finder = find.byKey(const Key('debug-status'));
  if (finder.evaluate().isEmpty) return '(no debug-status on screen)';
  return tester.widget<Text>(finder).data ?? '';
}
