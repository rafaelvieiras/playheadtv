// Visual keyboard-navigation regression test for the Settings screen tree.
//
// Desktop only: it resizes the window at will (800x450 at 200% interface size,
// 2560x1440, 1280x720) and asserts about the layout at each size. A phone or a TV
// box has one window — the screen — so the harness resizes a frame the device can
// never have, focus wanders in the scaled-down copy and the run says nothing
// about the product. `run_android_suites.sh` skips it for that reason.
//
// Drives Settings (and Settings -> Diagnostics, and the player's debug overlay)
// with real key events, including HELD keys (key down, repeated key-repeat
// events every ~35 ms, key up) as a keyboard's auto-repeat produces, at several
// window sizes and "Interface size" choices. After every step it
//   * saves a PNG screenshot (directory: PLAYHEADTV_SHOT_DIR, else
//     $OUT/shots when run by run_gui_suites.sh, else a temp dir),
//   * prints which row holds primary focus, and
//   * asserts that primary focus is a row of the current page and that the
//     focused row is fully inside the scrollable's viewport.
// Violations are collected and reported together at the end so that one lost
// focus does not hide the rest of the run.
//
// One real app boot.
import 'dart:io';
import 'dart:ui' as ui;

import 'package:client/log/log.dart';
import 'package:client/main.dart';
import 'package:client/ui/ui_scale.dart';
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

const _settingsRows = [
  'settings-connection-tile',
  'settings-server-tile',
  'settings-controls-tile',
  'settings-offline-tile',
  'settings-plugins-tile',
  'settings-fullscreen-tile',
  'settings-ui-scale-tile',
  'settings-preload-lead-tile',
  'settings-dim-level-tile',
  // The always-on clock's own row is always there; its opacity and size rows
  // only appear once the clock is on, and this suite clears the preferences.
  'settings-clock-tile',
  'settings-epg-thumbnail-tile',
  'settings-log-level-tile',
  'settings-diagnostics-tile',
];

const _diagnosticsRows = [
  'diagnostics-path-tile',
  'diagnostics-level-tile',
  'diagnostics-bundle-tile',
];

