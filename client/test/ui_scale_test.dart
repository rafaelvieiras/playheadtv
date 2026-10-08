import 'package:client/ui/ui_scale.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  group('autoUiScale', () {
    test('is 1 at a 1080p shortest side, whatever the aspect ratio', () {
      expect(autoUiScale(const Size(1920, 1080)), 1.0);
      expect(autoUiScale(const Size(2560, 1080)), 1.0); // 21:9
      expect(autoUiScale(const Size(1080, 1920)), 1.0); // portrait
    });

    test('follows the shortest side', () {
      expect(autoUiScale(const Size(1280, 720)), closeTo(720 / 1080, 1e-9));
      expect(autoUiScale(const Size(3840, 2160)), 2.0);
      expect(autoUiScale(const Size(2160, 3840)), 2.0);
    });

    test('is clamped at both ends', () {
      expect(autoUiScale(const Size(200, 100)), uiScaleMin);
      expect(autoUiScale(const Size(8000, 4500)), 4500 / 1080 > uiScaleMax ? uiScaleMax : 4500 / 1080);
      expect(autoUiScale(const Size(16000, 9000)), uiScaleMax);
    });

    test('falls back to 1 for a degenerate size', () {
      expect(autoUiScale(Size.zero), 1.0);
      expect(autoUiScale(const Size(double.infinity, double.infinity)), 1.0);
    });
  });

  group('resolveUiScale', () {
    test('uses the automatic factor without an override', () {
      expect(resolveUiScale(const Size(3840, 2160)), 2.0);
    });

    test('a fixed override replaces the automatic factor, unclamped', () {
      expect(resolveUiScale(const Size(3840, 2160), override: 1.0), 1.0);
      expect(resolveUiScale(const Size(800, 600), override: 2.0), 2.0);
    });

    test('ignores a non-positive or non-finite override', () {
      expect(resolveUiScale(const Size(1920, 1080), override: 0), 1.0);
      expect(resolveUiScale(const Size(1920, 1080), override: -1), 1.0);
      expect(resolveUiScale(const Size(1920, 1080), override: double.nan), 1.0);
    });
  });

  group('settings choices', () {
    test('cycle Automatic, each step, then Automatic again', () {
      double? c;
      final seen = <String>[uiScaleLabel(c)];
      for (var i = 0; i < uiScaleSteps.length + 1; i++) {
        c = nextUiScaleChoice(c);
        seen.add(uiScaleLabel(c));
      }
      expect(seen, ['Automatic', '75%', '100%', '125%', '150%', '200%', 'Automatic']);
    });

    test('an unknown stored value returns to Automatic', () {
      expect(nextUiScaleChoice(1.1), isNull);
    });
  });

  testWidgets('UiScaleHost provides the factor and context.ui scales pixels', (tester) async {
    tester.view.physicalSize = const Size(3840, 2160);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    late double seen;
    await tester.pumpWidget(MaterialApp(
      home: UiScaleHost(
        child: Builder(builder: (context) {
          seen = context.ui(10);
          return const SizedBox();
        }),
      ),
    ));
    expect(seen, 20.0);
  });
}
