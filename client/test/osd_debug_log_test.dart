// The on-screen log has an OSD button in a log peek build: it turns the lines
// off and on while the app keeps logging. Without the peek define the build has
// no such control, and a plain release is untouched.
import 'package:client/widgets/osd_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  final start = DateTime.utc(2030, 1, 1, 12, 0);

  Widget osd({
    bool showDebugLogButton = false,
    ValueNotifier<bool>? debugLogVisible,
    VoidCallback? onToggleDebugLog,
  }) =>
      MaterialApp(
        home: Scaffold(
          body: Stack(children: [
            ChannelOsd(
              key: const Key('osd'),
              channelNumber: 1,
              channelName: 'Movies',
              channelArtUrl: null,
              itemLabel: 'A Film',
              nextItemLabel: null,
              startAt: start,
              endAt: start.add(const Duration(minutes: 10)),
              progressByClock: true,
              controller: null,
              focusMode: ValueNotifier<bool>(false),
              onOpenSubtitles: () {},
              onOpenAudio: () {},
              onToggleDimmer: () {},
              onOpenDimmerLevel: () {},
              showDebugLogButton: showDebugLogButton,
              debugLogVisible: debugLogVisible,
              onToggleDebugLog: onToggleDebugLog,
              recentChannels: const [],
              onSelectChannel: (_) {},
              now: () => start.add(const Duration(seconds: 7)),
            ),
          ]),
        ),
      );

  testWidgets('a build without the log draw no toggle at all', (tester) async {
    await tester.pumpWidget(osd());
    await tester.pump(const Duration(milliseconds: 600));
    expect(find.byKey(const Key('osd-btn-debug-log')), findsNothing);
  });

  testWidgets('the toggle says what it will do and reports a tap', (tester) async {
    var taps = 0;
    await tester.pumpWidget(osd(
      showDebugLogButton: true,
      debugLogVisible: ValueNotifier<bool>(true),
      onToggleDebugLog: () => taps++,
    ));
    await tester.pump(const Duration(milliseconds: 600));

    expect(find.byKey(const Key('osd-btn-debug-log')), findsOneWidget);
    expect(find.text('Logs on'), findsOneWidget);

    await tester.tap(find.byKey(const Key('osd-btn-debug-log')));
    await tester.pump();
    expect(taps, 1);
  });

  testWidgets('the label follows the current state', (tester) async {
    final visible = ValueNotifier<bool>(false);
    addTearDown(visible.dispose);
    await tester.pumpWidget(osd(showDebugLogButton: true, debugLogVisible: visible, onToggleDebugLog: () {}));
    await tester.pump(const Duration(milliseconds: 600));
    expect(find.text('Logs off'), findsOneWidget);

    // The OSD re-reads the state on its own tick, so a change from outside
    // reaches the label without the parent rebuilding the widget.
    visible.value = true;
    await tester.pump(const Duration(milliseconds: 600));
    expect(find.text('Logs on'), findsOneWidget);
  });
}
