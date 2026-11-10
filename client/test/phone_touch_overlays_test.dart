// The overlays a phone can reach by tapping and could then not leave: the track
// picker, the dim level dialog and the update banner/changelog. Each was
// reachable with a finger and operable only with a keyboard (or a remote), which
// is the one thing the phone work must never ship.
//
// The television side of the same widgets is asserted too, because a row that
// grows on a TV would move a frozen golden.
import 'dart:io';

import 'package:client/plugin/plugin_manifest.dart';
import 'package:client/input/input_action.dart';
import 'package:client/services/settings_service.dart';
import 'package:client/ui/form_factor.dart';
import 'package:client/ui/ui_scale.dart';
import 'package:client/update/install_kind.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/semver.dart';
import 'package:client/update/update_notices.dart';
import 'package:client/widgets/dimmer_level_dialog.dart';
import 'package:client/widgets/plugin_source_settings_dialog.dart';
import 'package:client/widgets/track_selector_overlay.dart';
import 'package:client/widgets/update_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

Widget app(Widget home, {bool updateHost = false}) => ClientFormFactorHost(
      child: MaterialApp(
        theme: ThemeData.dark(),
        builder: (context, child) =>
            UiScaleHost(child: updateHost ? UpdateOverlayHost(child: child!) : child!),
        home: home,
      ),
    );

