import 'dart:io';

import 'package:client/log/log.dart';
import 'package:client/pages/settings_page.dart';
import 'package:client/ui/ui_scale.dart';
import 'package:client/widgets/debug_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  late Directory dir;

  setUp(() {
    SharedPreferences.setMockInitialValues({});
    UiScaleOverride.notifier.value = null;
    dir = Directory.systemTemp.createTempSync('playheadtv_diag_test_');
  });

  tearDown(() async {
    await Log.close();
    Log.configure(level: LogLevel.error);
    dir.deleteSync(recursive: true);
  });

  Future<void> pumpSettings(WidgetTester tester) async {
    tester.view.physicalSize = const Size(1920, 1080);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(MaterialApp(
      builder: (context, child) => UiScaleHost(child: child!),
      home: const SettingsPage(),
    ));
    await tester.pumpAndSettle();
  }

  String text(WidgetTester tester, String key) => tester.widget<Text>(find.byKey(Key(key))).data!;

  // Moves the focus with Down until [key]'s tile has it (at most [max] steps).
  // The bound only needs to exceed the number of focusable rows in Settings, so
  // that adding a row does not turn this into a test of the row count.
  Future<void> downTo(WidgetTester tester, String key, {int max = 30}) async {
    for (var i = 0; i < max; i++) {
      final focused = FocusManager.instance.primaryFocus?.context;
      final within = focused?.findAncestorWidgetOfExactType<ListTile>()?.key == Key(key);
      if (within) return;
      await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
      await tester.pump();
    }
    fail('the focus never reached $key');
  }

  testWidgets('Log level is reachable and changeable with Down and Select only, until restart', (tester) async {
    Log.configure(level: LogLevel.error);
    await pumpSettings(tester);
    expect(text(tester, 'settings-log-level-value'), 'Error');

    await downTo(tester, 'settings-log-level-tile');
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(Log.level, LogLevel.warn);
    expect(text(tester, 'settings-log-level-value'), contains('Warn (until the app restarts, then Error)'));
    expect(Log.configuredLevel, LogLevel.error);

    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(Log.level, LogLevel.error, reason: 'Error, Warn, Info, Debug, then back to Error');
    expect(text(tester, 'settings-log-level-value'), 'Error');

    // Nothing was saved: a restart (a fresh configure) starts from the configured level.
    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getKeys().where((k) => k.contains('log')), isEmpty);
  });

  testWidgets('Diagnostics shows the path and level and bundles with Select; Back leaves', (tester) async {
    Log.configure(level: LogLevel.info, directory: dir);
    Log.info('test', 'a line for the bundle');
    await tester.runAsync(Log.flush);
    await pumpSettings(tester);

    await downTo(tester, 'settings-diagnostics-tile');
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('diagnostics-page')), findsOneWidget);
    expect(text(tester, 'diagnostics-path-value'), '${dir.path}/$logFileName');
    expect(text(tester, 'diagnostics-level-value'), 'Info');

    // Arrival focuses Log level (the first actionable row); Down reaches the action.
    await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
    await tester.pump();
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    // The bundle is real file I/O inside the fake-async zone: alternate real
    // time with frames until the toast shows.
    for (var i = 0; i < 40 && find.byKey(const Key('diagnostics-toast')).evaluate().isEmpty; i++) {
      await tester.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 50)));
      await tester.pump();
    }
    await tester.pump(const Duration(milliseconds: 100));
    expect(find.byKey(const Key('diagnostics-toast')), findsOneWidget);
    final bundle = File('${dir.path}/$logBundleName');
    expect(bundle.existsSync(), isTrue);
    expect(bundle.readAsStringSync(), contains('a line for the bundle'));

    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('diagnostics-page')), findsNothing);
    expect(find.byKey(const Key('settings-page')), findsOneWidget);
  });

  testWidgets('Diagnostics: Select on Log level cycles it like Settings, until restart', (tester) async {
    Log.configure(level: LogLevel.error);
    await pumpSettings(tester);
    await downTo(tester, 'settings-diagnostics-tile');
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(text(tester, 'diagnostics-level-value'), 'Error');
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(Log.level, LogLevel.warn);
    expect(text(tester, 'diagnostics-level-value'), contains('Warn (until the app restarts, then Error)'));
    expect(Log.configuredLevel, LogLevel.error);
    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getKeys().where((k) => k.contains('log')), isEmpty);
    // Down past the end keeps the focus on the last row.
    await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
    await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
    await tester.pump();
    final focused = FocusManager.instance.primaryFocus!.context!;
    expect(focused.findAncestorWidgetOfExactType<ListTile>()?.key, const Key('diagnostics-bundle-tile'));
  });

  testWidgets('Diagnostics says so when there is no log file', (tester) async {
    Log.configure(level: LogLevel.error);
    await pumpSettings(tester);
    await downTo(tester, 'settings-diagnostics-tile');
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(text(tester, 'diagnostics-path-value'), contains('Not written to a file'));
    await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
    await tester.pump();
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));
    expect(find.byKey(const Key('diagnostics-toast')), findsOneWidget);
  });

  testWidgets('the debug overlay shows the level and the last log lines', (tester) async {
    tester.view.physicalSize = const Size(1920, 1080);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    Log.configure(level: LogLevel.debug);
    for (var i = 0; i < 15; i++) {
      Log.debug('player', 'event number $i');
    }
    await tester.pumpWidget(const MaterialApp(
      home: Scaffold(body: Stack(children: [DebugOverlay(controller: null, engineName: 'test', decoderPreference: 'test')])),
    ));
    expect(text(tester, 'debug-overlay-log-level'), 'Debug');
    final tail = tester.widget<Column>(find.byKey(const Key('debug-overlay-log-tail')));
    final lines = tail.children.whereType<Text>().map((t) => t.data!).toList();
    expect(lines, hasLength(10));
    expect(lines.last, contains('event number 14'));
    expect(lines.first, contains('event number 5'));
    // Dispose the overlay's timer before the test ends.
    await tester.pumpWidget(const SizedBox());
  });
}
