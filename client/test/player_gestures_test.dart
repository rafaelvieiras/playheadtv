import 'package:client/widgets/player_gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  late List<String> events;
  setUp(() => events = []);

  Widget harness({bool enabled = true, Widget? above}) => MaterialApp(
    home: Scaffold(
      backgroundColor: Colors.black,
      body: Stack(
        children: [
          Positioned.fill(
            child: PlayerGestureLayer(
              enabled: enabled,
              onTapPicture: () => events.add('tap'),
              onHorizontalFling: (direction) => events.add('fling:$direction'),
              onSwipeUp: () => events.add('up'),
            ),
          ),
          if (above != null) above,
        ],
      ),
    ),
  );

  testWidgets('a tap on the picture is a tap, not a fling', (tester) async {
    await tester.pumpWidget(harness());
    await tester.tap(find.byType(PlayerGestureLayer));
    await tester.pump();
    expect(events, ['tap']);
  });

  testWidgets('swiping left is the next channel, swiping right the previous one', (tester) async {
    await tester.pumpWidget(harness());
    await tester.fling(find.byType(PlayerGestureLayer), const Offset(-320, 0), 1200);
    await tester.pumpAndSettle();
    await tester.fling(find.byType(PlayerGestureLayer), const Offset(320, 0), 1200);
    await tester.pumpAndSettle();
    expect(events, ['fling:1', 'fling:-1']);
  });

  testWidgets('a slow horizontal drag is not a channel change', (tester) async {
    await tester.pumpWidget(harness());
    // Below gestureFlingVelocity: a finger that slipped, or a drag meant for
    // something else, must not switch channel.
    await tester.timedDrag(find.byType(PlayerGestureLayer), const Offset(-120, 0), const Duration(seconds: 2));
    await tester.pumpAndSettle();
    expect(events, isEmpty);
  });

  testWidgets('a swipe up opens the guide; a swipe down does nothing', (tester) async {
    await tester.pumpWidget(harness());
    await tester.fling(find.byType(PlayerGestureLayer), const Offset(0, 320), 1200);
    await tester.pumpAndSettle();
    expect(events, isEmpty, reason: 'a downward swipe has no meaning');
    await tester.fling(find.byType(PlayerGestureLayer), const Offset(0, -320), 1200);
    await tester.pumpAndSettle();
    expect(events, ['up']);
  });

  testWidgets('disabled, it swallows nothing and does nothing', (tester) async {
    await tester.pumpWidget(harness(
      enabled: false,
      above: Align(
        alignment: Alignment.center,
        child: GestureDetector(
          onTap: () => events.add('above'),
          child: Container(key: const Key('above'), width: 120, height: 120, color: Colors.white10),
        ),
      ),
    ));
    await tester.tap(find.byKey(const Key('above')));
    await tester.fling(find.byType(Scaffold), const Offset(-320, 0), 1200);
    await tester.pumpAndSettle();
    expect(events, ['above']);
  });

  testWidgets('a widget above the layer keeps its own taps', (tester) async {
    // The OSD, the spinner and the guide all sit above this layer in the
    // player's Stack; this is the ordering that has to hold.
    await tester.pumpWidget(harness(
      above: Align(
        alignment: Alignment.bottomCenter,
        child: GestureDetector(
          onTap: () => events.add('panel'),
          child: Container(key: const Key('panel'), width: 200, height: 120, color: Colors.white10),
        ),
      ),
    ));
    await tester.tap(find.byKey(const Key('panel')));
    await tester.pump();
    expect(events, ['panel']);
    // And the picture above it is still a tap on the picture.
    await tester.tapAt(const Offset(60, 60));
    await tester.pump();
    expect(events, ['panel', 'tap']);
  });
}
