import 'dart:io';

import 'package:client/video/video_engine.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';

// A minimal engine: proves the seam is enough to stand a different one in,
// and that a harness can swap it without touching the app.
class _FakeEngine implements VideoEngine {
  @override
  String get name => 'fake';
  @override
  String get decoderSummary => 'none';
  @override
  void register() {}
  @override
  VideoPlayback createNetwork(Uri uri) => _FakePlayback(uri.toString());
  @override
  VideoPlayback createAsset(String assetPath) => _FakePlayback(assetPath);
}

class _FakePlayback implements VideoPlayback {
  _FakePlayback(this.source);
  @override
  final String source;
  final calls = <String>[];
  @override
  PlaybackState get state => PlaybackState.uninitialized;
  @override
  MediaTracks get tracks => MediaTracks.none;
  @override
  Future<void> open({String label = 'initialize'}) async => calls.add('open');
  @override
  Future<void> play() async => calls.add('play');
  @override
  Future<void> pause() async {}
  @override
  Future<void> seekTo(Duration position) async {}
  @override
  Future<void> setVolume(double volume) async {}
  @override
  Future<void> setLooping(bool looping) async {}
  @override
  void selectAudio(List<int> tracks) => calls.add('audio$tracks');
  @override
  void selectSubtitles(List<int> tracks) => calls.add('subs$tracks');
  @override
  void tune(PlaybackTuning tuning) {}
  @override
  Widget buildView() => const SizedBox.shrink(key: Key('fake-view'));
  @override
  Future<void> dispose() async => calls.add('dispose');
}

void main() {
  test('only lib/video/ imports a concrete video engine package', () {
    final offenders = <String>[];
    final pattern = RegExp(r'''import\s+['"]package:(fvp|video_player|media_kit)[/a-z_]*\.dart['"]''');
    for (final dir in ['lib', 'test', 'integration_test']) {
      for (final file in Directory(dir).listSync(recursive: true).whereType<File>()) {
        if (!file.path.endsWith('.dart')) continue;
        if (file.path.startsWith('lib/video/')) continue;
        if (pattern.hasMatch(file.readAsStringSync())) offenders.add(file.path);
      }
    }
    expect(offenders, isEmpty, reason: 'go through lib/video/video_engine.dart instead');
  });

  testWidgets('useVideoEngine swaps the engine the app creates playbacks from', (tester) async {
    useVideoEngine(_FakeEngine());
    addTearDown(() => useVideoEngine(null));
    final playback = videoEngine.createNetwork(Uri.parse('http://example.com/a.mkv')) as _FakePlayback;
    await playback.open();
    playback.selectSubtitles(const []);
    expect(playback.source, 'http://example.com/a.mkv');
    expect(playback.calls, ['open', 'subs[]']);

    await tester.pumpWidget(Directionality(textDirection: TextDirection.ltr, child: VideoSurface(playback)));
    expect(find.byKey(const Key('fake-view')), findsOneWidget);
  });
}
