import 'package:client/ui/form_factor.dart';
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

    test('a fixed override replaces the automatic factor when it fits', () {
      expect(resolveUiScale(const Size(3840, 2160), override: 1.0), 1.0);
      expect(resolveUiScale(const Size(1920, 1080), override: 2.0), 2.0);
      expect(resolveUiScale(const Size(1280, 720), override: 1.5), 1.5);
    });

    test('a fixed override is capped by what fits the window', () {
      // 720p: the OSD needs 440 design px of height, so 2.0 cannot be honoured.
      expect(resolveUiScale(const Size(1280, 720), override: 2.0), closeTo(720 / uiScaleFitHeight, 1e-9));
      // A narrow window is capped by its width.
      expect(resolveUiScale(const Size(800, 2000), override: 2.0), closeTo(800 / uiScaleFitWidth, 1e-9));
      // Never below the floor, and a smaller choice is left alone.
      expect(resolveUiScale(const Size(200, 100), override: 2.0), uiScaleMin);
      expect(resolveUiScale(const Size(800, 600), override: 0.75), 0.75);
    });

    test('ignores a non-positive or non-finite override', () {
      expect(resolveUiScale(const Size(1920, 1080), override: 0), 1.0);
      expect(resolveUiScale(const Size(1920, 1080), override: -1), 1.0);
      expect(resolveUiScale(const Size(1920, 1080), override: double.nan), 1.0);
    });
  });

  group('maxFitUiScale', () {
    test('every fixed step but the largest fits 1080p, and every step fits 4K', () {
      expect(maxFitUiScale(const Size(1920, 1080)), greaterThanOrEqualTo(2.0));
      for (final step in uiScaleSteps) {
        expect(resolveUiScale(const Size(3840, 2160), override: step), step);
      }
    });

    test('is clamped and tolerates a degenerate size', () {
      expect(maxFitUiScale(const Size(10, 10)), uiScaleMin);
      expect(maxFitUiScale(const Size(100000, 100000)), uiScaleMax);
      expect(maxFitUiScale(Size.zero), uiScaleMax);
    });
  });

  group('phone scale', () {
    test('uses a 420 dp reference, not 1080p', () {
      // 360 dp is the commonest Android short side: stock Material sizes, not
      // the 0.6 floor the television reference would give it.
      expect(autoUiScale(const Size(360, 780), formFactor: ClientFormFactor.phone), closeTo(360 / 420, 1e-9));
      expect(autoUiScale(const Size(430, 932), formFactor: ClientFormFactor.phone), closeTo(430 / 420, 1e-9));
      expect(autoUiScale(const Size(780, 360), formFactor: ClientFormFactor.phone), closeTo(360 / 420, 1e-9));
    });

    test('is clamped inside the phone range', () {
      expect(autoUiScale(const Size(320, 640), formFactor: ClientFormFactor.phone), phoneUiScaleMin);
      expect(autoUiScale(const Size(800, 1280), formFactor: ClientFormFactor.phone), phoneUiScaleMax);
      expect(autoUiScale(Size.zero, formFactor: ClientFormFactor.phone), 1.0);
    });

    test('a fixed choice on a phone is never worse than the automatic one', () {
      // The television fit budget (440x640) would cap a 100% choice on a
      // 360x780 screen to 0.818 — BELOW the automatic 0.857, i.e. the cap made
      // it smaller. The phone budget is the compact panel, so every step that
      // the automatic factor allows also survives the cap.
      for (final size in const [Size(360, 780), Size(430, 932), Size(780, 360), Size(932, 430)]) {
        final auto = autoUiScale(size, formFactor: ClientFormFactor.phone);
        expect(maxFitUiScale(size, formFactor: ClientFormFactor.phone), greaterThanOrEqualTo(auto), reason: '$size');
        expect(resolveUiScale(size, override: 1.0, formFactor: ClientFormFactor.phone), 1.0, reason: '$size');
      }
      // A larger fixed step is still capped: by the phone ceiling on a wide
      // landscape screen, by the panel's own width on a narrow one.
      expect(resolveUiScale(const Size(932, 430), override: 2.0, formFactor: ClientFormFactor.phone), phoneUiScaleMax);
      expect(resolveUiScale(const Size(360, 780), override: 2.0, formFactor: ClientFormFactor.phone), 1.0);
    });

    test('the television branch is unchanged by the new argument', () {
      for (final size in const [Size(1920, 1080), Size(1280, 720), Size(3840, 2160)]) {
        expect(autoUiScale(size, formFactor: ClientFormFactor.tv), autoUiScale(size));
        expect(maxFitUiScale(size, formFactor: ClientFormFactor.tv), maxFitUiScale(size));
        expect(
          resolveUiScale(size, override: 1.5, formFactor: ClientFormFactor.tv),
          resolveUiScale(size, override: 1.5),
        );
      }
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

  testWidgets('context.uiTouch keeps a control at a touch target', (tester) async {
    tester.view.physicalSize = const Size(1920, 1080);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    late double small;
    late double big;
    await tester.pumpWidget(MaterialApp(
      home: UiScaleHost(
        child: Builder(builder: (context) {
          small = context.uiTouch(28);
          big = context.uiTouch(80);
          return const SizedBox();
        }),
      ),
    ));
    // 28 design px at scale 1 is smaller than a finger needs; 80 is left alone.
    expect(small, kMinInteractiveDimension);
    expect(big, 80.0);
  });
}
