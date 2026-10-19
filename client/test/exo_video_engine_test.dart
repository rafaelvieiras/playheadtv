import 'dart:async';

import 'package:client/video/exo_video_engine.dart';
import 'package:client/video/video_engine.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:video_player_platform_interface/video_player_platform_interface.dart';

// A stand-in for the Android plugin: one player whose tracks the test sets.
class _FakePlatform extends VideoPlayerPlatform {
  List<VideoAudioTrack> audio = const [];
  List<VideoTrack> video = const [];
  Object? audioError;
  final selected = <String>[];
  final created = <VideoCreationOptions>[];
  final disposed = <int>[];
  final mixWithOthers = <bool>[];
  final _events = StreamController<VideoEvent>();

  @override
  Future<void> init() async {}
  @override
  Future<void> dispose(int playerId) async => disposed.add(playerId);
  @override
  Future<int?> createWithOptions(VideoCreationOptions options) async {
    created.add(options);
    scheduleMicrotask(() => _events.add(VideoEvent(
          eventType: VideoEventType.initialized,
          duration: const Duration(seconds: 60),
          size: const Size(1920, 1080),
        )));
    return 7;
  }

  @override
  Future<void> setMixWithOthers(bool value) async => mixWithOthers.add(value);
  @override
  Stream<VideoEvent> videoEventsFor(int playerId) => _events.stream;
  @override
  Future<void> setLooping(int playerId, bool looping) async {}
  @override
  Future<void> play(int playerId) async {}
  @override
  Future<void> pause(int playerId) async {}
  @override
  Future<void> setVolume(int playerId, double volume) async {}
  @override
  Future<void> seekTo(int playerId, Duration position) async {}
  @override
  Future<Duration> getPosition(int playerId) async => Duration.zero;
  @override
  Widget buildView(int playerId) => const SizedBox(key: Key('exo-view'));
  @override
  Future<List<VideoAudioTrack>> getAudioTracks(int playerId) async {
    if (audioError != null) throw audioError!;
    return audio;
  }

  @override
  Future<void> selectAudioTrack(int playerId, String trackId) async => selected.add(trackId);
  @override
  Future<List<VideoTrack>> getVideoTracks(int playerId) async => video;
}

