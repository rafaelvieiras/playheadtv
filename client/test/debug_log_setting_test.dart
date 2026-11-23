// The on-screen log toggle is remembered across runs: null means the viewer
// never touched it, which lets a log peek build start with the log drawn and a
// plain release start with nothing to draw.
import 'package:client/services/settings_service.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  test('unset until the viewer toggles it', () async {
    expect((await SettingsService.create()).debugLogVisible, isNull);
  });

  test('keeps the choice across runs, including off', () async {
    await (await SettingsService.create()).setDebugLogVisible(false);
    expect((await SettingsService.create()).debugLogVisible, isFalse);

    await (await SettingsService.create()).setDebugLogVisible(true);
    expect((await SettingsService.create()).debugLogVisible, isTrue);
  });
}
