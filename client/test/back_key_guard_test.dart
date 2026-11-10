// Back (Escape / Android Back) inside a dialog.
//
// The app wraps the whole Navigator in `BackKeyGuard` (lib/input/back_key_guard.dart)
// so the platform default can never finish the activity. A guard that returns
// `handled` for every Back also swallows the key before Flutter's own
// `DismissAction` can close an open dialog, which is how "Clear the offline
// cache?" stayed open on Escape in settings_offline_test.
import 'package:client/input/back_key_guard.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

Widget app({required Widget home, bool guard = true}) => MaterialApp(
  theme: ThemeData.dark(),
  builder: (context, child) => guard ? BackKeyGuard(child: child!) : child!,
  home: home,
);

/// A page shaped like the offline cache one: a focused row that opens a
/// confirmation dialog whose Cancel button asks for focus.
class RowAndDialog extends StatelessWidget {
  const RowAndDialog({super.key});

  @override
  Widget build(BuildContext context) => Scaffold(
    body: ListView(
      children: [
        ListTile(
          key: const Key('the-row'),
          autofocus: true,
          title: const Text('Clear cache'),
          onTap: () => showDialog<bool>(
            context: context,
            builder: (context) => AlertDialog(
              key: const Key('the-dialog'),
              title: const Text('Clear the offline cache?'),
              actions: [
                TextButton(
                  key: const Key('the-cancel'),
                  autofocus: true,
                  onPressed: () => Navigator.of(context).pop(false),
                  child: const Text('Cancel'),
                ),
                TextButton(onPressed: () => Navigator.of(context).pop(true), child: const Text('Clear')),
              ],
            ),
          ),
        ),
      ],
    ),
  );
}

void main() {
  testWidgets('the guard lets an open dialog handle Back', (tester) async {
    await tester.pumpWidget(app(home: const RowAndDialog()));
    await tester.tap(find.byKey(const Key('the-row')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('the-dialog')), findsOneWidget);
    await tester.pump();

    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('the-dialog')), findsNothing, reason: 'Escape closes the dialog it opened');
  });

  testWidgets('without the guard the same page closes it', (tester) async {
    // The control: this is what Flutter does on its own, and what the guard is
    // changing. If this ever fails, the diagnosis is wrong, not the guard.
    await tester.pumpWidget(app(home: const RowAndDialog(), guard: false));
    await tester.tap(find.byKey(const Key('the-row')));
    await tester.pumpAndSettle();
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('the-dialog')), findsNothing);
  });

  testWidgets('a popup that cannot be dismissed keeps the guard in charge', (tester) async {
    // Nothing would close it, so the key must not sail past the guard to the
    // platform default either.
    await tester.pumpWidget(
      MaterialApp(
        theme: ThemeData.dark(),
        builder: (context, child) => BackKeyGuard(child: child!),
        home: Builder(
          builder: (context) => Scaffold(
            body: Center(
              child: TextButton(
                autofocus: true,
                onPressed: () => showDialog<void>(
                  context: context,
                  barrierDismissible: false,
                  builder: (context) => const AlertDialog(key: Key('stuck-dialog'), title: Text('Working')),
                ),
                child: const Text('start'),
              ),
            ),
          ),
        ),
      ),
    );
    await tester.tap(find.text('start'));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('stuck-dialog')), findsOneWidget);
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('stuck-dialog')), findsOneWidget, reason: 'it is not the guard\'s to close');
  });

  testWidgets('the guard still eats a Back that nothing handled', (tester) async {
    // Its reason to exist: no route takes the key, and the platform default
    // (finish the activity / pop the last route) must not run.
    var popped = false;
    await tester.pumpWidget(
      MaterialApp(
        theme: ThemeData.dark(),
        builder: (context, child) => BackKeyGuard(child: child!),
        home: Builder(
          builder: (context) => Scaffold(
            body: Center(
              child: TextButton(
                autofocus: true,
                onPressed: () {},
                child: const Text('only row'),
              ),
            ),
          ),
        ),
      ),
    );
    await tester.pump();
    final route = ModalRoute.of(tester.element(find.text('only row')));
    route!.addLocalHistoryEntry(LocalHistoryEntry(onRemove: () => popped = true));
    await tester.sendKeyEvent(LogicalKeyboardKey.escape);
    await tester.pumpAndSettle();
    expect(popped, isFalse, reason: 'the guard kept the local history entry from being popped');
  });
}
