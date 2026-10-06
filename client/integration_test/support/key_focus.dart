// Keeps key events reaching the player page in the integration suites.
//
// When the test window loses OS focus (another window on the desktop takes
// it) Flutter drops primary focus to the root focus scope, and the player's
// `KeyboardListener` stops receiving key events although `HardwareKeyboard`
// still sees them. Suites must therefore make sure the player's node holds
// focus before sending a key, not only once after boot.
import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';

/// Re-requests focus on the player's `KeyboardListener` when primary focus
/// has fallen to a scope (or is missing). A no-op while some real node holds
/// focus (an open picker, a text field) or when no player page is mounted.
Future<void> ensurePlayerFocus(WidgetTester tester) async {
  final primary = FocusManager.instance.primaryFocus;
  if (primary != null && primary is! FocusScopeNode) return;
  final listeners = find.byWidgetPredicate((w) => w is KeyboardListener && w.autofocus);
  if (listeners.evaluate().isEmpty) return;
  tester.widget<KeyboardListener>(listeners.first).focusNode.requestFocus();
  await tester.pump();
}

/// `tester.sendKeyEvent` preceded by [ensurePlayerFocus].
Future<void> pressKey(WidgetTester tester, LogicalKeyboardKey key) async {
  await ensurePlayerFocus(tester);
  await tester.sendKeyEvent(key);
}
