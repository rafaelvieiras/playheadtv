// The first-run screens on a phone: the three pages between an install and a
// paired device (server address, the PIN, manual entry).
//
// They were the one part of the client the phone work never covered, because
// every integration suite boots against a fixture that writes a connection
// first — so the app goes straight to the mosaic and never draws these. A real
// phone found it: two buttons side by side in `SetupPage` were 135 px wider than
// a 393 dp screen, and a control off the edge is a screen that does not answer.
//
// Asserted, per page, at the narrowest phone this project targets: nothing
// overflows (a rendering overflow fails the test on its own), every control is at
// least a finger tall, and every control is inside the window.
import 'package:client/pages/pairing_page.dart';
import 'package:client/pages/server_url_page.dart';
import 'package:client/pages/setup_page.dart';
import 'package:client/ui/form_factor.dart';
import 'package:client/ui/ui_scale.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

const _phoneSizes = [Size(360, 780), Size(780, 360)];

Widget app(Widget home) => ClientFormFactorHost(
  child: MaterialApp(
    debugShowCheckedModeBanner: false,
    theme: ThemeData.dark(),
    builder: (context, child) => UiScaleHost(child: child!),
    home: home,
  ),
);

/// The smallest tap target a finger needs, in logical pixels.
const double minTapTarget = 48;

/// Scrolls a control into view and asserts it lands inside the window: a
/// control a finger can reach is the property that matters, not whether a tall
/// page happens to need a scroll to show it.
Future<void> expectReachable(WidgetTester tester, Finder finder, String label) async {
  final window = tester.view.physicalSize / tester.view.devicePixelRatio;
  for (final element in finder.evaluate()) {
    await tester.ensureVisible(find.byElementPredicate((e) => e == element));
    await tester.pump(const Duration(milliseconds: 200));
    final box = element.renderObject as RenderBox;
    if (!box.hasSize || box.size.isEmpty || !box.attached) continue;
    final origin = box.localToGlobal(Offset.zero);
    expect(
      origin.dx >= -0.5 && origin.dx + box.size.width <= window.width + 0.5,
      isTrue,
      reason: '$label: ${element.widget.runtimeType} at $origin size ${box.size} is outside the width $window',
    );
    expect(
      origin.dy >= -0.5 && origin.dy + box.size.height <= window.height + 0.5,
      isTrue,
      reason: '$label: ${element.widget.runtimeType} at $origin size ${box.size} cannot be scrolled into $window',
    );
  }
}

/// The property the phone work actually promises: an action a thumb can hit
/// without aiming — at least a finger tall AND most of the content width, which
/// is what a Material control sized by its own label is not (measured on a real
/// 393 dp phone: Continue was 65x35 dp).
void expectThumbSized(WidgetTester tester, Finder finder, String label) {
  final window = tester.view.physicalSize / tester.view.devicePixelRatio;
  var seen = 0;
  for (final element in finder.evaluate()) {
    final box = element.renderObject as RenderBox;
    if (!box.hasSize || box.size.isEmpty) continue;
    seen++;
    expect(box.size.height, greaterThanOrEqualTo(minTapTarget), reason: '$label: ${element.widget.runtimeType} is too short (${box.size})');
    expect(box.size.width, greaterThanOrEqualTo(window.width * 0.6),
        reason: '$label: ${element.widget.runtimeType} hugs its label (${box.size}) on a phone');
  }
  expect(seen, greaterThan(0), reason: '$label: no action found to measure');
}

void expectFingerSized(WidgetTester tester, String label) {
  for (final element in find.byType(OutlinedButton).evaluate()) {
    final box = element.renderObject as RenderBox;
    expect(box.size.height, greaterThanOrEqualTo(minTapTarget), reason: '$label: an outlined button is too short');
  }
  for (final element in find.byType(FilledButton).evaluate()) {
    final box = element.renderObject as RenderBox;
    expect(box.size.height, greaterThanOrEqualTo(minTapTarget), reason: '$label: a filled button is too short');
  }
}

