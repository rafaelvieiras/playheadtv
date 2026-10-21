// Key presses for the performance suites, which also run in profile mode.
//
// `WidgetTester.sendKeyEvent` finds the physical key by its `debugName`, and
// that name only exists in debug builds, so it throws in profile/release. This
// sends the same event with the physical key given explicitly.
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

import 'key_focus.dart';

PhysicalKeyboardKey _physicalOf(LogicalKeyboardKey key) {
  if (key == LogicalKeyboardKey.enter) return PhysicalKeyboardKey.enter;
  if (key == LogicalKeyboardKey.arrowUp) return PhysicalKeyboardKey.arrowUp;
  if (key == LogicalKeyboardKey.arrowDown) return PhysicalKeyboardKey.arrowDown;
  if (key == LogicalKeyboardKey.arrowLeft) return PhysicalKeyboardKey.arrowLeft;
  if (key == LogicalKeyboardKey.arrowRight) return PhysicalKeyboardKey.arrowRight;
  throw ArgumentError('perf_keys: no physical key mapped for $key');
}

/// Presses and releases [key] on the focused widget, after making sure the
/// player has focus (see key_focus.dart). Passing the physical key skips the
/// debug-name lookup that fails outside debug builds.
Future<void> perfPressKey(WidgetTester tester, LogicalKeyboardKey key) async {
  await ensurePlayerFocus(tester);
  await tester.sendKeyEvent(key, physicalKey: _physicalOf(key));
}
