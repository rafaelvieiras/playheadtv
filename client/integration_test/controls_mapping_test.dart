// E2E suite for "Remap controls" — same pattern as
// mosaic_test.dart/channel_navigation_test.dart (read those files first):
// boots the real app against a real Go backend (BackendFixture) and a fake
// video server (VideoFixture), drives tap/keyboard navigation through the
// real `integration_test` binding and never uses `tester.pumpAndSettle()` —
// same reason as always (the OSD ticker never settles).
//
// SharedPreferences isolation: unlike the other suites, this `setUp` clears
// the prefs WITHOUT writing a fake `last_watched_channel_id` — the test needs
// boot to land on the MosaicPage on purpose, to navigate from there to
// Settings -> Remap controls.
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

  group('remap controls (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    // 2 channels: lowest number (2, becomes the featured tile in the mosaic
    // with no history) and highest (6, the deterministic "next" when switching
    // channel).
    const channelNumbers = [2, 6];
    late Map<int, String> channelIds;
    late Map<int, String> channelNames;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();

      channelIds = {};
      channelNames = {
        for (final n in channelNumbers) n: 'Controls Channel $n',
      };

      for (final number in channelNumbers) {
        final channelId = await backend.createChannel(number: number, name: channelNames[number]!);
        channelIds[number] = channelId;
        final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Controls source $number');
        final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
          {
            'external_id': 'controles-$number-a',
            'kind': 'episode',
            'title': 'CONTROLS PROGRAM $number A',
            'duration_ms': 1800000,
          },
          {
            'external_id': 'controles-$number-b',
            'kind': 'other',
            'title': 'CONTROLS COMMERCIAL $number',
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
      // No `last_watched_channel_id` on purpose: boot needs to land on the
      // MosaicPage so this test can navigate from there to Settings.
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
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

    Future<void> waitForChannelNumber(
      WidgetTester tester,
      int expectedNumber, {
      Duration timeout = const Duration(seconds: 15),
    }) async {
      await pumpUntil(
        tester,
        () => osdChannelNumberText(tester) == '$expectedNumber',
        timeout: timeout,
        failMessage: 'expected OSD showing channel $expectedNumber, but it stayed on "${osdChannelNumberText(tester)}"',
      );
    }

    String? bindingLabelText(WidgetTester tester, String actionName) {
      final finder = find.byKey(Key('binding-label-$actionName'));
      if (finder.evaluate().isEmpty) return null;
      return tester.widget<Text>(finder).data;
    }

    // Navigates from boot (mosaic) to the Remap controls screen, remapping
    // channelNext to arrowRight — the common core of the two tests below.
    Future<void> navigateToControlsMappingAndRemapChannelNext(WidgetTester tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(
        tester,
        () => find.byKey(const Key('mosaic-page')).evaluate().isNotEmpty,
        failMessage: 'mosaic did not appear on boot with no history',
      );

      // MosaicPage has no tap — only arrow/Enter navigation (see
      // mosaic_page.dart). Grid with this fixture (2 channels): the featured
      // block in the centre, channel 6 and Settings in the top row (columns 0
      // and 1). Up from the featured block reaches the nearest tile above it,
      // the Settings tile; activate with Enter.
      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty,
        failMessage: 'Settings screen did not open from the Settings tile',
      );

      await tester.tap(find.byKey(const Key('settings-controls-tile')));
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('controls-mapping-page')).evaluate().isNotEmpty,
        failMessage: 'Remap controls screen did not open from the Settings tile',
      );

      await tester.tap(find.byKey(const Key('remap-button-channelNext')));
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.arrowRight);
      await tester.pump();
      await pumpUntil(
        tester,
        () => (bindingLabelText(tester, 'channelNext') ?? '').contains('Arrow Right'),
        failMessage:
            'channelNext label did not update to Arrow Right after the remap (stayed on '
            '"${bindingLabelText(tester, 'channelNext')}")',
      );
    }

    testWidgets('remapping an action works and is honored on the player screen', (tester) async {
      await navigateToControlsMappingAndRemapChannelNext(tester);

      // Go back 2x (Remap controls -> Settings -> Mosaic). Wait for each route
      // transition to really finish (previous screen unmounted, not just the
      // new one mounted) before the next pageBack — while the pop animation
      // is still in flight, the two AppBars (the outgoing one and the one
      // underneath) coexist in the tree and each has its own "Back" button,
      // which would make the next `pageBack()` fail by finding more than
      // one.
      await tester.pageBack();
      await tester.pump();
      await pumpUntil(
        tester,
        () =>
            find.byKey(const Key('settings-page')).evaluate().isNotEmpty &&
            find.byKey(const Key('controls-mapping-page')).evaluate().isEmpty,
        failMessage: 'going back from the Remap controls screen did not return to Settings',
      );

      await tester.pageBack();
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('mosaic-page')).evaluate().isNotEmpty && find.byKey(const Key('settings-page')).evaluate().isEmpty,
        failMessage: 'going back from the Settings screen did not return to the mosaic',
      );

      // Featured tile = channel 2 (lowest number, no history). The MosaicPage
      // is the same instance as before (only push/pop, never recreated) — the
      // focus is still on the Settings tile (top row) from when we navigated
      // there; go down onto the featured block and activate with Enter.
      final featuredTileKey = Key('mosaic-tile-${channelIds[2]}');
      await pumpUntil(
        tester,
        () => find.byKey(featuredTileKey).evaluate().isNotEmpty,
        failMessage: 'featured tile (channel 2) did not appear in the mosaic',
      );
      await pressKey(tester, LogicalKeyboardKey.arrowDown);
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await waitForChannelNumber(tester, 2);

      // The OLD binding of channelNext (arrowUp) no longer does anything — it
      // isn't associated with any InputAction after the remap.
      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump(const Duration(seconds: 1));
      expect(osdChannelNumberText(tester), '2');

      // The NEW binding (arrowRight) switches channel normally.
      await pressKey(tester, LogicalKeyboardKey.arrowRight);
      await tester.pump();
      await waitForChannelNumber(tester, 6);
    });

    testWidgets('Restore defaults reverts the remap without leaving the screen', (tester) async {
      await navigateToControlsMappingAndRemapChannelNext(tester);

      await tester.tap(find.byKey(const Key('restore-defaults-button')));
      await tester.pump();

      await pumpUntil(
        tester,
        () => (bindingLabelText(tester, 'channelNext') ?? '').contains('Arrow Up'),
        failMessage:
            'channelNext label did not go back to the default (Arrow Up) after Restore defaults (stayed on '
            '"${bindingLabelText(tester, 'channelNext')}")',
      );
    });

    // Path 2 accelerators (cycleAudioTrack/cycleSubtitleTrack — see
    // input/input_action.dart and AGENTS.md's "6-button baseline"): neither
    // has a default binding, so they must still be listed (as "Not set") and
    // remappable like every other action.
    testWidgets('the optional track accelerators are listed unbound and can be assigned a key', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(
        tester,
        () => find.byKey(const Key('mosaic-page')).evaluate().isNotEmpty,
        failMessage: 'mosaic did not appear on boot with no history',
      );

      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty,
        failMessage: 'Settings screen did not open from the Settings tile',
      );

      await tester.tap(find.byKey(const Key('settings-controls-tile')));
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('controls-mapping-page')).evaluate().isNotEmpty,
        failMessage: 'Remap controls screen did not open from the Settings tile',
      );

      // Both accelerators are listed, with no default binding. Scoped to
      // THIS page's ListView, not a bare find.byType(ListView) — the
      // Settings page underneath (also a ListView, see settings_page.dart)
      // is still in the tree right after the push (this suite never calls
      // pumpAndSettle, see the file-level doc comment), so an unscoped
      // finder matches 2 widgets and dragUntilVisible throws. Two separate
      // dragUntilVisible calls, one per action — it's a lazy sliver, so
      // scrolling cycleAudioTrack into view doesn't necessarily also build
      // cycleSubtitleTrack's tile (the next one down).
      final listFinder =
          find.descendant(of: find.byKey(const Key('controls-mapping-page')), matching: find.byType(ListView));
      await tester.dragUntilVisible(
        find.byKey(const Key('remap-button-cycleAudioTrack')),
        listFinder,
        const Offset(0, -200),
      );
      expect(bindingLabelText(tester, 'cycleAudioTrack'), 'Not set');

      await tester.dragUntilVisible(
        find.byKey(const Key('remap-button-cycleSubtitleTrack')),
        listFinder,
        const Offset(0, -200),
      );
      expect(bindingLabelText(tester, 'cycleSubtitleTrack'), 'Not set');

      // Assign a key to cycleAudioTrack, same capture flow as any other action.
      await tester.tap(find.byKey(const Key('remap-button-cycleAudioTrack')));
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.keyA);
      await tester.pump();
      await pumpUntil(
        tester,
        () => (bindingLabelText(tester, 'cycleAudioTrack') ?? 'Not set') != 'Not set',
        failMessage: 'cycleAudioTrack label did not update after assigning a key (stayed on '
            '"${bindingLabelText(tester, 'cycleAudioTrack')}")',
      );
    });
  });
}