void out(String line) {
  // ignore: avoid_print
  print('[nav] $line');
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('Settings keyboard navigation (visual)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;
    late Directory shotDir;
    final violations = <String>[];
    var shotCounter = 0;

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
      out('screenshots: ${shotDir.path}');
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
      await seedChannel(backend, number: 91, prefix: 'NAVA', items: 4, itemMs: 60000);
      await seedChannel(backend, number: 92, prefix: 'NAVB', items: 4, itemMs: 60000);
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    Future<void> tap(WidgetTester tester, LogicalKeyboardKey key) async {
      await pressKey(tester, key);
      await tester.pump(const Duration(milliseconds: 400));
    }

    // Auto-repeat: down, then repeat events about every 35 ms, then up.
    Future<void> hold(WidgetTester tester, LogicalKeyboardKey key, {int ms = 1300, int intervalMs = 35}) async {
      await ensurePlayerFocus(tester);
      await tester.sendKeyDownEvent(key);
      await tester.pump(const Duration(milliseconds: 500)); // the OS repeat delay
      final stop = DateTime.now().add(Duration(milliseconds: ms));
      while (DateTime.now().isBefore(stop)) {
        await tester.sendKeyRepeatEvent(key);
        await tester.pump(Duration(milliseconds: intervalMs));
      }
      await tester.sendKeyUpEvent(key);
      await tester.pump(const Duration(milliseconds: 500));
    }

    RenderRepaintBoundary? outermostBoundary(WidgetTester tester, Finder from) {
      final matches = from.evaluate();
      if (matches.isEmpty) return null;
      RenderObject? ro = matches.first.renderObject;
      RenderRepaintBoundary? found;
      while (ro != null) {
        if (ro is RenderRepaintBoundary) found = ro;
        ro = ro.parent;
      }
      return found;
    }

    Future<void> screenshot(WidgetTester tester, String label, Finder pageFinder) async {
      shotCounter++;
      final name = '${shotCounter.toString().padLeft(2, '0')}_$label.png';
      // Two frames so the highlight of the focus change is painted.
      await tester.pump(const Duration(milliseconds: 120));
      await tester.pump(const Duration(milliseconds: 120));
      final boundary = outermostBoundary(tester, pageFinder);
      if (boundary == null) {
        violations.add('$name: no repaint boundary to capture');
        return;
      }
      String? problem;
      await tester.runAsync(() async {
        final image = await boundary.toImage(pixelRatio: 1.0);
        final raw = await image.toByteData(format: ui.ImageByteFormat.rawRgba);
        final png = await image.toByteData(format: ui.ImageByteFormat.png);
        if (raw == null || png == null) {
          problem = 'encode failed';
          return;
        }
        final colors = <int>{};
        for (var i = 0; i + 3 < raw.lengthInBytes; i += 4 * 997) {
          colors.add(raw.getUint32(i));
        }
        if (colors.length < 2) problem = 'screenshot is blank (${colors.length} distinct sampled colors)';
        File('${shotDir.path}/$name').writeAsBytesSync(png.buffer.asUint8List());
        out('shot $name ${image.width}x${image.height} colors=${colors.length}');
      });
      if (problem != null) violations.add('$name: $problem');
    }

    // The focus state, described as text, and checked against [rows].
    String checkFocus(WidgetTester tester, String step, String pageKey, List<String> rows) {
      final primary = FocusManager.instance.primaryFocus;
      if (primary == null) {
        violations.add('$step: primaryFocus is null');
        return 'null';
      }
      if (primary is FocusScopeNode) {
        violations.add('$step: primaryFocus is a scope ($primary), not a row');
        return 'scope:${primary.debugLabel}';
      }
      final ctx = primary.context;
      if (ctx == null) {
        violations.add('$step: primaryFocus has no context (detached): $primary');
        return 'detached';
      }
      var inPage = false;
      var onBackButton = false;
      String? rowKey;
      Element? rowElement;
      void visit(Element e) {
        final w = e.widget;
        if (w is BackButton || w is CloseButton) onBackButton = true;
        final k = w.key;
        if (k is ValueKey<String>) {
          if (k.value == pageKey) inPage = true;
          if (rowKey == null && rows.contains(k.value)) {
            rowKey = k.value;
            rowElement = e;
          }
        }
      }

      if (ctx is Element) visit(ctx);
      ctx.visitAncestorElements((e) {
        visit(e);
        return true;
      });
      final chain = <String>[];
      ctx.visitAncestorElements((e) {
        final k = e.widget.key;
        if (k is ValueKey<String> && chain.length < 3) chain.add(k.value);
        return true;
      });
      final types = <String>[];
      ctx.visitAncestorElements((e) {
        if (types.length < 7) types.add(e.widget.runtimeType.toString());
        return true;
      });
      final w0 = ctx.widget;
      final detail = 'rect=${primary.rect}, hasOnKeyEvent=${w0 is Focus ? w0.onKeyEvent != null : null}, '
          'parentNode=${primary.parent?.runtimeType}/${primary.parent?.debugLabel}, ancestorsUp=$types';
      final label = rowKey ??
          (onBackButton
              ? 'AppBar-back-button(in $pageKey=$inPage)'
              : 'other:${ctx.widget.runtimeType}[node=${primary.debugLabel}, keyedAncestors=$chain, $detail]');
      if (inPage && rowKey == null && onBackButton) {
        // The AppBar back button of the page is a legitimate focus target.
        out('$step: (info) focus is on the page\'s AppBar back button');
      } else if (!inPage) {
        violations.add('$step: primaryFocus ($label) is not inside page $pageKey');
      } else if (rowKey == null) {
        violations.add('$step: primaryFocus ($label) is not one of the page rows');
      } else {
        final box = rowElement!.renderObject as RenderBox;
        final rowRect = box.localToGlobal(Offset.zero) & box.size;
        final viewport = find.descendant(of: find.byKey(ValueKey(pageKey)), matching: find.byType(Viewport));
        if (viewport.evaluate().isNotEmpty) {
          final vb = viewport.evaluate().first.renderObject! as RenderBox;
          final vr = vb.localToGlobal(Offset.zero) & vb.size;
          const eps = 1.0;
          final inside = rowRect.top >= vr.top - eps && rowRect.bottom <= vr.bottom + eps;
          if (!inside) violations.add('$step: focused row $rowKey rect $rowRect is not fully inside viewport $vr');
        }
      }
      return label;
    }

    Future<void> step(WidgetTester tester, String label, String pageKey, List<String> rows) async {
      final focus = checkFocus(tester, label, pageKey, rows);
      final pages = [
        for (final k in ['mosaic-page', 'settings-page', 'diagnostics-page', 'offline-cache-page', 'controls-mapping-page'])
          if (find.byKey(Key(k)).evaluate().isNotEmpty) k
      ];
      final lastRow = find.byKey(Key(rows.last));
      final lastRect = lastRow.evaluate().isEmpty
          ? null
          : (lastRow.evaluate().first.renderObject! as RenderBox).localToGlobal(Offset.zero) &
              (lastRow.evaluate().first.renderObject! as RenderBox).size;
      final pageBox = find.byKey(ValueKey(pageKey)).evaluate().isEmpty
          ? null
          : find.byKey(ValueKey(pageKey)).evaluate().first.renderObject as RenderBox?;
      out('$label -> focus=$focus pagesInTree=$pages lastRowRect=$lastRect pageSize=${pageBox?.size}');
      await screenshot(tester, label, find.byKey(ValueKey(pageKey)));
    }

    // The row key holding primary focus, or null.
    String? focusedRow(List<String> rows) {
      final primary = FocusManager.instance.primaryFocus;
      final ctx = primary?.context;
      if (ctx == null || primary is FocusScopeNode) return null;
      String? found;
      void visit(Element e) {
        final k = e.widget.key;
        if (found == null && k is ValueKey<String> && rows.contains(k.value)) found = k.value;
      }

      if (ctx is Element) visit(ctx);
      ctx.visitAncestorElements((e) {
        visit(e);
        return true;
      });
      return found;
    }

    // Test-only recovery so one lost focus does not poison the later scenarios:
    // when primary focus is not on a row, request it on [rowKey] and say so.
    Future<void> recoverTo(WidgetTester tester, String pageKey, List<String> rows, String rowKey) async {
      final primary = FocusManager.instance.primaryFocus;
      var onRow = false;
      final ctx = primary?.context;
      if (ctx != null && primary is! FocusScopeNode) {
        void visit(Element e) {
          final k = e.widget.key;
          if (k is ValueKey<String> && rows.contains(k.value)) onRow = true;
        }

        if (ctx is Element) visit(ctx);
        ctx.visitAncestorElements((e) {
          visit(e);
          return true;
        });
      }
      // Up on the first row legitimately reaches the AppBar back button: come
      // back with a real Down (not a recovery), then continue from the row.
      var onBack = false;
      if (!onRow && ctx != null && primary is! FocusScopeNode) {
        if (ctx.widget is BackButton) onBack = true;
        ctx.visitAncestorElements((e) {
          if (e.widget is BackButton) onBack = true;
          return true;
        });
      }
      if (onBack) {
        await tap(tester, LogicalKeyboardKey.arrowDown);
        if (focusedRow(rows) != null) return recoverTo(tester, pageKey, rows, rowKey);
      }
      if (onRow) {
        // On a different row: walk to the target with real Up/Down taps.
        for (var i = 0; i < rows.length + 2; i++) {
          final current = focusedRow(rows);
          if (current == null || current == rowKey) break;
          final goDown = rows.indexOf(current) < rows.indexOf(rowKey);
          await tap(tester, goDown ? LogicalKeyboardKey.arrowDown : LogicalKeyboardKey.arrowUp);
        }
        if (focusedRow(rows) == rowKey) return;
        violations.add('could not walk to $rowKey with Up/Down (focus on ${focusedRow(rows)})');
        return;
      }
      violations.add('TEST RECOVERY needed on $pageKey (focus was not on a row)');
      out('TEST RECOVERY: focus was not on a row; requesting focus on $rowKey (the app did not do this)');
      if (find.byKey(Key(rowKey)).evaluate().isEmpty) {
        // A lazy row that is not built: scroll the list to the end or the start
        // first. A page laid out so that everything fits has no Scrollable at
        // all, and then there is nothing to scroll.
        final scrollables = find.descendant(of: find.byKey(ValueKey(pageKey)), matching: find.byType(Scrollable)).evaluate();
        if (scrollables.isNotEmpty) {
          final position = (scrollables.first as StatefulElement).state as ScrollableState;
          position.position.jumpTo(rowKey == rows.last ? position.position.maxScrollExtent : position.position.minScrollExtent);
          await tester.pump(const Duration(milliseconds: 400));
        }
      }
      if (find.byKey(Key(rowKey)).evaluate().isEmpty) {
        // Say what is there instead of dying inside the recovery with "Bad
        // state: No element", which reads like a lab failure.
        final present = find
            .byWidgetPredicate((w) => w.key is ValueKey<String>)
            .evaluate()
            .map((e) => (e.widget.key! as ValueKey<String>).value)
            .where(rows.contains)
            .toList();
        // What the screen actually says, so the next reader does not have to
        // guess whether the page opened, the row is missing, or the app left.
        final texts = find
            .byType(Text)
            .evaluate()
            .map((e) => (e.widget as Text).data)
            .whereType<String>()
            .take(24)
            .toList();
        fail('$rowKey is not on $pageKey (rows present: $present; '
            'page open: ${find.byKey(ValueKey(pageKey)).evaluate().isNotEmpty}; '
            'texts on screen: $texts)');
      }
      final inner = find.descendant(of: find.byKey(Key(rowKey)), matching: find.byType(Focus)).evaluate().first;
      ((inner as StatefulElement).state as dynamic).focusNode.requestFocus();
      await tester.pump(const Duration(milliseconds: 400));
    }

    // Moves to [rowKey] with real Up/Down taps; when focus is not on a row,
    // falls back to the test-only recovery.
    Future<void> goTo(WidgetTester tester, String pageKey, List<String> rows, String rowKey) async {
      out('goTo $rowKey from ${focusedRow(rows)}');
      for (var i = 0; i < rows.length + 2; i++) {
        final current = focusedRow(rows);
        if (current == rowKey) return;
        if (current == null) {
          await recoverTo(tester, pageKey, rows, rowKey);
          return;
        }
        await tap(tester, rows.indexOf(current) < rows.indexOf(rowKey) ? LogicalKeyboardKey.arrowDown : LogicalKeyboardKey.arrowUp);
      }
      final landed = focusedRow(rows);
      out('goTo $rowKey ended on $landed');
      if (landed != rowKey) violations.add('goTo could not reach $rowKey with Up/Down (landed on $landed)');
    }

    Future<void> setWindow(WidgetTester tester, double w, double h) async {
      tester.view.devicePixelRatio = 1.0;
      tester.view.physicalSize = Size(w, h);
      await tester.pump(const Duration(milliseconds: 600));
    }

    testWidgets('Settings, Diagnostics and the debug overlay with held keys', (tester) async {
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
      // Synthetic key events may leave the highlight in "touch" mode, where no
      // focus indicator is drawn; a real keyboard switches it to traditional.
      FocusManager.instance.highlightStrategy = FocusHighlightStrategy.alwaysTraditional;
      addTearDown(() => FocusManager.instance.highlightStrategy = FocusHighlightStrategy.automatic);
      await setWindow(tester, 1920, 1080);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-tile-settings')).evaluate().isNotEmpty, failMessage: 'no mosaic');

      // ---- Debug overlay (player) ------------------------------------------
      await tap(tester, LogicalKeyboardKey.enter); // the featured tile
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-page')).evaluate().isEmpty && osdVisible(tester), failMessage: 'Select on the featured tile did not enter the player');
      await tap(tester, LogicalKeyboardKey.keyD);
      await pumpUntil(tester, () => find.byKey(const Key('debug-overlay')).evaluate().isNotEmpty, failMessage: 'D did not open the debug overlay');
      await tester.pump(const Duration(seconds: 1));
      final overlay = find.byKey(const Key('debug-overlay'));
      final overlayFocusables = find.descendant(
        of: overlay,
        matching: find.byWidgetPredicate((w) => w is Focus || w is InkResponse || w is ButtonStyleButton || w is Switch || w is Slider || w is TextField),
      );
      out('debug overlay: focusable/interactive widgets inside = ${overlayFocusables.evaluate().length}; primaryFocus = ${FocusManager.instance.primaryFocus}');
      String overlayLevel() => tester.widget<Text>(find.byKey(const Key('debug-overlay-log-level'))).data ?? '';
      final levelBefore = overlayLevel();
      final inputs = <LogicalKeyboardKey>[
        LogicalKeyboardKey.arrowUp,
        LogicalKeyboardKey.arrowDown,
        LogicalKeyboardKey.arrowLeft,
        LogicalKeyboardKey.arrowRight,
        LogicalKeyboardKey.enter,
      ];
      await screenshot(tester, 'debug_overlay_open', find.byKey(const Key('debug-overlay')));
      // The overlay is IgnorePointer and holds no focus; arrows are still the
      // player's channel switch, Select the OSD. Only check the overlay's own
      // option (log level) never reacts, using keys that do not leave the channel.
      for (final k in [LogicalKeyboardKey.arrowLeft, LogicalKeyboardKey.arrowRight, LogicalKeyboardKey.enter]) {
        await tap(tester, k);
        out('debug overlay after ${k.keyLabel}: log level = ${overlayLevel()} (was $levelBefore), overlay still open = ${find.byKey(const Key('debug-overlay')).evaluate().isNotEmpty}');
      }
      out('debug overlay: inputs tried = ${inputs.map((k) => k.keyLabel).join(',')} (Up/Down not sent: they switch channel)');
      await screenshot(tester, 'debug_overlay_after_keys', find.byKey(const Key('debug-overlay')));
      await tap(tester, LogicalKeyboardKey.keyD); // close
      // Back until the mosaic.
      for (var i = 0; i < 4 && find.byKey(const Key('mosaic-page')).evaluate().isEmpty; i++) {
        await tap(tester, LogicalKeyboardKey.escape);
        await tester.pump(const Duration(seconds: 1));
      }
      await pumpUntil(tester, () => find.byKey(const Key('mosaic-tile-settings')).evaluate().isNotEmpty, failMessage: 'Back did not return to the mosaic');

      // ---- Settings, 1920x1080, automatic scale ----------------------------
      await tap(tester, LogicalKeyboardKey.arrowUp);
      await tap(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('settings-page')).evaluate().isNotEmpty, failMessage: 'Settings did not open');
      await tester.pump(const Duration(seconds: 1));
      const sp = 'settings-page';
      const first = 'settings-controls-tile';
      const last = 'settings-diagnostics-tile';
      await step(tester, 'settings_open_1920x1080', sp, _settingsRows);
      // The first row holds focus at open (Connection and Server sit above it).
      for (var i = _settingsRows.indexOf(first) + 1; i < _settingsRows.length; i++) {
        await tap(tester, LogicalKeyboardKey.arrowDown);
        await step(tester, 'down_tap_${i}_1920x1080', sp, _settingsRows);
      }
      // Single Down on the last row.
      await tap(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'down_tap_past_end_1920x1080', sp, _settingsRows);
      await tap(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'down_tap_past_end_again_1920x1080', sp, _settingsRows);
      await tap(tester, LogicalKeyboardKey.arrowUp);
      await step(tester, 'up_tap_after_overshoot_1920x1080', sp, _settingsRows);
      // Hold Down from the top.
      await recoverTo(tester, sp, _settingsRows, first);
      await tap(tester, LogicalKeyboardKey.arrowUp); // reaches the AppBar back button or stays
      await recoverTo(tester, sp, _settingsRows, first);
      await hold(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'down_hold_from_top_1920x1080', sp, _settingsRows);
      // Hold Up from the last row.
      await recoverTo(tester, sp, _settingsRows, last);
      await hold(tester, LogicalKeyboardKey.arrowUp);
      await step(tester, 'up_hold_from_last_1920x1080', sp, _settingsRows);
      await hold(tester, LogicalKeyboardKey.arrowUp, ms: 600);
      await step(tester, 'up_hold_again_at_top_1920x1080', sp, _settingsRows);

      // ---- 1280x720 with Interface size 150%, set with the keys ------------
      await setWindow(tester, 1280, 720);
      await recoverTo(tester, sp, _settingsRows, 'settings-ui-scale-tile');
      await step(tester, 'resized_1280x720_auto', sp, _settingsRows);
      for (var i = 0; i < 4; i++) {
        await tap(tester, LogicalKeyboardKey.enter); // 75, 100, 125, 150
      }
      out('interface size override = ${UiScaleOverride.notifier.value}');
      await step(tester, 'interface_size_150_1280x720', sp, _settingsRows);
      await recoverTo(tester, sp, _settingsRows, first);
      await hold(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'down_hold_from_top_1280x720_150', sp, _settingsRows);
      await recoverTo(tester, sp, _settingsRows, last);
      await tap(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'down_tap_past_end_1280x720_150', sp, _settingsRows);
      await recoverTo(tester, sp, _settingsRows, last);
      await hold(tester, LogicalKeyboardKey.arrowUp);
      await step(tester, 'up_hold_from_last_1280x720_150', sp, _settingsRows);

      // ---- 800x450 with Interface size 200% (capped to fit) ----------------
      await setWindow(tester, 800, 450);
      await recoverTo(tester, sp, _settingsRows, 'settings-ui-scale-tile');
      await tap(tester, LogicalKeyboardKey.enter); // 150 -> 200
      out('interface size override = ${UiScaleOverride.notifier.value}');
      await step(tester, 'interface_size_200_800x450', sp, _settingsRows);
      await recoverTo(tester, sp, _settingsRows, first);
      await hold(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'down_hold_from_top_800x450_200', sp, _settingsRows);
      await recoverTo(tester, sp, _settingsRows, last);
      await tap(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'down_tap_past_end_800x450_200', sp, _settingsRows);
      await recoverTo(tester, sp, _settingsRows, last);
      // Walk Up one tap at a time to see each scroll step.
      for (var i = 1; i <= _settingsRows.length; i++) {
        await tap(tester, LogicalKeyboardKey.arrowUp);
        await step(tester, 'up_tap_${i}_800x450_200', sp, _settingsRows);
      }
      await recoverTo(tester, sp, _settingsRows, last);
      await hold(tester, LogicalKeyboardKey.arrowUp);
      await step(tester, 'up_hold_from_last_800x450_200', sp, _settingsRows);

      // ---- A tall window: 2560x1440, Down on the last row --------------------
      await setWindow(tester, 2560, 1440);
      await recoverTo(tester, sp, _settingsRows, first);
      for (var i = 1; i < _settingsRows.length; i++) {
        await tap(tester, LogicalKeyboardKey.arrowDown);
      }
      await step(tester, 'on_last_row_2560x1440', sp, _settingsRows);
      await tap(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'down_tap_past_end_2560x1440', sp, _settingsRows);

      // ---- Interface size back to Automatic, Log level, Diagnostics ---------
      await setWindow(tester, 1280, 720);
      await goTo(tester, sp, _settingsRows, 'settings-ui-scale-tile');
      await step(tester, 'back_to_automatic_before_select', sp, _settingsRows);
      await tap(tester, LogicalKeyboardKey.enter); // 200 -> Automatic
      out('interface size override reset = ${UiScaleOverride.notifier.value}');
      await goTo(tester, sp, _settingsRows, 'settings-log-level-tile');
      await step(tester, 'on_log_level_row', sp, _settingsRows);
      String settingsLevel() {
        final f = find.byKey(const Key('settings-log-level-value'));
        return f.evaluate().isEmpty ? '<row not in tree>' : tester.widget<Text>(f).data ?? '';
      }

      final settingsLevelBefore = settingsLevel();
      await tap(tester, LogicalKeyboardKey.enter);
      out('Settings log level: $settingsLevelBefore -> ${settingsLevel()}');
      await step(tester, 'log_level_changed_in_settings', sp, _settingsRows);
      for (var i = 0; i < 3; i++) {
        await tap(tester, LogicalKeyboardKey.enter);
      }
      out('Settings log level after 4 Selects: ${settingsLevel()} (Log.level=${Log.level})');
      await goTo(tester, sp, _settingsRows, last);
      await tap(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => find.byKey(const Key('diagnostics-page')).evaluate().isNotEmpty, failMessage: 'Diagnostics did not open');
      await tester.pump(const Duration(seconds: 1));
      const dp = 'diagnostics-page';
      String dlevel() => tester.widget<Text>(find.byKey(const Key('diagnostics-level-value'))).data ?? '';
      await step(tester, 'diagnostics_open', dp, _diagnosticsRows);
      if (focusedRow(_diagnosticsRows) != 'diagnostics-level-tile') {
        violations.add('diagnostics opened with focus on ${focusedRow(_diagnosticsRows)}, not diagnostics-level-tile');
      }
      final diagLevelBefore = dlevel();
      await tap(tester, LogicalKeyboardKey.enter); // cycle the level on the focused tile
      final diagLevelAfter = dlevel();
      out('Diagnostics log level Select: $diagLevelBefore -> $diagLevelAfter (Log.level=${Log.level})');
      if (diagLevelAfter == diagLevelBefore) violations.add('Select on the Diagnostics Log level tile did not change it');
      await step(tester, 'diagnostics_level_cycled', dp, _diagnosticsRows);
      await tap(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'diagnostics_down_to_bundle', dp, _diagnosticsRows);
      await tap(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'diagnostics_down_past_end', dp, _diagnosticsRows);
      if (focusedRow(_diagnosticsRows) != 'diagnostics-bundle-tile') violations.add('Down past the end left the bundle tile');
      await tap(tester, LogicalKeyboardKey.arrowUp);
      await step(tester, 'diagnostics_up_1', dp, _diagnosticsRows);
      await tap(tester, LogicalKeyboardKey.arrowUp);
      await step(tester, 'diagnostics_up_2', dp, _diagnosticsRows);
      await tap(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'diagnostics_down_after_up_2', dp, _diagnosticsRows);
      await tap(tester, LogicalKeyboardKey.arrowLeft);
      await tap(tester, LogicalKeyboardKey.arrowRight);
      await step(tester, 'diagnostics_left_right', dp, _diagnosticsRows);
      out('Diagnostics log level: $diagLevelBefore -> ${dlevel()}; tiles: '
          '${[for (final r in _diagnosticsRows) '$r(onTap=${tester.widget<ListTile>(find.byKey(Key(r))).onTap != null})'].join(', ')}');
      await hold(tester, LogicalKeyboardKey.arrowDown);
      await step(tester, 'diagnostics_down_hold', dp, _diagnosticsRows);
      await hold(tester, LogicalKeyboardKey.arrowUp);
      await step(tester, 'diagnostics_up_hold', dp, _diagnosticsRows);
      await hold(tester, LogicalKeyboardKey.arrowDown, ms: 600);
      await recoverTo(tester, dp, _diagnosticsRows, 'diagnostics-bundle-tile');
      await tap(tester, LogicalKeyboardKey.enter); // Create log bundle
      await tester.pump(const Duration(seconds: 1));
      out('Diagnostics toast shown after Select on bundle: ${find.byKey(const Key('diagnostics-toast')).evaluate().isNotEmpty}');
      await step(tester, 'diagnostics_bundle_selected', dp, _diagnosticsRows);
      await tap(tester, LogicalKeyboardKey.escape);
      await pumpUntil(tester, () => find.byKey(const Key('diagnostics-page')).evaluate().isEmpty && find.byKey(const Key('settings-page')).evaluate().isNotEmpty, failMessage: 'Back did not return to Settings');
      await tester.pump(const Duration(milliseconds: 600));
      await step(tester, 'back_in_settings', sp, _settingsRows);
      await tap(tester, LogicalKeyboardKey.escape);

      out('violations: ${violations.length}');
      for (final v in violations) {
        out('VIOLATION $v');
      }
      expect(violations, isEmpty, reason: 'keyboard navigation lost focus or left a focused row out of view (screenshots in ${shotDir.path})');
    });
  });
}
