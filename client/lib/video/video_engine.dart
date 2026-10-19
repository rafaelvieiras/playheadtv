// The app's seam over the video engine. Everything outside `lib/video/` talks
// to these types and never to a concrete engine's (`fvp`, `video_player`)
// API, so adding or swapping an engine (e.g. ExoPlayer on Android TV) is one
// new implementation of [VideoEngine], not a hunt through the player, the
// mosaic and the overlays.
//
// The surface is what the app really uses, not a superset: open/dispose, play
// control, position/duration, the track list and the choice of tracks, the
// rendering widget, and `tune` for engine-specific knobs. A test
// (`test/video_engine_boundary_test.dart`) fails if another file imports an
// engine package.
import 'package:flutter/widgets.dart';

import 'fvp_video_engine.dart';

/// A point-in-time snapshot of a playback. Cheap to read; never cached by the
/// callers beyond one build/sample.
@immutable
class PlaybackState {
  final bool isInitialized;
  final bool isPlaying;
  final bool isBuffering;
  final Duration position;
  final Duration duration;

  /// How far ahead the engine has buffered, or null when it cannot say.
  final Duration? bufferedUntil;

  /// The video frame size in pixels; [Size.zero] until the first frame is known.
  final Size size;
  final double volume;

  const PlaybackState({
    this.isInitialized = false,
    this.isPlaying = false,
    this.isBuffering = false,
    this.position = Duration.zero,
    this.duration = Duration.zero,
    this.bufferedUntil,
    this.size = Size.zero,
    this.volume = 1,
  });

  static const PlaybackState uninitialized = PlaybackState();
}

/// What the app needs to know about one audio/subtitle track. The engine maps
/// its own metadata (ffmpeg-style tags, case-insensitive) into these fields.
@immutable
class TrackInfo {
  /// The `language` tag (e.g. `por`), when the container carries one.
  final String? language;

  /// The `title` tag (e.g. "Director's Commentary"), when present.
  final String? title;

  const TrackInfo({this.language, this.title});
}

@immutable
class VideoTrackInfo {
  final String codec;
  final int profile;
  final int level;
  final int width;
  final int height;
  final double frameRate;

  /// Declared bit rate in bits per second.
  final int bitRate;

  const VideoTrackInfo({
    this.codec = '',
    this.profile = 0,
    this.level = 0,
    this.width = 0,
    this.height = 0,
    this.frameRate = 0,
    this.bitRate = 0,
  });
}

@immutable
class AudioTrackInfo extends TrackInfo {
  final String codec;
  final int channels;
  final int sampleRate;

  /// Declared bit rate in bits per second.
  final int bitRate;

  const AudioTrackInfo({
    super.language,
    super.title,
    this.codec = '',
    this.channels = 0,
    this.sampleRate = 0,
    this.bitRate = 0,
  });
}

@immutable
class SubtitleTrackInfo extends TrackInfo {
  const SubtitleTrackInfo({super.language, super.title});
}

/// The tracks of an opened media. [audio] and [subtitle] are in the order the
/// `selectAudio`/`selectSubtitles` indexes address: an index is the 0-based
/// position within that list, never the container's absolute stream number.
@immutable
class MediaTracks {
  final List<VideoTrackInfo> video;
  final List<AudioTrackInfo> audio;
  final List<SubtitleTrackInfo> subtitle;

  const MediaTracks({
    this.video = const [],
    this.audio = const [],
    this.subtitle = const [],
  });

  static const MediaTracks none = MediaTracks();
}

/// Engine-specific tuning, applied after a playback is opened. An engine
/// ignores what it does not support and adds any workaround of its own here
/// (the call sites never hardcode an engine's property names).
@immutable
class PlaybackTuning {
  /// Read-ahead window: playback starts/resumes once [bufferMin] is buffered
  /// and the engine reads on up to [bufferMax]. Null leaves the engine's own.
  final Duration? bufferMin;
  final Duration? bufferMax;

  const PlaybackTuning({this.bufferMin, this.bufferMax});
}

/// One media being played (or being prepared). The app holds two of them for
/// the crossfade, plus the slate and the mosaic preview.
abstract class VideoPlayback {
  /// Where it plays from (a URL or an asset path); for diagnostics and tests.
  String get source;

  PlaybackState get state;

  /// Opens the media. The engine serializes whatever native work needs it, so
  /// callers just await. [label] only names the step in the logs.
  Future<void> open({String label = 'initialize'});

  Future<void> play();
  Future<void> pause();
  Future<void> seekTo(Duration position);
  Future<void> setVolume(double volume);
  Future<void> setLooping(bool looping);

  /// The tracks of the opened media ([MediaTracks.none] until it is open).
  MediaTracks get tracks;

  /// Plays the audio tracks at these [tracks] positions (see [MediaTracks]).
  void selectAudio(List<int> tracks);

  /// Shows the subtitle tracks at these positions; an empty list shows none.
  void selectSubtitles(List<int> tracks);

  void tune(PlaybackTuning tuning);

  /// The widget that draws the frames. Built once per playback and placed in
  /// the tree through [VideoSurface], so it is never rebuilt by a state change.
  Widget buildView();

  /// Releases the playback. The engine serializes native teardown itself.
  Future<void> dispose();
}

/// A video engine: where playbacks come from.
abstract class VideoEngine {
  /// Shown by the debug overlay.
  String get name;

  /// How the engine picks decoders, shown by the debug overlay.
  String get decoderSummary;

  /// One-time process setup, from `main()`. A harness that never calls it
  /// (the integration tests) gets the engine's defaults.
  void register();

  VideoPlayback createNetwork(Uri uri);
  VideoPlayback createAsset(String assetPath);
}

VideoEngine? _engine;

/// The engine in use. Lazily the default one, so a test harness that boots the
/// app without `main()` still has an engine.
VideoEngine get videoEngine => _engine ??= FvpVideoEngine();

/// Replaces the engine (a test double, or a platform choice made at startup);
/// null goes back to the default. Call before any playback is created.
void useVideoEngine(VideoEngine? engine) => _engine = engine;

/// Draws a [VideoPlayback]. A widget of its own so a test (or a parent) can
/// find the playbacks on screen and read [playback].
class VideoSurface extends StatelessWidget {
  final VideoPlayback playback;

  const VideoSurface(this.playback, {super.key});

  @override
  Widget build(BuildContext context) => playback.buildView();
}
