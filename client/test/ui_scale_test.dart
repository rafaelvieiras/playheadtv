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

    test('a fixed choice multiplies the automatic factor, never replaces it', () {
      // The whole point of the relative model: 100% means "what this screen asks
      // for", so the same choice reads the same on a 4K set (2.0) and on a 1080p
      // desktop window (1.0) instead of pinning 150% of a 1080p layout to both.
      expect(resolveUiScale(const Size(3840, 2160), override: 1.0), 2.0);
      expect(resolveUiScale(const Size(1920, 1080), override: 1.0), 1.0);
      expect(resolveUiScale(const Size(1280, 720), override: 1.0), closeTo(720 / uiScaleReferenceSide, 1e-9));
      expect(resolveUiScale(const Size(3840, 2160), override: 0.85), closeTo(2.0 * 0.85, 1e-9));
      expect(resolveUiScale(const Size(1920, 1080), override: 1.3), closeTo(1.3, 1e-9));
    });

    test('the result is capped by what fits the window', () {
      // 720p: the OSD needs 440 design px of height, so a 130% choice (0.87) is
      // under the cap and survives; a wild one is not.
      expect(
        resolveUiScale(const Size(1280, 720), override: 1.3),
        closeTo((720 / uiScaleReferenceSide) * 1.3, 1e-9),
      );
      expect(resolveUiScale(const Size(1280, 720), override: 2.5), closeTo(720 / uiScaleFitHeight, 1e-9));
      // A narrow window is capped by its width.
      expect(resolveUiScale(const Size(800, 2000), override: 2.5), closeTo(800 / uiScaleFitWidth, 1e-9));
      // Never below the floor.
      expect(resolveUiScale(const Size(200, 100), override: 2.5), uiScaleMin);
    });

    test('ignores a non-positive or non-finite override', () {
      expect(resolveUiScale(const Size(1920, 1080), override: 0), 1.0);
      expect(resolveUiScale(const Size(1920, 1080), override: -1), 1.0);
      expect(resolveUiScale(const Size(1920, 1080), override: double.nan), 1.0);
    });
  });

  group('maxFitUiScale', () {
    test('every fixed step fits a 1080p window and a 4K one', () {
      expect(maxFitUiScale(const Size(1920, 1080)), greaterThanOrEqualTo(2.0));
      for (final size in const [Size(1920, 1080), Size(3840, 2160), Size(1280, 720)]) {
        final auto = autoUiScale(size);
        for (final step in uiScaleSteps) {
          expect(
            resolveUiScale(size, override: step),
            closeTo((auto * step).clamp(uiScaleMin, uiScaleMax), 1e-9),
            reason: '$size x $step',
          );
        }
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

    test('a fixed choice on a phone multiplies its own automatic factor', () {
      // The cap must never make a choice *smaller* than the automatic factor:
      // that was the television fit budget's failure on a 360 dp screen.
      for (final size in const [Size(360, 780), Size(430, 932), Size(780, 360), Size(932, 430)]) {
        final auto = autoUiScale(size, formFactor: ClientFormFactor.phone);
        expect(maxFitUiScale(size, formFactor: ClientFormFactor.phone), greaterThanOrEqualTo(auto), reason: '$size');
        expect(resolveUiScale(size, override: 1.0, formFactor: ClientFormFactor.phone), closeTo(auto, 1e-9), reason: '$size');
      }
      // A larger choice is still capped: by the phone ceiling on a wide
      // landscape screen, by the panel's own width on a narrow one.
      expect(resolveUiScale(const Size(932, 430), override: 2.5, formFactor: ClientFormFactor.phone), phoneUiScaleMax);
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
      expect(seen, ['Automatic', '85%', '100%', '115%', '130%', 'Automatic']);
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
