import 'package:client/dimmer.dart';
import 'package:client/main.dart' show AppConfig;
import 'package:client/services/settings_service.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  group('sanitizeDimLevelPercent', () {
    test('keeps an in-range step', () {
      expect(sanitizeDimLevelPercent(50), 50);
      expect(sanitizeDimLevelPercent(10), 10);
      expect(sanitizeDimLevelPercent(80), 80);
    });
    test('clamps out-of-range values', () {
      expect(sanitizeDimLevelPercent(0), minDimLevelPercent);
      expect(sanitizeDimLevelPercent(-30), minDimLevelPercent);
      expect(sanitizeDimLevelPercent(95), maxDimLevelPercent);
      expect(sanitizeDimLevelPercent(1000), maxDimLevelPercent);
    });
    test('snaps to the step grid', () {
      expect(sanitizeDimLevelPercent(44), 40);
      expect(sanitizeDimLevelPercent(45), 50);
      expect(sanitizeDimLevelPercent(50.4), 50);
    });
    test('falls back to the default on junk', () {
      expect(sanitizeDimLevelPercent(null), defaultDimLevelPercent);
      expect(sanitizeDimLevelPercent('50'), defaultDimLevelPercent);
      expect(sanitizeDimLevelPercent(double.nan), defaultDimLevelPercent);
    });
  });

  group('stepDimLevel', () {
    test('moves by one step either way', () {
      expect(stepDimLevel(50, 1), 60);
      expect(stepDimLevel(50, -1), 40);
    });
    test('stops at the bounds', () {
      expect(stepDimLevel(80, 1), 80);
      expect(stepDimLevel(10, -1), 10);
      expect(stepDimLevel(70, 5), 80);
    });
  });

  test('dimOpacity is zero when off and the level when on', () {
    expect(dimOpacity(active: false, levelPercent: 50), 0);
    expect(dimOpacity(active: true, levelPercent: 50), 0.5);
    expect(dimOpacity(active: true, levelPercent: 80), 0.8);
  });

  test('Dimmer.reset goes back to off at the default level', () {
    Dimmer.active.value = true;
    Dimmer.level.value = 70;
    Dimmer.reset();
    expect(Dimmer.active.value, isFalse);
    expect(Dimmer.level.value, defaultDimLevelPercent);
  });

  test('the toggle is stored too, null until it is first touched', () async {
    SharedPreferences.setMockInitialValues({});
    final settings = SettingsService(await SharedPreferences.getInstance());
    expect(settings.dimActive, isNull);
    expect(AppConfig.configuredDimActive(settings), isFalse, reason: 'a fresh install starts undimmed');

    await settings.setDimActive(true);
    expect(AppConfig.configuredDimActive(settings), isTrue);

    await settings.setDimActive(false);
    expect(AppConfig.configuredDimActive(settings), isFalse);
  });

  test('a restart brings the picture back as it was left', () async {
    SharedPreferences.setMockInitialValues({});
    final settings = SettingsService(await SharedPreferences.getInstance());
    await settings.setDimActive(true);
    await settings.setDimLevelPercent(70);

    // The next launch: fresh notifiers, state applied from storage.
    Dimmer.reset();
    Dimmer.active.value = AppConfig.configuredDimActive(settings);
    Dimmer.level.value = AppConfig.configuredDimLevel(settings);
    expect(Dimmer.active.value, isTrue);
    expect(Dimmer.level.value, 70);
    expect(dimOpacity(active: Dimmer.active.value, levelPercent: Dimmer.level.value), 0.7);
  });

  test('SettingsService stores a sanitized level, null when never chosen', () async {
    SharedPreferences.setMockInitialValues({});
    final settings = SettingsService(await SharedPreferences.getInstance());
    expect(settings.dimLevelPercent, isNull);
    await settings.setDimLevelPercent(30);
    expect(settings.dimLevelPercent, 30);
    await settings.setDimLevelPercent(99);
    expect(settings.dimLevelPercent, maxDimLevelPercent);
  });
}
