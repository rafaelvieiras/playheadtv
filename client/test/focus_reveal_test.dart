import 'package:client/widgets/focus_reveal.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

// Guards the fix for docs/ROADMAP.md "the focus that stops moving after a
// resize", measured in the failing GUI suite: 13 rows of 104 px in a
// 2560x1440 window, the focus on the last row, then a shrink to 1280x720. After
// it, Up and Down moved nothing and the page opened the wrong screen.
//
// Two decisions make that impossible, and each is pinned here:
//
//   - the settings-like pages build every row (SingleChildScrollView + Column).
//     Their lists are small and fixed, so a lazy ListView bought nothing and
//     cost this: a row the viewport drops is a row the focus cannot come back
//     to, and the suite measured exactly that (`lastRowRect=null` while the
//     focus still reported the row);
//   - FocusReveal keeps the focused row on screen, including across a resize,
//     which is the case with no focus change to react to.

const double _rowHeight = 104;

List<FocusNode> _nodes(int count) => [for (var i = 0; i < count; i++) FocusNode(debugLabel: 'row-$i')];

Widget _page(List<FocusNode> nodes) => MaterialApp(
      home: Scaffold(
        body: FocusReveal(
          child: SingleChildScrollView(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                for (var i = 0; i < nodes.length; i++)
                  SizedBox(
                    height: _rowHeight,
                    child: Focus(
                      key: ValueKey('row-$i'),
                      focusNode: nodes[i],
                      child: Text('row $i'),
                    ),
                  ),
              ],
            ),
          ),
        ),
      ),
    );

Future<void> _window(WidgetTester tester, Size size) async {
  tester.view.devicePixelRatio = 1.0;
  tester.view.physicalSize = size;
  await tester.pumpAndSettle();
}

void main() {
  late List<FocusNode> nodes;

  setUp(() => nodes = _nodes(13));
  tearDown(() {
    for (final node in nodes) {
      node.dispose();
    }
  });

  testWidgets('every row is built, so the focus survives a shrink that hides it', (tester) async {
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await _window(tester, const Size(800, 1440));
    await tester.pumpWidget(_page(nodes));
    await tester.pumpAndSettle();

    nodes.last.requestFocus();
    await tester.pumpAndSettle();

    await _window(tester, const Size(800, 720));
    expect(find.byKey(const ValueKey('row-12')).evaluate(), isNotEmpty,
        reason: 'the row the focus is on is still in the tree after the shrink');
    expect(FocusManager.instance.primaryFocus, nodes.last);

    await tester.sendKeyEvent(LogicalKeyboardKey.arrowUp);
    await tester.pumpAndSettle();
    expect(FocusManager.instance.primaryFocus, nodes[11], reason: 'the walk continues across the resize');
  });

  testWidgets('the resize reveals the row that stayed focused, so it is on screen', (tester) async {
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await _window(tester, const Size(800, 1440));
    await tester.pumpWidget(_page(nodes));
    await tester.pumpAndSettle();

    nodes.last.requestFocus();
    await tester.pumpAndSettle();
    final scrollable = tester.state<ScrollableState>(find.byType(Scrollable).first);
    expect(scrollable.position.pixels, 0, reason: 'everything fits, so nothing scrolled yet');

    // 13 rows of 104 px in 720 px: the focused row is below the viewport unless
    // the reveal ran for the resize, and a viewer on a remote would see nothing.
    await _window(tester, const Size(800, 720));
    expect(scrollable.position.pixels, greaterThan(0));
    final row = tester.getRect(find.byKey(const ValueKey('row-12')));
    expect(row.top, lessThan(720));
    expect(row.bottom, greaterThan(0));
  });

  testWidgets('moving the focus reveals the row it landed on', (tester) async {
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await _window(tester, const Size(800, 300));
    await tester.pumpWidget(_page(nodes));
    await tester.pumpAndSettle();

    final scrollable = tester.state<ScrollableState>(find.byType(Scrollable).first);
    nodes.first.requestFocus();
    await tester.pumpAndSettle();
    final restingOffset = scrollable.position.pixels;

    // Down past the bottom of the viewport: the list follows the focus.
    for (var i = 0; i < 5; i++) {
      await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
      await tester.pumpAndSettle();
    }
    expect(FocusManager.instance.primaryFocus, nodes[5]);
    expect(scrollable.position.pixels, greaterThan(restingOffset));
    final row = tester.getRect(find.byKey(const ValueKey('row-5')));
    expect(row.top, greaterThanOrEqualTo(0));
    expect(row.bottom, lessThanOrEqualTo(300));
  });

  testWidgets('it leaves a focus outside its own subtree alone', (tester) async {
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await _window(tester, const Size(800, 300));

    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: Column(
          children: [
            Expanded(
              child: FocusReveal(
                child: SingleChildScrollView(
                  child: Column(children: [
                    for (var i = 0; i < 6; i++)
                      SizedBox(
                        height: _rowHeight,
                        child: Focus(key: ValueKey('a-$i'), child: Text('a $i')),
                      ),
                  ]),
                ),
              ),
            ),
            SizedBox(
              height: 100,
              child: Focus(key: const ValueKey('b-0'), autofocus: true, child: const Text('b 0')),
            ),
          ],
        ),
      ),
    ));
    await tester.pumpAndSettle();

    final scrollable = tester.state<ScrollableState>(find.byType(Scrollable).first);
    expect(scrollable.position.pixels, 0, reason: "the focus is not this list's to reveal");
  });
}
