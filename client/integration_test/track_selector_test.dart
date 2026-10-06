// E2E suite for the tier 1 track selector (issue #3, tier 1 — see
// AGENTS.md's "6-button baseline" and
// services/stream_selection_resolver.dart's TierOneOverride): same pattern
// as channel_navigation_test.dart/epg_overlay_test.dart (read those first):
// boots the real app against a real Go backend (BackendFixture) and a fake
// video server (VideoFixture, playing the role of "Jellyfin"), drives
// keyboard navigation through the real `integration_test` binding and never
// uses `tester.pumpAndSettle()` (the OSD's 500ms ticker never settles).
//
// VideoFixture's clip carries 2 real audio tracks tagged "eng"/"por" (no
// real subtitle track) — the picker/accelerator both read the ACTUAL track
// list via getMediaInfo(), so a fixture with a single, indistinguishable
// track couldn't exercise real navigation between options.
//
// Split across two files (this one + track_selector_persistence_test.dart)
// on purpose — see AGENTS.md's mdk/GL-texture-under-load gotcha. Booting
// several real PlayheadSpikeApp instances (real dual crossfade
// VideoPlayerControllers, real GL textures) back-to-back in the SAME process
// — which is what `flutter test` does for every testWidgets in one file —
// eventually stalls the next boot's rendering pipeline indefinitely, timing-
// dependent rather than tied to any specific action (reproduced during this
// slice's validation at a different boot count each run: the 3rd, 4th and
// 5th real boot in one process each stalled at least once with this file's
// heavier 2-track fixture). Keeping each file to 2-3 real boots keeps it
// under that threshold reliably; don't merge these two files back into one
// without re-verifying against that gotcha first.
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

  group('tier 1 track selector (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String channelId;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start(audioTracks: 2);
      await backend.start();

      channelId = await backend.createChannel(number: 4, name: 'Track Channel 4');
      final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Track source 4');
      final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
        {'external_id': 'track-4-a', 'kind': 'episode', 'title': 'TRACK PROGRAM 4 A', 'duration_ms': 1800000},
        {'external_id': 'track-4-b', 'kind': 'other', 'title': 'TRACK COMMERCIAL 4', 'duration_ms': 1800000},
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
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-tracks');
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
        failMessage: 'expected OSD showing channel $expectedNumber, but it stayed on "${osdChannelNumberText(tester)}"',
      );
    }

    // Ensures the OSD is visible in focus mode (the 6-button path to the
    // audio/CC buttons, see AGENTS.md's "6-button baseline"): waits for the
    // boot auto-show to hide first, then presses Enter once (OSD hidden ->
    // show OSD with the first control focused), same building block as
    // epg_overlay_test.dart's openEpg helper.
    Future<void> showOsd(WidgetTester tester) async {
      await pumpUntil(
        tester,
        () => !osdVisible(tester),
        timeout: const Duration(seconds: 8),
        failMessage: 'OSD never hid on its own after the boot auto-show',
      );
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Enter (OSD hidden) did not show the OSD');
    }

    Future<void> openTrackSelector(WidgetTester tester) async {
      await showOsd(tester);
      await pressKey(tester, LogicalKeyboardKey.arrowRight); // first arrow after Select enters OSD focus mode (audio button)
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.enter); // activate the focused audio button
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('track-selector-overlay')).evaluate().isNotEmpty,
        failMessage: 'Select on the OSD audio button did not open the track selector',
      );
      // Both real audio tracks (eng/por) must have loaded before any
      // navigation/selection is sent.
      await pumpUntil(
        tester,
        () => find.byKey(const Key('track-option-audio-1')).evaluate().isNotEmpty,
        failMessage: 'track selector never showed the 2nd audio track (track-option-audio-1)',
      );
    }

    testWidgets('Select on the OSD audio button opens the track selector, listing the real audio tracks', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 4);

      await openTrackSelector(tester);

      expect(find.byKey(const Key('track-option-audio-0')), findsOneWidget);
      expect(find.byKey(const Key('track-option-audio-1')), findsOneWidget);
      // The audio button opens an audio-only picker: no subtitle panel.
      expect(find.byKey(const Key('track-option-subtitle--1')), findsNothing);
    });

    testWidgets('arrowUp/arrowDown move focus within the panel and do not switch channel while the picker is open', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 4);

      await openTrackSelector(tester);

      bool focused(String key) {
        final decoration = tester.widget<Container>(find.byKey(Key(key))).decoration as BoxDecoration;
        return decoration.color == Colors.white24;
      }

      // Raw Up/Down are captured by the picker instead of switching channel:
      // Down focuses the next option, Up the previous one.
      expect(focused('track-option-audio-0'), isTrue);
      await pressKey(tester, LogicalKeyboardKey.arrowDown);
      await tester.pump();
      expect(focused('track-option-audio-1'), isTrue, reason: 'Down must focus the next option');
      expect(osdChannelNumberText(tester), '4', reason: 'arrowDown must not switch channel while the picker is open');
      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      await tester.pump();
      expect(focused('track-option-audio-0'), isTrue, reason: 'Up must focus the previous option');
      expect(find.byKey(const Key('track-selector-overlay')), findsOneWidget, reason: 'picker should still be open');
      expect(osdChannelNumberText(tester), '4', reason: 'arrowUp must not switch channel while the picker is open');
    });

    testWidgets('Back closes the picker without applying or persisting anything', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 4);

      await openTrackSelector(tester);

      await pressKey(tester, LogicalKeyboardKey.escape);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('track-selector-overlay')).evaluate().isEmpty,
        failMessage: 'Esc (back) did not close the track selector',
      );
      expect(find.byKey(const Key('track-toast')), findsNothing);
      expect(osdChannelNumberText(tester), '4');

      final prefs = await SharedPreferences.getInstance();
      expect(prefs.getInt('track_override_audio_$channelId'), isNull);
    });
  });
}
