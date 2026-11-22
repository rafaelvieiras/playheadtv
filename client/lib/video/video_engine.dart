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

import 'exo_video_engine.dart';
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

  /// The engine gave up on this source. It stays false while it retries on its
  /// own, so a true here means the controller will not play this item without
  /// being opened again.
  final bool hasError;

  const PlaybackState({
    this.isInitialized = false,
    this.isPlaying = false,
    this.isBuffering = false,
    this.position = Duration.zero,
    this.duration = Duration.zero,
    this.bufferedUntil,
    this.size = Size.zero,
    this.volume = 1,
    this.hasError = false,
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

  /// Whether a playback can take an audio track from a separate url
  /// ([createNetwork]'s `audioUri`), which is what a plugin's `pair` answer is
  /// (docs/PLUGINS.md "Modes"). The player only asks a plugin for a pair when
  /// this is true, so an engine without it never receives one.
  bool get supportsPairedAudio;

  /// One-time process setup, from `main()`. A harness that never calls it
  /// (the integration tests) gets the engine's defaults.
  void register();

  /// [audioUri] is a second source carrying only audio, played in step with
  /// [uri]; null is the ordinary case of one source with both tracks inside.
  VideoPlayback createNetwork(Uri uri, {Uri? audioUri});
  VideoPlayback createAsset(String assetPath);
}

/// The shapes a player can open, best first, as the plugin modes they name
/// (docs/PLUGINS.md "Modes"): a video and an audio address the player merges
/// (`pair`), one address carrying both (`single`), or a stream the plugin
/// produces (`remux`). The order is the host's preference and a plugin answers
/// with the first mode it can.
///
/// `pair` comes first when the engine can merge one: it costs the host nothing
/// and it is the best picture a source has — measured on the plugin this was
/// written for, `single` was a 360p muxed file while the separated video track
/// was 1080p. `remux` is last because it is the only mode that runs a process per
/// playback.
/// The shapes to ask a plugin for, best first. A `pair` costs the host nothing
/// and carries the source's best tracks, so it wins whenever the engine can
/// merge one; `remux` is the transcription of those same tracks when it cannot.
/// `single` is deliberately last — and never asked for, since a source's one
/// muxed file is usually the cheap one (on YouTube's it is 360p against a 1080p
/// separated track) — but the player still parses and plays one if a plugin
/// answers it (`PluginStream.mode`).
List<String> pluginPlaybackModes(VideoEngine engine) => [
      if (engine.supportsPairedAudio) 'pair',
      'remux',
    ];

VideoEngine? _engine;

/// The engine in use. Lazily the default one, so a test harness that boots the
/// app without `main()` still has an engine.
VideoEngine get videoEngine => _engine ??= FvpVideoEngine();

/// The engines the app can play with, as named in the config.
enum VideoEngineKind {
  /// libmdk through `fvp`: every platform.
  fvp,

  /// ExoPlayer (Media3): Android only.
  exo,
}

/// Which engine a setting asks for. [requested] is the value of the
/// `PLAYHEADTV_VIDEO_ENGINE` environment variable or `video_engine` in the
/// config file (`fvp`, `exo`; case-insensitive). Absent, empty or unknown, and
/// `exo` anywhere but Android, give [VideoEngineKind.fvp]; [onFallback] is told
/// why when a request could not be honored.
VideoEngineKind resolveVideoEngineKind(
  String? requested, {
  required bool isAndroid,
  void Function(String reason)? onFallback,
}) {
  final value = requested?.trim().toLowerCase() ?? '';
  switch (value) {
    case '':
    case 'fvp':
      return VideoEngineKind.fvp;
    case 'exo':
    case 'exoplayer':
      if (isAndroid) return VideoEngineKind.exo;
      onFallback?.call('the ExoPlayer engine only exists on Android');
      return VideoEngineKind.fvp;
    default:
      onFallback?.call('unknown video engine "$requested"');
      return VideoEngineKind.fvp;
  }
}

/// The Settings row's value: the saved engine, and a reminder when it is not the
/// one running (a change applies at the next start).
String videoEngineLabel(VideoEngineKind saved, {required VideoEngine running}) {
  final name = switch (saved) {
    VideoEngineKind.fvp => 'Default (libmdk)',
    VideoEngineKind.exo => 'ExoPlayer',
  };
  final active = switch (running) {
    ExoVideoEngine() => VideoEngineKind.exo,
    _ => VideoEngineKind.fvp,
  };
  return active == saved ? name : '$name, applies after restart';
}

/// Installs the engine [kind] names. Call before [VideoEngine.register] and
/// before any playback exists.
void selectVideoEngine(VideoEngineKind kind) =>
    useVideoEngine(switch (kind) {
      VideoEngineKind.fvp => FvpVideoEngine(),
      VideoEngineKind.exo => ExoVideoEngine(),
    });

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
