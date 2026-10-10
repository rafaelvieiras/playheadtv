// The OSD's elapsed time follows where the picture is in the schedule, not the
// wall clock: a program that begins reads 0:00 even when the wall clock is
// ahead of the picture (late transition within the sync tolerance, seek or
// buffering after a resync).
import 'package:client/osd_subject.dart';
import 'package:client/widgets/osd_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  final start = DateTime.utc(2030, 1, 1, 12, 0);

  group('picturePlayheadAt', () {
    test('is the entry start plus the played time', () {
      expect(picturePlayheadAt(start, Duration.zero, const Duration(minutes: 20), const Duration(seconds: 3)), start.add(const Duration(seconds: 3)));
    });

    test('subtracts the file offset of a split segment or a mid-item unit', () {
      expect(
        picturePlayheadAt(start, const Duration(minutes: 10), const Duration(minutes: 10), const Duration(minutes: 10, seconds: 4)),
        start.add(const Duration(seconds: 4)),
      );
    });

    test('never leaves the unit', () {
      expect(picturePlayheadAt(start, const Duration(seconds: 5), const Duration(seconds: 30), Duration.zero), start);
      expect(picturePlayheadAt(start, Duration.zero, const Duration(seconds: 30), const Duration(minutes: 5)), start.add(const Duration(seconds: 30)));
    });
  });

  Widget osd({DateTime? Function()? playhead}) => MaterialApp(
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
              playhead: playhead,
              controller: null,
              focusMode: ValueNotifier<bool>(false),
              onOpenSubtitles: () {},
              onOpenAudio: () {},
              onToggleDimmer: () {},
              onOpenDimmerLevel: () {},
              recentChannels: const [],
              onSelectChannel: (_) {},
              // The wall clock is 7 s past the program's start.
              now: () => start.add(const Duration(seconds: 7)),
            ),
          ]),
        ),
      );

  String elapsed(WidgetTester tester) => tester.widget<Text>(find.byKey(const Key('osd-elapsed'))).data!.split(' / ').first;

  testWidgets('shows the picture position when it lags the wall clock', (tester) async {
    await tester.pumpWidget(osd(playhead: () => start.add(const Duration(milliseconds: 300))));
    expect(elapsed(tester), '0:00');
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('falls back to the wall clock when the picture position is unknown', (tester) async {
    await tester.pumpWidget(osd(playhead: () => null));
    expect(elapsed(tester), '0:07');
    await tester.pumpWidget(osd());
    expect(elapsed(tester), '0:07');
    await tester.pumpWidget(const SizedBox());
  });
}
