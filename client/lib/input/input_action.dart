import 'package:flutter/services.dart';

import '../services/settings_service.dart';

/// Every navigation/control action of the app, remappable in Settings
/// ("Remap controls"). See KeyBindingsService.resolve, called from main.dart,
/// for how each action is dispatched.
enum InputAction {
  channelNext,
  channelPrevious,
  select,
  back,
  home,
  digit0,
  digit1,
  digit2,
  digit3,
  digit4,
  digit5,
  digit6,
  digit7,
  digit8,
  digit9,
  debugOverlay,
}

extension InputActionDigit on InputAction {
  /// '0'-'9' if this is a digit action, otherwise null.
  String? get digitValue {
    const digits = {
      InputAction.digit0: '0',
      InputAction.digit1: '1',
      InputAction.digit2: '2',
      InputAction.digit3: '3',
      InputAction.digit4: '4',
      InputAction.digit5: '5',
      InputAction.digit6: '6',
      InputAction.digit7: '7',
      InputAction.digit8: '8',
      InputAction.digit9: '9',
    };
    return digits[this];
  }
}

/// Factory-default bindings. `select` (Enter) has 2 stages: the first press
/// shows the OSD; the second press (with the OSD already visible) opens the
/// full-screen EPG.
const Map<InputAction, LogicalKeyboardKey> defaultKeyBindings = {
  InputAction.channelNext: LogicalKeyboardKey.arrowUp,
  InputAction.channelPrevious: LogicalKeyboardKey.arrowDown,
  InputAction.select: LogicalKeyboardKey.enter,
  InputAction.back: LogicalKeyboardKey.escape,
  InputAction.home: LogicalKeyboardKey.home,
  InputAction.digit0: LogicalKeyboardKey.digit0,
  InputAction.digit1: LogicalKeyboardKey.digit1,
  InputAction.digit2: LogicalKeyboardKey.digit2,
  InputAction.digit3: LogicalKeyboardKey.digit3,
  InputAction.digit4: LogicalKeyboardKey.digit4,
  InputAction.digit5: LogicalKeyboardKey.digit5,
  InputAction.digit6: LogicalKeyboardKey.digit6,
  InputAction.digit7: LogicalKeyboardKey.digit7,
  InputAction.digit8: LogicalKeyboardKey.digit8,
  InputAction.digit9: LogicalKeyboardKey.digit9,
  InputAction.debugOverlay: LogicalKeyboardKey.keyD,
};

/// Resolves LogicalKeyboardKey -> InputAction by combining the default +
/// the persisted override (SettingsService). The action is persisted by
/// `InputAction.name` (the enum member's name, e.g. "channelNext").
class KeyBindingsService {
  final SettingsService settings;

  KeyBindingsService(this.settings);

  Map<InputAction, LogicalKeyboardKey> get effectiveBindings {
    final result = <InputAction, LogicalKeyboardKey>{};
    for (final action in InputAction.values) {
      final overrideKeyId = settings.getKeyBindingKeyId(action.name);
      result[action] = overrideKeyId != null ? LogicalKeyboardKey(overrideKeyId) : defaultKeyBindings[action]!;
    }
    return result;
  }

  InputAction? resolve(LogicalKeyboardKey key) {
    final bindings = effectiveBindings;
    for (final entry in bindings.entries) {
      if (entry.value == key) return entry.key;
    }
    return null;
  }

  Future<void> rebind(InputAction action, LogicalKeyboardKey key) => settings.setKeyBinding(action.name, key.keyId);

  Future<void> restoreDefaults() => settings.clearKeyBindings(InputAction.values.map((a) => a.name));
}
