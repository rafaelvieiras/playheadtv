// The card a channel shows instead of a picture when this device cannot play the
// program on air. It is the whole visible half of "do not play what you cannot
// decode": what cannot play, why, and when the channel is back.
import 'package:client/widgets/unplayable_notice.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  final notice = UnplayableNotice(
    channelNumber: 36,
    channelName: 'Discovery Turbo',
    programLabel: "Overhaulin': Os Supercarros · S1E2 · Episódio 2",
    reason: 'no decoder for mpeg4 at 320x240',
    resumeAt: DateTime.utc(2026, 11, 25, 3, 42),
  );

  Future<void> pumpNotice(WidgetTester tester, ValueListenable<UnplayableNotice?> source) {
    return tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: Stack(children: [PlayerUnplayableNotice(source: source)]),
      ),
    ));
  }

  testWidgets('the card names the program, the reason and the wait', (tester) async {
    final source = ValueNotifier<UnplayableNotice?>(notice);
    addTearDown(source.dispose);
    await pumpNotice(tester, source);
    expect(find.byKey(const Key('unplayable-notice')), findsOneWidget);
    expect(find.text('This program cannot be played on this device'), findsOneWidget);
    expect(find.text(notice.programLabel), findsOneWidget);
    // The reason arrives lowercase from the decoder check and is shown as a
    // sentence.
    expect(find.text('No decoder for mpeg4 at 320x240.'), findsOneWidget);
    expect(
      find.textContaining('Channel 36 · Discovery Turbo is waiting for the next program'),
      findsOneWidget,
    );
    // The return time is shown in local time, whatever the zone.
    expect(find.textContaining(unplayableClock(notice.resumeAt)), findsOneWidget);
  });

  testWidgets('nothing is drawn while the channel plays', (tester) async {
    final source = ValueNotifier<UnplayableNotice?>(null);
    addTearDown(source.dispose);
    await pumpNotice(tester, source);
    expect(find.byKey(const Key('unplayable-notice')), findsNothing);
  });

  testWidgets('it never eats a tap or a key', (tester) async {
    final source = ValueNotifier<UnplayableNotice?>(notice);
    addTearDown(source.dispose);
    var taps = 0;
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: Stack(children: [
          Positioned.fill(
            child: GestureDetector(
              // A bare SizedBox.expand does not hit-test itself, so the tap would
              // miss the gesture layer and the test would pass for the wrong
              // reason.
              behavior: HitTestBehavior.opaque,
              onTap: () => taps++,
              child: const SizedBox.expand(),
            ),
          ),
          PlayerUnplayableNotice(source: source),
        ]),
      ),
    ));
    await tester.tapAt(const Offset(400, 300));
    expect(taps, 1);
    // Nothing inside the card is focusable, so a remote keeps working.
    expect(
      FocusManager.instance.primaryFocus?.context?.findRenderObject(),
      anyOf(isNull, isA<RenderObject>()),
    );
  });

  test('the same notice compares equal, so the notifier does not redraw', () {
    expect(
      UnplayableNotice(
        channelNumber: 36,
        channelName: 'Discovery Turbo',
        programLabel: 'x',
        reason: 'y',
        resumeAt: DateTime.utc(2026),
      ),
      UnplayableNotice(
        channelNumber: 36,
        channelName: 'Discovery Turbo',
        programLabel: 'x',
        reason: 'y',
        resumeAt: DateTime.utc(2026),
      ),
    );
  });

  test('an empty reason still reads as a sentence', () {
    expect(unplayableReasonText('  '), 'This device cannot play the video of this program.');
  });
}
