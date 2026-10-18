// Unit tests for the device identity helper and the persisted "chosen server
// URL" added to SettingsService for first-run pairing.
import 'package:client/services/device_identity.dart';
import 'package:client/services/settings_service.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  group('DeviceIdentity', () {
    test('maps the running OS to a backend platform', () {
      expect(DeviceIdentity.platform(operatingSystem: 'linux'), 'linux');
      expect(DeviceIdentity.platform(operatingSystem: 'windows'), 'windows');
      expect(DeviceIdentity.platform(operatingSystem: 'android'), 'android');
      expect(DeviceIdentity.platform(operatingSystem: 'android', isAndroidTv: true), 'android_tv');
      expect(DeviceIdentity.platform(operatingSystem: 'ios'), 'ios');
      expect(DeviceIdentity.platform(operatingSystem: 'macos'), 'ios');
      expect(DeviceIdentity.platform(operatingSystem: 'fuchsia'), 'linux');
      expect(DeviceIdentity.platform(operatingSystem: 'LINUX'), 'linux');
    });

    test('every mapped platform is one the backend accepts', () {
      for (final os in ['linux', 'windows', 'android', 'ios', 'macos', 'fuchsia', '']) {
        expect(backendPlatforms, contains(DeviceIdentity.platform(operatingSystem: os)));
      }
    });

    test('name falls back to the default and is bounded/sanitised', () {
      expect(DeviceIdentity.name(hostname: ''), DeviceIdentity.defaultName);
      expect(DeviceIdentity.name(hostname: '   '), DeviceIdentity.defaultName);
      expect(DeviceIdentity.name(hostname: 'Living Room TV'), 'Living Room TV');
      expect(DeviceIdentity.name(hostname: 'a' * 200).length, DeviceIdentity.maxNameLength);
      expect(DeviceIdentity.name(hostname: 'evil\nname\t'), isNot(contains('\n')));
    });
  });

  group('SettingsService.chosenServerUrl', () {
    setUp(() => SharedPreferences.setMockInitialValues({}));

    test('is empty until set and survives a round trip', () async {
      final settings = await SettingsService.create();
      expect(settings.chosenServerUrl, '');
      await settings.setChosenServerUrl('http://example.test');
      final reloaded = await SettingsService.create();
      expect(reloaded.chosenServerUrl, 'http://example.test');
    });

    test('clearing it removes the stored value', () async {
      final settings = await SettingsService.create();
      await settings.setChosenServerUrl('http://example.test');
      await settings.setChosenServerUrl('');
      expect(settings.chosenServerUrl, '');
      final reloaded = await SettingsService.create();
      expect(reloaded.chosenServerUrl, '');
    });
  });
}
