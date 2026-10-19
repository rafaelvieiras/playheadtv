import 'package:client/input/back_key_guard.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

Future<List<LogicalKeyboardKey>> _keysReachingTheApp(WidgetTester tester, LogicalKeyboardKey key) async {
  final seen = <LogicalKeyboardKey>[];
  await tester.pumpWidget(
    MaterialApp(
      home: Focus(
        // Stands in for the platform: whatever reaches it was not consumed.
        onKeyEvent: (node, event) {
          seen.add(event.logicalKey);
          return KeyEventResult.ignored;
        },
        child: BackKeyGuard(
          // A page that listens but never handles, like a KeyboardListener.
          child: Focus(autofocus: true, child: const SizedBox()),
        ),
      ),
    ),
  );
  await tester.sendKeyDownEvent(key, platform: 'android', physicalKey: PhysicalKeyboardKey.escape);
  await tester.sendKeyUpEvent(key, platform: 'android', physicalKey: PhysicalKeyboardKey.escape);
  return seen;
}

void main() {
  testWidgets('a Back key nobody handled is consumed by the guard', (tester) async {
    expect(await _keysReachingTheApp(tester, LogicalKeyboardKey.goBack), isEmpty);
  });

  testWidgets('Esc and the gamepad B button are consumed too', (tester) async {
    expect(await _keysReachingTheApp(tester, LogicalKeyboardKey.escape), isEmpty);
    expect(await _keysReachingTheApp(tester, LogicalKeyboardKey.gameButtonB), isEmpty);
  });

  testWidgets('other keys pass through the guard', (tester) async {
    expect(await _keysReachingTheApp(tester, LogicalKeyboardKey.arrowDown), isNotEmpty);
  });
}
