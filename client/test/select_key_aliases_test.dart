import 'package:client/input/input_action.dart';
import 'package:client/services/settings_service.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

Future<KeyBindingsService> _service() async {
  SharedPreferences.setMockInitialValues({});
  return KeyBindingsService(SettingsService(await SharedPreferences.getInstance()));
}

void main() {
  backAliasTests();
  test('the D-pad centre button and other activate keys resolve to select', () async {
    final keys = await _service();
    for (final key in [
      LogicalKeyboardKey.enter,
      LogicalKeyboardKey.select,
      LogicalKeyboardKey.numpadEnter,
      LogicalKeyboardKey.gameButtonA,
    ]) {
      expect(keys.resolve(key), InputAction.select, reason: key.keyLabel);
    }
  });

  test('a key the user bound to another action keeps that action', () async {
    final keys = await _service();
    await keys.rebind(InputAction.debugOverlay, LogicalKeyboardKey.select);
    expect(keys.resolve(LogicalKeyboardKey.select), InputAction.debugOverlay);
  });

  test('unrelated keys do not become select', () async {
    final keys = await _service();
    expect(keys.resolve(LogicalKeyboardKey.arrowLeft), isNull);
    expect(keys.resolve(LogicalKeyboardKey.keyQ), isNull);
  });
}

void backAliasTests() {
  test('the remote Back button and the gamepad B button resolve to back', () async {
    final keys = await _service();
    for (final key in [LogicalKeyboardKey.escape, LogicalKeyboardKey.goBack, LogicalKeyboardKey.gameButtonB]) {
      expect(keys.resolve(key), InputAction.back, reason: key.keyLabel);
    }
  });

  test('a key bound to another action keeps that action over the back aliases', () async {
    final keys = await _service();
    await keys.rebind(InputAction.debugOverlay, LogicalKeyboardKey.goBack);
    expect(keys.resolve(LogicalKeyboardKey.goBack), InputAction.debugOverlay);
  });
}
