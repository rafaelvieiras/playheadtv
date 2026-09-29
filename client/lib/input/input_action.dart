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
  // Tier 1 stream selector (issue #3 / AGENTS.md "6-button baseline"): the
  // required 6-button path — opens the on-screen track picker overlay (see
  // widgets/track_selector_overlay.dart). Has a default binding, unlike the
  // two accelerators below.
  trackSelector,
  // Optional power-user accelerators: only shortcut the same outcome the
  // picker above already reaches with 6 buttons (per AGENTS.md's accelerator
  // exception) — no default binding on purpose, see defaultKeyBindings' doc
  // comment and KeyBindingsService.effectiveBindings for how an action with
  // no binding at all is a supported state.
  cycleAudioTrack,
  cycleSubtitleTrack,
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
///
/// Not every InputAction has an entry here — `cycleAudioTrack`/
/// `cycleSubtitleTrack` (the optional accelerators, see the enum's doc
/// comments) are deliberately absent: "no binding at all until the user
/// assigns one" is a supported state, handled by `effectiveBindings`/
/// `resolve` below, not a bug. Never add a `[action]!` non-null assertion
/// against this map elsewhere — read via `effectiveBindings`/`resolve`
/// instead, which already treat a missing entry as "unbound".
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
  InputAction.trackSelector: LogicalKeyboardKey.arrowRight,
};

/// Resolves LogicalKeyboardKey -> InputAction by combining the default +
/// the persisted override (SettingsService). The action is persisted by
/// `InputAction.name` (the enum member's name, e.g. "channelNext").
class KeyBindingsService {
  final SettingsService settings;

  KeyBindingsService(this.settings);

  /// An InputAction with neither a persisted override nor a
  /// `defaultKeyBindings` entry simply doesn't appear in the returned map —
  /// it never resolves from any key until the user assigns one from Settings
  /// -> Remap controls (`rebind`). Every action with a real default (the
  /// overwhelming majority) is unaffected: this only changes behavior for an
  /// action that was deliberately left out of `defaultKeyBindings`.
  Map<InputAction, LogicalKeyboardKey> get effectiveBindings {
    final result = <InputAction, LogicalKeyboardKey>{};
    for (final action in InputAction.values) {
      final overrideKeyId = settings.getKeyBindingKeyId(action.name);
      if (overrideKeyId != null) {
        result[action] = LogicalKeyboardKey(overrideKeyId);
        continue;
      }
      final defaultKey = defaultKeyBindings[action];
      if (defaultKey != null) result[action] = defaultKey;
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