void main() {
  late _FakePlatform platform;

  setUp(() {
    platform = _FakePlatform();
    VideoPlayerPlatform.instance = platform;
  });

  VideoAudioTrack audio(String id, String? lang, {String? label, bool selected = false, int? channels}) => VideoAudioTrack(
        id: id,
        label: label,
        language: lang,
        isSelected: selected,
        channelCount: channels,
        codec: 'ac-3',
        bitrate: 640000,
        sampleRate: 48000,
      );

  testWidgets('open reads the tracks, with languages in the app\'s three-letter form', (tester) async {
    platform.audio = [
      audio('0_0', 'pt', label: 'Portuguese', selected: true, channels: 6),
      audio('0_1', 'en', label: ' ', channels: 2),
      audio('0_2', 'und'),
    ];
    platform.video = const [VideoTrack(id: '1_0', isSelected: true, width: 1920, height: 1080, frameRate: 23.976, codec: 'avc1.640028', bitrate: 8000000)];
    final playback = ExoVideoEngine().createNetwork(Uri.parse('http://example.com/a.mkv'));
    addTearDown(playback.dispose);

    await tester.runAsync(playback.open);

    final tracks = playback.tracks;
    expect(tracks.audio.map((t) => t.language), ['por', 'eng', null]);
    expect(tracks.audio.map((t) => t.title), ['Portuguese', null, null]);
    expect(tracks.audio.first.channels, 6);
    expect(tracks.audio.first.codec, 'ac-3');
    expect(tracks.video.single.height, 1080);
    expect(tracks.video.single.codec, 'avc1.640028');
    expect(tracks.subtitle, isEmpty, reason: 'the plugin exposes no embedded subtitles');
    expect(playback.state.isInitialized, isTrue);
    expect(playback.source, 'http://example.com/a.mkv');
  });

  testWidgets('selectAudio addresses the position in the track list, not an id', (tester) async {
    platform.audio = [audio('0_0', 'pt'), audio('0_1', 'en'), audio('2_0', 'ja')];
    final playback = ExoVideoEngine().createNetwork(Uri.parse('http://example.com/a.mkv'));
    addTearDown(playback.dispose);
    await tester.runAsync(playback.open);

    playback.selectAudio([2]);
    playback.selectAudio([1, 0]);
    await tester.runAsync(() => Future<void>.delayed(Duration.zero));

    expect(platform.selected, ['2_0', '0_1'], reason: 'only the first position plays; ExoPlayer has one audio track at a time');
  });

  testWidgets('selectAudio ignores an empty list and an out-of-range position', (tester) async {
    platform.audio = [audio('0_0', 'pt')];
    final playback = ExoVideoEngine().createNetwork(Uri.parse('http://example.com/a.mkv'));
    addTearDown(playback.dispose);
    await tester.runAsync(playback.open);

    playback.selectAudio(const []);
    playback.selectAudio([5]);
    playback.selectAudio([-1]);
    await tester.runAsync(() => Future<void>.delayed(Duration.zero));

    expect(platform.selected, isEmpty);
  });

  testWidgets('selectSubtitles and tune are accepted and change nothing', (tester) async {
    final playback = ExoVideoEngine().createNetwork(Uri.parse('http://example.com/a.mkv'));
    addTearDown(playback.dispose);
    await tester.runAsync(playback.open);

    playback.selectSubtitles([0]);
    playback.selectSubtitles(const []);
    playback.tune(const PlaybackTuning(bufferMin: Duration(seconds: 1), bufferMax: Duration(seconds: 5)));

    expect(playback.tracks.subtitle, isEmpty);
    expect(platform.selected, isEmpty);
  });

  testWidgets('a track list that cannot be read leaves a playing, track-less playback', (tester) async {
    platform.audioError = StateError('disposed');
    final playback = ExoVideoEngine().createNetwork(Uri.parse('http://example.com/a.mkv'));
    addTearDown(playback.dispose);

    await tester.runAsync(playback.open);

    expect(playback.state.isInitialized, isTrue);
    expect(playback.tracks.audio, isEmpty);
    playback.selectAudio([0]);
    expect(platform.selected, isEmpty);
  });

  testWidgets('players do not request audio focus, so the crossfade pair does not pause each other', (tester) async {
    final playback = ExoVideoEngine().createNetwork(Uri.parse('http://example.com/a.mkv'));
    addTearDown(playback.dispose);
    await tester.runAsync(playback.open);

    expect(platform.mixWithOthers, [true]);
    expect(platform.created.single.videoPlayerOptions?.mixWithOthers, isTrue);
  });

  testWidgets('dispose reaches the plugin and the view is the plugin\'s', (tester) async {
    final playback = ExoVideoEngine().createNetwork(Uri.parse('http://example.com/a.mkv'));
    await tester.runAsync(playback.open);

    await tester.pumpWidget(Directionality(textDirection: TextDirection.ltr, child: VideoSurface(playback)));
    expect(find.byKey(const Key('exo-view')), findsOneWidget);

    await tester.runAsync(playback.dispose);
    expect(platform.disposed, [7]);
  });

  test('the engine names itself for the debug overlay', () {
    final engine = ExoVideoEngine();
    expect(engine.name, contains('ExoPlayer'));
    expect(engine.decoderSummary, contains('MediaCodec'));
  });

  group('resolveVideoEngineKind', () {
    test('defaults to fvp', () {
      for (final v in [null, '', '  ', 'fvp', 'FVP']) {
        expect(resolveVideoEngineKind(v, isAndroid: true), VideoEngineKind.fvp, reason: '"$v"');
      }
    });

    test('exo is honored on Android only', () {
      expect(resolveVideoEngineKind('exo', isAndroid: true), VideoEngineKind.exo);
      expect(resolveVideoEngineKind(' ExoPlayer ', isAndroid: true), VideoEngineKind.exo);
      final reasons = <String>[];
      expect(resolveVideoEngineKind('exo', isAndroid: false, onFallback: reasons.add), VideoEngineKind.fvp);
      expect(reasons.single, contains('Android'));
    });

    test('an unknown name falls back and says why', () {
      final reasons = <String>[];
      expect(resolveVideoEngineKind('mpv', isAndroid: true, onFallback: reasons.add), VideoEngineKind.fvp);
      expect(reasons.single, contains('mpv'));
    });

    test('a request that is honored reports nothing', () {
      final reasons = <String>[];
      resolveVideoEngineKind('fvp', isAndroid: false, onFallback: reasons.add);
      resolveVideoEngineKind(null, isAndroid: false, onFallback: reasons.add);
      expect(reasons, isEmpty);
    });
  });

  test('selectVideoEngine installs the engine for the kind', () {
    addTearDown(() => useVideoEngine(null));
    selectVideoEngine(VideoEngineKind.exo);
    expect(videoEngine, isA<ExoVideoEngine>());
    selectVideoEngine(VideoEngineKind.fvp);
    expect(videoEngine, isNot(isA<ExoVideoEngine>()));
  });

  test('the Settings label flags a pending change', () {
    addTearDown(() => useVideoEngine(null));
    selectVideoEngine(VideoEngineKind.fvp);
    expect(videoEngineLabel(VideoEngineKind.fvp, running: videoEngine), 'Default (libmdk)');
    expect(videoEngineLabel(VideoEngineKind.exo, running: videoEngine), 'ExoPlayer, applies after restart');
    selectVideoEngine(VideoEngineKind.exo);
    expect(videoEngineLabel(VideoEngineKind.exo, running: videoEngine), 'ExoPlayer');
    expect(videoEngineLabel(VideoEngineKind.fvp, running: videoEngine), 'Default (libmdk), applies after restart');
  });
}
