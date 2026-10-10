// E2E: the software dimmer (issue #85).
// - the OSD's dimmer button toggles a black layer above everything (video,
//   OSD), at the saved level, with ONLY the six baseline buttons;
// - the level dialog is reachable with the six buttons from the OSD (the level
//   chip beside the button while dimmed) and from Settings; the level persists
//   across a restart while the toggle starts off;
// - holding Select on the button is only an accelerator to the same dialog.
//
// Three real app boots (budget: see AGENTS.md).
import 'package:client/dimmer.dart';
import 'package:client/main.dart';
import 'package:client/services/settings_service.dart';
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

  group('dimmer (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start(audioTracks: 2);
      await backend.start();

      final channelId = await backend.createChannel(number: 5, name: 'Dimmer Channel 5');
      final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Dimmer source 5');
      final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
        {'external_id': 'dim-5-a', 'kind': 'episode', 'title': 'DIM PROGRAM 5 A', 'duration_ms': 1800000},
        {'external_id': 'dim-5-b', 'kind': 'other', 'title': 'DIM COMMERCIAL 5', 'duration_ms': 1800000},
      ]);
      final blockId = await backend.createBlockWithItems(catalogItemIds);
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
      final deadline = DateTime.now().add(const Duration(seconds: 15));
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met in time');
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    Future<void> press(WidgetTester tester, LogicalKeyboardKey key) async {
      await pressKey(tester, key);
      await tester.pump();
    }

    bool osdVisible(WidgetTester tester) {
      final finder = find.ancestor(of: find.byKey(const Key('channel-osd')), matching: find.byType(AnimatedOpacity));
      return finder.evaluate().isNotEmpty && tester.widget<AnimatedOpacity>(finder).opacity == 1;
    }

    bool dimmed(WidgetTester tester) => find.byKey(const Key('dimmer-overlay')).evaluate().isNotEmpty;

    double dimAlpha(WidgetTester tester) => tester.widget<ColoredBox>(find.byKey(const Key('dimmer-overlay'))).color.a;

    Future<void> bootPlayer(WidgetTester tester, {int? savedLevel}) async {
      final prefs = await SharedPreferences.getInstance();
      final keep = savedLevel ?? prefs.getInt('dim_level_percent');
      await prefs.clear();
      if (keep != null) await prefs.setInt('dim_level_percent', keep);
      // Always lands on the player with the lowest-number channel.
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-dimmer');
      await configure();
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'OSD never auto-showed at boot');
      await pumpUntil(tester, () => !osdVisible(tester), failMessage: 'OSD never auto-hid');
      // The audio track button depends on media info loading.
    }

    // Select, then an arrow: the OSD in focus mode with the focus on the first
    // control; walks Right until [key] holds the focus (checked by pressing
    // nothing: the caller activates it).
    Future<void> enterOsdFocusMode(WidgetTester tester) async {
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Select did not show the OSD');
      // The fixture clip has 2 audio tracks (media info loads asynchronously):
      // the row is [audio, dimmer] (+ the level chip while dimmed).
      await pumpUntil(tester, () => find.byKey(const Key('osd-btn-audio')).evaluate().isNotEmpty,
          failMessage: 'no audio button in the OSD');
      expect(find.byKey(const Key('osd-btn-dimmer')), findsOneWidget);
      // First arrow: enters focus mode (focus on audio) and is applied as the
      // first move (Left: stays); then Right reaches the dimmer button.
      await press(tester, LogicalKeyboardKey.arrowLeft);
      await press(tester, LogicalKeyboardKey.arrowRight);
    }

    testWidgets('six buttons: Select toggles the dim, the level chip opens the level dialog, Back cancels', (tester) async {
      await bootPlayer(tester, savedLevel: 50);
      expect(dimmed(tester), isFalse, reason: 'the dimmer starts off');

      // OSD focus mode, the dimmer button holds the focus -> Select.
      await enterOsdFocusMode(tester);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => dimmed(tester), failMessage: 'Select on the dimmer button did not dim');
      expect(dimAlpha(tester), closeTo(0.5, 0.01), reason: '50% by default');
      // The layer covers the whole page, above the (still shown) OSD.
      expect(tester.getSize(find.byKey(const Key('dimmer-overlay'))), tester.view.physicalSize / tester.view.devicePixelRatio);
      expect(osdVisible(tester), isTrue);
      expect(find.byKey(const Key('osd-btn-dimmer-level')), findsOneWidget, reason: 'the level chip shows while dimmed');

      // Right -> the level chip -> Select opens the level dialog.
      await press(tester, LogicalKeyboardKey.arrowRight);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('dimmer-dialog')).evaluate().isNotEmpty, failMessage: 'the level dialog did not open');
      expect(tester.widget<Text>(find.byKey(const Key('dimmer-dialog-value'))).data, '50%');

      // Left x2 lowers it, live behind the dialog.
      await press(tester, LogicalKeyboardKey.arrowLeft);
      await press(tester, LogicalKeyboardKey.arrowLeft);
      expect(tester.widget<Text>(find.byKey(const Key('dimmer-dialog-value'))).data, '30%');
      expect(dimAlpha(tester), closeTo(0.3, 0.01));
      // Back cancels: the previous level returns.
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('dimmer-dialog')).evaluate().isEmpty, failMessage: 'Back did not close the dialog');
      expect(dimAlpha(tester), closeTo(0.5, 0.01));
      expect((await SettingsService.create()).dimLevelPercent, 50, reason: 'a cancelled dialog saves nothing');

      // Again, this time Select saves.
      await enterOsdFocusMode(tester);
      await press(tester, LogicalKeyboardKey.arrowRight); // the level chip
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('dimmer-dialog')).evaluate().isNotEmpty, failMessage: 'the level dialog did not open again');
      await press(tester, LogicalKeyboardKey.arrowLeft);
      await press(tester, LogicalKeyboardKey.arrowLeft);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('dimmer-dialog')).evaluate().isEmpty, failMessage: 'Select did not close the dialog');
      expect(dimAlpha(tester), closeTo(0.3, 0.01));
      expect((await SettingsService.create()).dimLevelPercent, 30, reason: 'stored for the next run');

      // Select on the dimmer button again removes the dim.
      await enterOsdFocusMode(tester);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => !dimmed(tester), failMessage: 'Select did not remove the dim');
    });

    testWidgets('restart: the level persists, the toggle starts off; holding Select is an accelerator', (tester) async {
      await bootPlayer(tester); // keeps the stored 30%
      expect(dimmed(tester), isFalse, reason: 'the toggle is not persisted');
      expect(Dimmer.level.value, 30, reason: 'the saved level is loaded');

      await enterOsdFocusMode(tester);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => dimmed(tester), failMessage: 'Select did not dim');
      expect(dimAlpha(tester), closeTo(0.3, 0.01), reason: 'the saved level applies');

      // Accelerator: hold Select on the button (still focused, the OSD still
      // shown) -> the same dialog.
      expect(osdVisible(tester), isTrue);
      await ensurePlayerFocus(tester);
      await tester.sendKeyDownEvent(LogicalKeyboardKey.enter);
      await tester.pump(dimmerHoldDuration + const Duration(milliseconds: 300));
      await pumpUntil(tester, () => find.byKey(const Key('dimmer-dialog')).evaluate().isNotEmpty, failMessage: 'holding Select did not open the dialog');
      await tester.sendKeyUpEvent(LogicalKeyboardKey.enter);
      await tester.pump();
      expect(find.byKey(const Key('dimmer-dialog')), findsOneWidget, reason: 'the release must not close it');
      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('dimmer-dialog')).evaluate().isEmpty, failMessage: 'Back did not close the dialog');
      expect(dimmed(tester), isTrue, reason: 'Back restores the state before the dialog (dimmed)');
    });

    testWidgets('Settings -> Dim level with six buttons; the mosaic is not dimmed', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear(); // no history: boot lands on the mosaic
      await prefs.setInt('dim_level_percent', 30);
      await configure();
      Dimmer.active.value = true; // even if it were on, the mosaic has no layer
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-tile-settings')).evaluate().isNotEmpty, failMessage: 'no Settings tile in the mosaic');
      expect(dimmed(tester), isFalse, reason: 'the mosaic is not dimmed');

      await press(tester, LogicalKeyboardKey.arrowUp);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty, failMessage: 'Settings did not open');

      bool tileFocused(String key) {
        var found = false;
        FocusManager.instance.primaryFocus?.context?.visitAncestorElements((a) {
          if (a.widget.key == Key(key)) {
            found = true;
            return false;
          }
          return true;
        });
        return found;
      }

      for (var i = 0; i < 10 && !tileFocused('settings-dim-level-tile'); i++) {
        await press(tester, LogicalKeyboardKey.arrowDown);
      }
      expect(tileFocused('settings-dim-level-tile'), isTrue, reason: 'Down never reached the Dim level row');
      String value() => tester.widget<Text>(find.byKey(const Key('settings-dim-level-value'))).data ?? '';
      expect(value(), '30%');

      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('dimmer-dialog')).evaluate().isNotEmpty, failMessage: 'the dialog did not open from Settings');
      await press(tester, LogicalKeyboardKey.arrowRight);
      await press(tester, LogicalKeyboardKey.arrowRight);
      await press(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('dimmer-dialog')).evaluate().isEmpty, failMessage: 'Select did not close the dialog');
      await pumpUntil(tester, () => value() == '50%', failMessage: 'the row did not show 50%: ${value()}');
      expect((await SettingsService.create()).dimLevelPercent, 50);

      await press(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isEmpty, failMessage: 'Back did not leave Settings');
    });
  });
}
