import 'package:client/services/loudness_service.dart';
import 'package:client/services/settings_service.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

// An install that updated from the fixed pre-amp model carries
// `loudness_target = -18` (the old default, chosen next to a +12 dB pre-amp) and
// no `loudness_max_boost` at all. That -18 is the old policy, not a choice, and
// keeping it would leave every quiet item 4 dB under the target the new model
// aims at: measured on a device, the boost stopped at +11.9 dB where the new
// target asks for +15.
void main() {
  test('a target from the old model is replaced by the new default once', () async {
    SharedPreferences.setMockInitialValues({'flutter.loudness_target': -18.0});
    final settings = await SettingsService.create();
    expect(settings.loudnessTargetLufs, loudnessTargetLufsDefault);
  });

  test('a target chosen under the new model is kept, -18 included', () async {
    SharedPreferences.setMockInitialValues({
      'flutter.loudness_target': -18.0,
      'flutter.loudness_max_boost': 15.0,
    });
    final settings = await SettingsService.create();
    expect(settings.loudnessTargetLufs, -18.0);
  });

  test('any other stored target is the viewer\u2019s own', () async {
    SharedPreferences.setMockInitialValues({'flutter.loudness_target': -22.0});
    final settings = await SettingsService.create();
    expect(settings.loudnessTargetLufs, -22.0);
  });

  test('nothing stored means the installation default', () async {
    SharedPreferences.setMockInitialValues({});
    final settings = await SettingsService.create();
    expect(settings.loudnessTargetLufs, isNull);
    expect(readLoudnessTarget(const {}), loudnessTargetLufsDefault);
  });
}
