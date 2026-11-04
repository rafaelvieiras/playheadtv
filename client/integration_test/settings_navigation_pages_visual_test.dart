// Keyboard-navigation regression test for the Settings sub-pages "Remap
// controls" and "Offline cache": held Down/Up (key auto-repeat) and Down on the
// last row / Up on the first row must keep primary focus on a row (or, going
// up from the first row, on the AppBar back button), and Back must leave from
// a focused row. Screenshots go to PLAYHEADTV_SHOT_DIR (else $OUT/shots, else a
// temp dir). See settings_navigation_visual_test.dart for Settings/Diagnostics.
//
// One real app boot.
import 'dart:io';
import 'dart:ui' as ui;

import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:path_provider/path_provider.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

void out(String line) {
  // ignore: avoid_print
  print('[nav2] $line');
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('Sub-page keyboard navigation (visual)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;
    late Directory shotDir;
    final violations = <String>[];
    var shotCounter = 100;

    setUpAll(() async {
      final env = Platform.environment;
      final configured = env['PLAYHEADTV_SHOT_DIR'];
      final fromRunner = env['OUT'] != null ? '${env['OUT']}/shots' : null;
      shotDir = configured != null
          ? Directory(configured)
          : fromRunner != null
              ? Directory(fromRunner)
              // On a device `systemTemp` lives inside the app sandbox and dies
              // with the process — exactly when the screenshots matter. The
              // support directory is `files/`, which the host can read while the
              // app still runs (`adb shell run-as <package> cat files/…`).
              : Directory('${(await getApplicationSupportDirectory()).path}/lab-shots');
      shotDir.createSync(recursive: true);
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
      await seedChannel(backend, number: 93, prefix: 'NAVC', items: 4, itemMs: 60000);
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    Future<void> tap(WidgetTester tester, LogicalKeyboardKey key) async {
      await pressKey(tester, key);
      await tester.pump(const Duration(milliseconds: 400));
    }

    Future<void> hold(WidgetTester tester, LogicalKeyboardKey key, {int ms = 2500}) async {
      await ensurePlayerFocus(tester);
      await tester.sendKeyDownEvent(key);
      await tester.pump(const Duration(milliseconds: 500));
      final stop = DateTime.now().add(Duration(milliseconds: ms));
      while (DateTime.now().isBefore(stop)) {
        await tester.sendKeyRepeatEvent(key);
        await tester.pump(const Duration(milliseconds: 35));
      }
      await tester.sendKeyUpEvent(key);
      await tester.pump(const Duration(milliseconds: 500));
    }

    Future<void> shot(WidgetTester tester, String label, String pageKey) async {
      final name = '${(++shotCounter).toString().padLeft(3, '0')}_$label.png';
      await tester.pump(const Duration(milliseconds: 240));
      RenderObject? ro = find.byKey(Key(pageKey)).evaluate().first.renderObject;
      RenderRepaintBoundary? found;
      while (ro != null) {
        if (ro is RenderRepaintBoundary) found = ro;
        ro = ro.parent;
      }
      await tester.runAsync(() async {
        final image = await found!.toImage(pixelRatio: 1.0);
        final png = await image.toByteData(format: ui.ImageByteFormat.png);
        File('${shotDir.path}/$name').writeAsBytesSync(png!.buffer.asUint8List());
      });
    }

    // The key of the row (a ValueKey<String> starting with [prefix] or listed
    // in [exact]) that holds primary focus, 'back-button', or null.
    String? where(String prefix, Set<String> exact) {
      final primary = FocusManager.instance.primaryFocus;
      final ctx = primary?.context;
      if (ctx == null || primary is FocusScopeNode) return null;
      String? found;
      var back = false;
      void visit(Element e) {
        final k = e.widget.key;
        if (found == null && k is ValueKey<String> && (exact.contains(k.value) || (prefix.isNotEmpty && k.value.startsWith(prefix)))) {
          found = k.value;
        }
        if (e.widget is BackButton || (k is ValueKey<String> && k.value == 'restore-defaults-button')) back = true;
      }

      if (ctx is Element) visit(ctx);
      ctx.visitAncestorElements((e) {
        visit(e);
        return true;
      });
      return found ?? (back ? 'app-bar-action' : null);
    }

    void expectOn(String step, String? actual, String expected) {
      out('$step -> $actual');
      if (actual != expected) violations.add('$step: focus on $actual, expected $expected');
    }

    void expectRow(String step, String? actual) {
      out('$step -> $actual');
      if (actual == null) violations.add('$step: focus is not on a row');
    }

    testWidgets('Remap controls and Offline cache keep focus on a row at both ends', (tester) async {
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
        cacheSyncInterval: const Duration(minutes: 5),
      );
      FocusManager.instance.highlightStrategy = FocusHighlightStrategy.alwaysTraditional;
      addTearDown(() => FocusManager.instance.highlightStrategy = FocusHighlightStrategy.automatic);
      tester.view.devicePixelRatio = 1.0;
      tester.view.physicalSize = const Size(1920, 1080);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-tile-settings')).evaluate().isNotEmpty, failMessage: 'no mosaic');
      await tap(tester, LogicalKeyboardKey.arrowUp);
      await tap(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty, failMessage: 'Settings did not open');

      // ---- Remap controls ----------------------------------------------------
      await tap(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('controls-mapping-page')).evaluate().isNotEmpty, failMessage: 'Remap controls did not open');
      await tester.pump(const Duration(seconds: 1));
      const cp = 'controls-mapping-page';
      const cFirst = 'remap-button-channelPrevious';
      const cLast = 'remap-button-cycleSubtitleTrack';
      expectOn('controls open', where('remap-button-', {}), cFirst);
      await shot(tester, 'controls_open', cp);
      await hold(tester, LogicalKeyboardKey.arrowDown, ms: 4000);
      expectOn('controls held Down', where('remap-button-', {}), cLast);
      await shot(tester, 'controls_held_down_last_row', cp);
      await tap(tester, LogicalKeyboardKey.arrowDown);
      expectOn('controls Down past the end', where('remap-button-', {}), cLast);
      await shot(tester, 'controls_down_past_end', cp);
      await tap(tester, LogicalKeyboardKey.arrowUp);
      expectRow('controls Up after the overshoot', where('remap-button-', {}));
      await hold(tester, LogicalKeyboardKey.arrowUp, ms: 4000);
      expectRow('controls held Up', where('remap-button-', {}));
      await shot(tester, 'controls_held_up', cp);
      await tap(tester, LogicalKeyboardKey.arrowUp);
      expectRow('controls Up on the first row', where('remap-button-', {}));
      if (where('remap-button-', {}) == 'app-bar-action') await tap(tester, LogicalKeyboardKey.arrowDown);
      // Back from a focused row leaves.
      await tap(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('controls-mapping-page')).evaluate().isEmpty, failMessage: 'Back did not leave Remap controls');
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty, failMessage: 'not back in Settings');

      // ---- Offline cache -----------------------------------------------------
      await tester.pump(const Duration(milliseconds: 600));
      await tap(tester, LogicalKeyboardKey.arrowDown); // Remap controls -> Offline cache
      await tap(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('offline-cache-page')).evaluate().isNotEmpty, failMessage: 'Offline cache did not open');
      await tester.pump(const Duration(seconds: 1));
      const op = 'offline-cache-page';
      const oRows = {'offline-exhausted-tile', 'offline-window-tile', 'offline-sync-tile', 'offline-clear-tile'};
      expectOn('offline open', where('', oRows), 'offline-exhausted-tile');
      await shot(tester, 'offline_open', op);
      await hold(tester, LogicalKeyboardKey.arrowDown);
      expectOn('offline held Down', where('', oRows), 'offline-clear-tile');
      await shot(tester, 'offline_last_row', op);
      await tap(tester, LogicalKeyboardKey.arrowDown);
      expectOn('offline Down past the end', where('', oRows), 'offline-clear-tile');
      await shot(tester, 'offline_down_past_end', op);
      await hold(tester, LogicalKeyboardKey.arrowUp);
      expectRow('offline held Up', where('', oRows));
      await tap(tester, LogicalKeyboardKey.arrowUp);
      expectRow('offline Up on the first row', where('', oRows));
      if (where('', oRows) == 'app-bar-action') await tap(tester, LogicalKeyboardKey.arrowDown);
      await tap(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('offline-cache-page')).evaluate().isEmpty, failMessage: 'Back did not leave Offline cache');

      out('violations: ${violations.length}');
      for (final v in violations) {
        out('VIOLATION $v');
      }
      expect(violations, isEmpty, reason: 'sub-page navigation lost focus (screenshots in ${shotDir.path})');
    });
  });
}
