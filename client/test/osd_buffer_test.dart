// The OSD's progress bar shows how much of the program is buffered, not only how
// much has played: a picture that stops with a healthy buffer is a decode
// problem, one that stops at the edge of the buffer is the network. That
// difference is the whole point of the extra segment.
import 'package:client/video/video_engine.dart';
import 'package:client/widgets/osd_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

class _FakePlayback implements VideoPlayback {
  _FakePlayback(this._state);
  final PlaybackState _state;

  @override
  PlaybackState get state => _state;
  @override
  MediaTracks get tracks => MediaTracks.none;
  @override
  String get source => 'fake';
  @override
  Future<void> open({String label = 'initialize'}) async {}
  @override
  Future<void> play() async {}
  @override
  Future<void> pause() async {}
  @override
  Future<void> seekTo(Duration position) async {}
  @override
  Future<void> setVolume(double volume) async {}
  @override
  Future<void> setLooping(bool looping) async {}
  @override
  void selectAudio(List<int> tracks) {}
  @override
  void selectSubtitles(List<int> tracks) {}
  @override
  void tune(PlaybackTuning tuning) {}
  @override
  Widget buildView() => const SizedBox.shrink();
  @override
  Future<void> dispose() async {}
}

void main() {
  final start = DateTime.utc(2030, 1, 1, 12, 0);

  Future<void> pump(WidgetTester tester, VideoPlayback playback) async {
    await tester.pumpWidget(MaterialApp(
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
            controller: playback,
            focusMode: ValueNotifier<bool>(false),
            onOpenSubtitles: () {},
            onOpenAudio: () {},
            onToggleDimmer: () {},
            onOpenDimmerLevel: () {},
            recentChannels: const [],
            onSelectChannel: (_) {},
            now: () => start,
          ),
        ]),
      ),
    ));
    await tester.pump(const Duration(milliseconds: 50));
  }

  testWidgets('the buffered segment runs to the furthest buffered instant', (tester) async {
    await pump(
      tester,
      _FakePlayback(const PlaybackState(
        isInitialized: true,
        isPlaying: true,
        position: Duration(seconds: 30),
        duration: Duration(seconds: 100),
        bufferedUntil: Duration(seconds: 60),
      )),
    );
    final bar = tester.getRect(find.byKey(const Key('osd-progress')));
    final buffered = tester.getRect(find.byKey(const Key('osd-buffered')));
    // 60 of 100 seconds buffered.
    expect(buffered.width / bar.width, closeTo(0.6, 0.02));
  });

  testWidgets('a buffer that only covers what already played adds nothing ahead', (tester) async {
    await pump(
      tester,
      _FakePlayback(const PlaybackState(
        isInitialized: true,
        isPlaying: true,
        position: Duration(seconds: 50),
        duration: Duration(seconds: 100),
        bufferedUntil: Duration(seconds: 50),
      )),
    );
    final bar = tester.getRect(find.byKey(const Key('osd-progress')));
    final buffered = tester.getRect(find.byKey(const Key('osd-buffered')));
    // The segment covers the played 50 s and nothing more: no read-ahead to
    // promise.
    expect(buffered.width / bar.width, closeTo(0.5, 0.02));
  });

  testWidgets('an engine that cannot report a buffer keeps the plain bar', (tester) async {
    await pump(
      tester,
      _FakePlayback(const PlaybackState(
        isInitialized: true,
        isPlaying: true,
        position: Duration(seconds: 25),
        duration: Duration(seconds: 100),
      )),
    );
    final bar = tester.getRect(find.byKey(const Key('osd-progress')));
    final buffered = tester.getRect(find.byKey(const Key('osd-buffered')));
    // Nothing is promised beyond what has played.
    expect(buffered.width / bar.width, closeTo(0.25, 0.02));
    final progress = tester.widget<LinearProgressIndicator>(find.byKey(const Key('osd-progress')));
    expect(progress.value, closeTo(0.25, 0.01));
  });
}