void main() {
  setUp(() {
    // These pages read settings and write them on success; the real store is
    // not available in a widget test.
    SharedPreferences.setMockInitialValues({});
    UiScaleOverride.notifier.value = null;
    ClientFormFactorState.notifier.value = ClientFormFactor.phone;
  });

  // The television must keep the layout every existing report and screenshot
  // shows: actions sized by their own label, side by side. This is the frozen
  // half of the change — the phone branch above must not have moved it.
  testWidgets('a television keeps intrinsic-width actions', (tester) async {
    ClientFormFactorState.notifier.value = ClientFormFactor.tv;
    tester.view.physicalSize = const Size(1920, 1080);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    await tester.pumpWidget(app(const SetupPage()));
    await tester.pump(const Duration(seconds: 1));
    await tester.pump(const Duration(seconds: 1));

    final save = tester.renderObject<RenderBox>(find.byKey(const Key('setup-save-button')));
    final test = tester.renderObject<RenderBox>(find.byKey(const Key('setup-test-button')));
    expect(save.size.width, lessThan(1920 * 0.4), reason: 'the TV button stopped hugging its label');
    expect(test.size.width, lessThan(1920 * 0.4), reason: 'the TV button stopped hugging its label');
    expect(test.localToGlobal(Offset.zero).dy, save.localToGlobal(Offset.zero).dy,
        reason: 'the two TV actions are no longer on one line');
  });

  for (final (name, size) in [for (final s in _phoneSizes) ('${s.width.toInt()}x${s.height.toInt()}', s)]) {
    testWidgets('the server address screen fits $name and answers a finger', (tester) async {
      tester.view.physicalSize = size * 2.75;
      tester.view.devicePixelRatio = 2.75;
      addTearDown(tester.view.reset);

      await tester.pumpWidget(app(const ServerUrlPage(returnsResult: true)));
      await tester.pump(const Duration(seconds: 1));
      await tester.pump(const Duration(seconds: 1));

      expect(find.byKey(const Key('server-url-field')), findsOneWidget);
      expectFingerSized(tester, 'server url $name');
      await expectReachable(tester, find.byType(TextField), 'server url $name');
      await expectReachable(tester, find.byType(FilledButton), 'server url $name');
      expectThumbSized(tester, find.byKey(const Key('server-url-continue-button')), 'server url $name');

      // The one thing a thumb must be able to do here: put the keyboard up.
      await tester.tap(find.byKey(const Key('server-url-field')));
      await tester.pump();
      final field = tester.widget<TextField>(find.byKey(const Key('server-url-field')));
      expect(field.focusNode?.hasFocus, isTrue, reason: 'tapping the field did not focus it');
    });

    testWidgets('the pairing screen fits $name and answers a finger', (tester) async {
      tester.view.physicalSize = size * 2.75;
      tester.view.devicePixelRatio = 2.75;
      addTearDown(tester.view.reset);

      // No client: the start call fails at once and the error state is what is
      // measured, which is the state with the most to fit in.
      await tester.pumpWidget(app(const PairingPage(serverUrl: 'http://192.0.2.1:9394')));
      await tester.pump(const Duration(seconds: 1));

      expect(find.byKey(const Key('pairing-page')), findsOneWidget);
      expectFingerSized(tester, 'pairing $name');
      await expectReachable(tester, find.byType(OutlinedButton), 'pairing $name');
      expect(find.byKey(const Key('pairing-retry-button')), findsOneWidget);
      expectThumbSized(tester, find.byKey(const Key('pairing-select-server-button')), 'pairing $name');
      expectThumbSized(tester, find.byKey(const Key('pairing-manual-button')), 'pairing $name');
    });

    testWidgets('manual entry fits $name and answers a finger', (tester) async {
      tester.view.physicalSize = size * 2.75;
      tester.view.devicePixelRatio = 2.75;
      addTearDown(tester.view.reset);

      await tester.pumpWidget(app(const SetupPage()));
      await tester.pump(const Duration(seconds: 1));
      await tester.pump(const Duration(seconds: 1));

      expect(find.byKey(const Key('setup-test-button')), findsOneWidget);
      expect(find.byKey(const Key('setup-save-button')), findsOneWidget);
      expectFingerSized(tester, 'setup $name');
      await expectReachable(tester, find.byType(OutlinedButton), 'setup $name');
      await expectReachable(tester, find.byType(FilledButton), 'setup $name');
      expectThumbSized(tester, find.byKey(const Key('setup-save-button')), 'setup $name');
      expectThumbSized(tester, find.byKey(const Key('setup-test-button')), 'setup $name');
    });
  }
}