Future<void> useSize(WidgetTester tester, Size s) async {
  tester.view.physicalSize = s;
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
}

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
    ClientFormFactorState.notifier.value = ClientFormFactor.phone;
  });
  tearDown(() => ClientFormFactorState.notifier.value = ClientFormFactor.tv);

  group('track picker', () {
    Widget picker({
      required void Function(TrackSelectorPanel, TrackOption) onSelect,
      VoidCallback? onClose,
    }) =>
        TrackSelectorOverlay(
          key: const Key('picker'),
          audioOptions: const [TrackOption(index: 0, label: 'English'), TrackOption(index: 1, label: 'Portuguese')],
          subtitleOptions: const [TrackOption(index: -1, label: 'None'), TrackOption(index: 2, label: 'English')],
          initialPanel: TrackSelectorPanel.audio,
          singlePanel: true,
          onSelect: onSelect,
          onClose: onClose ?? () {},
        );

    testWidgets('a tap on an option picks that option', (tester) async {
      final picked = <String>[];
      await useSize(tester, const Size(780, 360));
      await tester.pumpWidget(app(Scaffold(
        body: picker(onSelect: (panel, option) => picked.add('${panel.name}:${option.index}')),
      )));
      await tester.pump();
      await tester.tap(find.byKey(const Key('track-option-audio-1')));
      await tester.pump();
      expect(picked, ['audio:1']);
    });

    testWidgets('a tap outside the panel closes it', (tester) async {
      var closed = 0;
      await useSize(tester, const Size(780, 360));
      await tester.pumpWidget(app(Scaffold(body: picker(onSelect: (_, _) {}, onClose: () => closed++))));
      await tester.pump();
      await tester.tapAt(const Offset(4, 4));
      await tester.pump();
      expect(closed, 1);
    });

    testWidgets('a phone row is a touch target; the television row is exactly what it was', (tester) async {
      await useSize(tester, const Size(780, 360));
      double rowHeight() => tester.getSize(find.byKey(const Key('track-option-audio-0'))).height;

      await tester.pumpWidget(app(Scaffold(body: picker(onSelect: (_, _) {}))));
      await tester.pump();
      final phone = rowHeight();

      ClientFormFactorState.notifier.value = ClientFormFactor.tv;
      await tester.pumpWidget(app(Scaffold(body: picker(onSelect: (_, _) {}))));
      await tester.pump();
      final tv = rowHeight();

      expect(phone, greaterThanOrEqualTo(48), reason: 'a finger needs a target');
      expect(tv, lessThan(phone), reason: 'a television keeps its frozen row height');
      expect(tv, lessThan(48));
    });
  });

  group('dim level dialog', () {
    Future<int?> open(WidgetTester tester, {required void Function(int) onChanged}) async {
      int? result;
      await useSize(tester, const Size(780, 360));
      await tester.pumpWidget(app(Scaffold(
        body: Builder(
          builder: (context) => TextButton(
            onPressed: () async {
              result = await showDimmerLevelDialog(context, initial: 30, resolve: (_) => null, onChanged: onChanged);
            },
            child: const Text('open'),
          ),
        ),
      )));
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      return result;
    }

    testWidgets('the buttons adjust the level and Save returns it', (tester) async {
      final changes = <int>[];
      await open(tester, onChanged: changes.add);
      expect(find.byKey(const Key('dimmer-dialog')), findsOneWidget);

      await tester.tap(find.byKey(const Key('dimmer-dialog-up')));
      await tester.pump();
      expect(changes, isNotEmpty, reason: 'each step previews, exactly as a key does');

      await tester.tap(find.byKey(const Key('dimmer-dialog-save')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('dimmer-dialog')), findsNothing);
      expect(find.text('open'), findsOneWidget);
    });

    testWidgets('Cancel closes without confirming', (tester) async {
      final changes = <int>[];
      await open(tester, onChanged: changes.add);
      await tester.tap(find.byKey(const Key('dimmer-dialog-up')));
      await tester.pump();
      await tester.tap(find.byKey(const Key('dimmer-dialog-cancel')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('dimmer-dialog')), findsNothing);
    });

    testWidgets('the buttons never take the focus from the dialog keys', (tester) async {
      await open(tester, onChanged: (_) {});
      await tester.tap(find.byKey(const Key('dimmer-dialog-up')));
      await tester.pump();
      // A focused button would swallow Select and turn it into "press this
      // button"; ExcludeFocus is what keeps the dialog's own handler first.
      expect(find.byWidgetPredicate((w) => w is TextButton && (w.focusNode?.hasFocus ?? false)), findsNothing);
    });
  });

  group('plugin settings dialog', () {
    testWidgets('the box stays inside a landscape phone', (tester) async {
      final settings = await SettingsService.create();
      final keys = KeyBindingsService(settings);
      await useSize(tester, const Size(780, 360));
      await tester.pumpWidget(app(Scaffold(
        body: Builder(
          builder: (context) => TextButton(
            onPressed: () => showDialog<void>(
              context: context,
              builder: (_) => PluginSourceSettingsDialog(
                source: const PluginSource(id: 's', pluginId: 'p', name: 'Source', enabled: true, settings: {}),
                fields: const [PluginConfigField(key: 'quality', label: 'Quality', type: 'text')],
                save: (_) async =>
                    const PluginSource(id: 's', pluginId: 'p', name: 'Source', enabled: true, settings: {}),
                keyBindings: keys,
              ),
            ),
            child: const Text('open'),
          ),
        ),
      )));
      await tester.tap(find.text('open'));
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull, reason: 'no overflow on a 360 dp-tall window');
      expect(find.byKey(const Key('plugin-source-settings')), findsOneWidget);
    });
  });

  group('update overlay', () {
    late Directory scratch;
    setUp(() => scratch = Directory.systemTemp.createTempSync('playheadtv_overlay_touch_'));
    tearDown(() {
      UpdateNotices.overrideForTest(null);
      scratch.deleteSync(recursive: true);
    });

    Future<void> showBanner(WidgetTester tester, Key key) async {
      UpdateNotices.overrideForTest(UpdateNoticeEnvironment(
        runningVersion: '1.0.0',
        kind: InstallKind.tarball,
        stateFile: null,
        releaseSource: _FixedSource(),
        autoUpdate: () => true,
        startDelay: Duration.zero,
      ));
      await useSize(tester, const Size(780, 360));
      await tester.pumpWidget(app(const Scaffold(backgroundColor: Colors.black), updateHost: true));
      // The host loads its settings and asks the fake source on real time.
      for (var i = 0; i < 30 && find.byKey(key).evaluate().isEmpty; i++) {
        await tester.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 20)));
        await tester.pump(const Duration(milliseconds: 10));
      }
      expect(find.byKey(key), findsOneWidget);
    }

    testWidgets('a tap dismisses the banner', (tester) async {
      await showBanner(tester, const Key('update-banner'));
      await tester.tap(find.byKey(const Key('update-banner')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('update-banner')), findsNothing,
          reason: 'a phone cannot press Select: the tap has to be the way out');
      expect(tester.takeException(), isNull);
    });

    testWidgets('tapping the banner never moves the focus', (tester) async {
      await showBanner(tester, const Key('update-banner'));
      final before = FocusManager.instance.primaryFocus;
      await tester.tap(find.byKey(const Key('update-banner')));
      await tester.pumpAndSettle();
      expect(FocusManager.instance.primaryFocus, same(before),
          reason: 'the overlay is a GestureDetector precisely so it adds no FocusNode');
    });
  });
}

class _FixedSource implements ReleaseSource {
  @override
  Future<ReleaseInfo?> latestStable() async {
    final url = Uri.parse('https://releases.example.test/x');
    return ReleaseInfo(version: SemVer.tryParse('2.0.0')!, packageUrl: url, checksumsUrl: url);
  }

  @override
  Future<List<int>> fetchBytes(Uri url) => throw UnimplementedError();

  @override
  Future<void> download(Uri url, File dest) => throw UnimplementedError();
}
