// E2E suite for the tier 1 track selector's persistence (issue #3, tier 1 —
// see AGENTS.md's "6-button baseline" and
// services/stream_selection_resolver.dart's TierOneOverride): companion to
// track_selector_test.dart (read its file-level doc comment first for why
// this is a separate file, not just for topic — a real per-process
// resource/timing constraint with the real mdk decoder under repeated real
// app boots, not a stylistic split). Same fixtures/helpers duplicated here
// rather than shared, matching this suite's existing convention (see
// channel_navigation_test.dart/epg_overlay_test.dart — no shared test
// harness module exists yet).
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

  group('tier 1 track selector persistence (real e2e)', () {
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

      // A 2nd channel purely so "a real channel switch after a live track
      // switch" (the regression check in "Select applies...") has somewhere
      // to switch to.
      final channelId11 = await backend.createChannel(number: 11, name: 'Track Channel 11');
      final mediaSourceId11 = await backend.createLocalFolderMediaSource(name: 'Track source 11');
      final catalogItemIds11 = await backend.syncCatalogItems(mediaSourceId11, [
        {'external_id': 'track-11-a', 'kind': 'episode', 'title': 'TRACK PROGRAM 11 A', 'duration_ms': 1800000},
      ]);
      final blockId11 = await backend.createBlockWithItems(catalogItemIds11);
      await backend.createScheduleOnce(
        channelId: channelId11,
        blockId: blockId11,
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

    Future<void> showOsd(WidgetTester tester) async {
      await pumpUntil(
        tester,
        () => !osdVisible(tester),
        timeout: const Duration(seconds: 8),
        failMessage: 'OSD never hid on its own after the boot auto-show',
      );
      await tester.sendKeyEvent(LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Enter (OSD hidden) did not show the OSD');
    }

    Future<void> openTrackSelector(WidgetTester tester) async {
      await showOsd(tester);
      await tester.sendKeyEvent(LogicalKeyboardKey.enter); // OSD focus mode starts on the audio button
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('track-selector-overlay')).evaluate().isNotEmpty,
        failMessage: 'Select on the OSD audio button did not open the track selector',
      );
      await pumpUntil(
        tester,
        () => find.byKey(const Key('track-option-audio-1')).evaluate().isNotEmpty,
        failMessage: 'track selector never showed the 2nd audio track (track-option-audio-1)',
      );
    }

    testWidgets('Select applies the focused audio track live and persists it, keyed by channel', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 4);

      await openTrackSelector(tester);

      // Default focus is the 1st audio option (index 0, "eng"). Move down
      // once (channelNext/arrowUp -> moveFocus(1)) to focus the 2nd ("por").
      await tester.sendKeyEvent(LogicalKeyboardKey.arrowUp);
      await tester.pump();
      await tester.sendKeyEvent(LogicalKeyboardKey.enter);
      await tester.pump();

      await pumpUntil(
        tester,
        () => find.byKey(const Key('track-selector-overlay')).evaluate().isEmpty,
        failMessage: 'Select did not close the picker',
      );
      await pumpUntil(
        tester,
        () => find.byKey(const Key('track-toast')).evaluate().isNotEmpty,
        failMessage: 'no confirmation toast appeared after Select',
      );
      final toastText = tester.widget<Text>(
        find.descendant(of: find.byKey(const Key('track-toast')), matching: find.byType(Text)),
      );
      expect(toastText.data, contains('por'));

      // Persisted client-local only, keyed by this channel's id — never sent
      // to the backend (SettingsService.setAudioTrackOverride).
      final prefs = await SharedPreferences.getInstance();
      expect(prefs.getInt('track_override_audio_$channelId'), 1);

      // Regression check: a real channel switch (a fresh
      // VideoPlayerController via crossfade), right after a live
      // setAudioTracks call, in the SAME app session, must not stall — this
      // is the production scenario the file-split gotcha (see this file's
      // and track_selector_test.dart's doc comments) explicitly confirmed
      // does NOT happen for a real user; only repeated fresh *app* boots in
      // one test process do.
      await tester.sendKeyEvent(LogicalKeyboardKey.arrowUp);
      await tester.pump();
      await waitForChannelNumber(tester, 11, timeout: const Duration(seconds: 20));
    });

    testWidgets('revisiting the channel later shows the persisted choice pre-focused in the picker', (tester) async {
      // Simulates "a previous session already picked the 2nd audio track
      // (por) for this channel" by writing the same key
      // SettingsService.setAudioTrackOverride writes, directly, before boot.
      final prefs = await SharedPreferences.getInstance();
      await prefs.setInt('track_override_audio_$channelId', 1);

      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, 4);

      await openTrackSelector(tester);

      final focusedOption = tester.widget<Container>(find.byKey(const Key('track-option-audio-1')));
      final focusedDecoration = focusedOption.decoration as BoxDecoration;
      expect(
        focusedDecoration.color,
        Colors.white24,
        reason: 'the persisted override (audio index 1, "por") should be pre-focused on reopening the picker',
      );

      final otherOption = tester.widget<Container>(find.byKey(const Key('track-option-audio-0')));
      final otherDecoration = otherOption.decoration as BoxDecoration;
      expect(otherDecoration.color, Colors.transparent);
    });
  });
}
