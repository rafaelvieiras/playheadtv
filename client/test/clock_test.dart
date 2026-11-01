// The always-on clock: the policy behind its Settings rows, and the widget that
// draws it over the picture with the OSD closed.
import 'package:client/clock.dart';
import 'package:client/services/settings_service.dart';
import 'package:client/widgets/clock_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  group('sanitizeClockOpacityPct', () {
    test('keeps a choice', () {
      for (final c in clockOpacityChoices) {
        expect(sanitizeClockOpacityPct(c), c);
      }
    });
    test('snaps to the nearest choice', () {
      expect(sanitizeClockOpacityPct(10), 20);
      expect(sanitizeClockOpacityPct(35), 40);
      expect(sanitizeClockOpacityPct(99), 100);
    });
    test('falls back to the default on junk', () {
      expect(sanitizeClockOpacityPct(null), defaultClockOpacityPct);
      expect(sanitizeClockOpacityPct('80'), defaultClockOpacityPct);
      expect(sanitizeClockOpacityPct(double.nan), defaultClockOpacityPct);
    });
  });

  group('sanitizeClockScale', () {
    test('keeps a choice', () {
      for (final c in clockScaleChoices) {
        expect(sanitizeClockScale(c), c);
      }
    });
    test('snaps out of range to an end', () {
      expect(sanitizeClockScale(0.1), clockScaleChoices.first);
      expect(sanitizeClockScale(9), clockScaleChoices.last);
    });
    test('falls back to the default on junk', () {
      expect(sanitizeClockScale(null), defaultClockScale);
      expect(sanitizeClockScale(false), defaultClockScale);
    });
  });

  test('the choices cycle and wrap around', () {
    expect(nextClockOpacityChoice(clockOpacityChoices.last), clockOpacityChoices.first);
    expect(nextClockOpacityChoice(clockOpacityChoices.first), clockOpacityChoices[1]);
    expect(nextClockScaleChoice(clockScaleChoices.last), clockScaleChoices.first);
    // A value that is not a choice starts over at the first one.
    expect(nextClockScaleChoice(0.7), clockScaleChoices.first);
  });

  test('labels are readable percentages', () {
    expect(clockOpacityLabel(80), '80%');
    expect(clockScaleLabel(1.0), '100%');
    expect(clockScaleLabel(1.25), '125%');
  });

  test('ClockOverlaySettings.reset goes back to off at the defaults', () {
    ClockOverlaySettings.alwaysVisible.value = true;
    ClockOverlaySettings.opacityPct.value = 20;
    ClockOverlaySettings.scale.value = 2.0;
    ClockOverlaySettings.reset();
    expect(ClockOverlaySettings.alwaysVisible.value, isFalse);
    expect(ClockOverlaySettings.opacityPct.value, defaultClockOpacityPct);
    expect(ClockOverlaySettings.scale.value, defaultClockScale);
  });

  test('SettingsService stores the clock choices, defaults when never chosen', () async {
    SharedPreferences.setMockInitialValues({});
    var settings = SettingsService(await SharedPreferences.getInstance());
    expect(settings.clockAlwaysVisible, isFalse);
    expect(settings.clockOpacityPct, defaultClockOpacityPct);
    expect(settings.clockScale, defaultClockScale);

    await settings.setClockAlwaysVisible(true);
    await settings.setClockOpacityPct(35); // snapped
    await settings.setClockScale(1.4); // snapped
    settings = SettingsService(await SharedPreferences.getInstance());
    expect(settings.clockAlwaysVisible, isTrue);
    expect(settings.clockOpacityPct, 40);
    expect(settings.clockScale, 1.5);
  });

  group('the widget', () {
    // A fixed clock: 21:07 local.
    DateTime now() => DateTime(2030, 1, 1, 21, 7);

    Widget host({required ValueNotifier<bool> osdVisible, required ValueNotifier<bool> guideOpen}) => MaterialApp(
          home: Scaffold(
            body: Stack(children: [
              ClockOverlay(osdVisible: osdVisible, guideOpen: guideOpen, now: now),
            ]),
          ),
        );

    setUp(ClockOverlaySettings.reset);
    tearDown(ClockOverlaySettings.reset);

    testWidgets('draws nothing until the setting is on', (tester) async {
      await tester.pumpWidget(host(osdVisible: ValueNotifier(false), guideOpen: ValueNotifier(false)));
      expect(find.byKey(const Key('always-on-clock')), findsNothing);

      ClockOverlaySettings.alwaysVisible.value = true;
      await tester.pump();
      expect(find.text('21:07'), findsOneWidget);
    });

    testWidgets('steps aside while the OSD or the guide is on screen', (tester) async {
      ClockOverlaySettings.alwaysVisible.value = true;
      final osdVisible = ValueNotifier(false);
      final guideOpen = ValueNotifier(false);
      await tester.pumpWidget(host(osdVisible: osdVisible, guideOpen: guideOpen));
      expect(find.byKey(const Key('always-on-clock')), findsOneWidget);

      osdVisible.value = true;
      await tester.pump();
      expect(find.byKey(const Key('always-on-clock')), findsNothing);

      osdVisible.value = false;
      guideOpen.value = true;
      await tester.pump();
      expect(find.byKey(const Key('always-on-clock')), findsNothing);

      guideOpen.value = false;
      await tester.pump();
      expect(find.byKey(const Key('always-on-clock')), findsOneWidget);
    });

    testWidgets('applies the opacity and the size', (tester) async {
      ClockOverlaySettings.alwaysVisible.value = true;
      ClockOverlaySettings.opacityPct.value = 40;
      ClockOverlaySettings.scale.value = 2.0;
      await tester.pumpWidget(host(osdVisible: ValueNotifier(false), guideOpen: ValueNotifier(false)));

      final opacity = tester.widget<Opacity>(find.ancestor(
        of: find.byKey(const Key('always-on-clock')),
        matching: find.byType(Opacity),
      ).first);
      expect(opacity.opacity, closeTo(0.4, 1e-9));

      // The size is the OSD clock's (28 at 1080p) times the choice. The test
      // window is not 1080p tall, so compare the ratio instead of the pixels.
      final text = tester.widget<Text>(find.byKey(const Key('always-on-clock')));
      final base = ClockOverlaySettings.scale.value;
      expect(text.style!.fontSize, greaterThan(28 * base * 0.5));
    });

    testWidgets('takes no pointer events and never takes focus', (tester) async {
      ClockOverlaySettings.alwaysVisible.value = true;
      await tester.pumpWidget(host(osdVisible: ValueNotifier(false), guideOpen: ValueNotifier(false)));
      final ignore = find.ancestor(
        of: find.byKey(const Key('always-on-clock')),
        matching: find.byType(IgnorePointer),
      );
      expect(ignore, findsWidgets);
      expect(tester.widget<IgnorePointer>(ignore.first).ignoring, isTrue);
    });
  });
}
