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

/// Whether primary focus sits on the widget keyed [key], or inside it.
///
/// A page builds before it takes focus, and a device widens that gap enough for
/// the first key presses to be swallowed: the suite then asserts about a row
/// that never received them (the lab saw the full screen row still reading On
/// after Enter). Wait on this before pressing.
bool focusIsInside(Key key) {
  final primary = FocusManager.instance.primaryFocus;
  final context = primary?.context;
  if (primary == null || primary is FocusScopeNode || context == null) return false;
  if (context.widget.key == key) return true;
  var found = false;
  context.visitAncestorElements((e) {
    if (e.widget.key == key) {
      found = true;
      return false;
    }
    return true;
  });
  return found;
}
