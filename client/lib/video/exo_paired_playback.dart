// A video and an audio url played in step, for the Android engine. A plugin's
// `pair` answer is two addresses (docs/PLUGINS.md "Modes"), which the
// `video_player` plugin cannot express — it opens one media source per player, so
// a second one would play as silent video. The player itself is built on the
// Android side (`android/app/src/main/kotlin/.../PairedVideos.kt`, Media3's
// `MergingMediaSource`) and drawn here as a texture.
//
// The surface is what this class offers the app, nothing more: the same
// [PlaybackState] shape as every other engine, the same control calls, and the
// position passing through [PlayheadClock] so a device whose audio timestamps
// come back unusable cannot look like a stopped stream (see playhead_clock.dart).
import 'dart:async';

import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

import '../log/log.dart';
import 'exo_teardown.dart';
import 'language_code.dart';
import 'playhead_clock.dart';
import 'video_engine.dart';

/// The channel the Android host registers from `MainActivity`.
const MethodChannel _channel = MethodChannel('playheadtv/paired');

/// The playbacks that are open, by the host's handle: the channel is one for the
/// whole app, and every event names the playback it belongs to.
final Map<int, ExoPairedPlayback> _open = {};

bool _handlerInstalled = false;

void _installHandler() {
  if (_handlerInstalled) return;
  _handlerInstalled = true;
  _channel.setMethodCallHandler((call) async {
    final handle = _handleOf(call.arguments);
    final playback = handle == null ? null : _open[handle];
    if (playback == null) return null;
    switch (call.method) {
      case 'event':
        playback._apply(call.arguments as Map);
      case 'tracks':
        playback._applyTracks(call.arguments as Map);
    }
    return null;
  });
}

int? _handleOf(Object? arguments) {
  final map = arguments;
  if (map is! Map) return null;
  final handle = map['handle'];
  return handle is int ? handle : null;
}

class ExoPairedPlayback implements VideoPlayback {
  ExoPairedPlayback({required this.video, required this.audio});

  /// The two addresses of the pair, as the plugin answered them.
  final Uri video;
  final Uri audio;

  final PlayheadClock _clock = PlayheadClock();

  int? _handle;
  int _textureId = 0;
  bool _disposed = false;

  bool _ready = false;
  bool _playing = false;
  bool _buffering = false;
  bool _ended = false;
  Duration _position = Duration.zero;
  Duration _buffered = Duration.zero;
  Duration _duration = Duration.zero;
  Size _size = Size.zero;
  double _volume = 1;
  String? _error;
  String? _reportedError;

  MediaTracks _tracks = MediaTracks.none;

  @override
  String get source => video.toString();

  @override
  PlaybackState get state {
    final length = trustedDuration(duration: _duration, bufferedUntil: _buffered);
    return PlaybackState(
      isInitialized: _ready,
      isPlaying: _playing,
      isBuffering: _buffering,
      position: _clock.advance(
        engine: _position,
        playing: _playing,
        bufferedUntil: _buffered,
        duration: length,
      ),
      duration: length,
      bufferedUntil: _buffered > Duration.zero ? _buffered : null,
      size: _size,
      volume: _volume,
      // The host clears it when a new open succeeds, so a non-null one is the
      // current verdict on this source.
      hasError: _error != null,
    );
  }

  @override
  MediaTracks get tracks => _tracks;

  @override
  Future<void> open({String label = 'initialize'}) async {
    _installHandler();
    try {
      final created = await _channel.invokeMapMethod<String, Object?>('create', {
        'video': video.toString(),
        'audio': audio.toString(),
      });
      final handle = created?['handle'];
      final textureId = created?['textureId'];
      if (handle is! int || textureId is! int) {
        throw StateError('the paired player answered without a handle');
      }
      _handle = handle;
      _textureId = textureId;
      // The host sends the track list with the answer, so an open that returned is
      // one whose tracks are already readable.
      _applyTracks(created!);
      // The host only answers `create` once the merged timeline is ready, so an
      // open that returned is an open that happened; later samples refine this.
      _ready = true;
      _open[handle] = this;
    } on PlatformException catch (e) {
      // The host failed to build the merged timeline: the caller treats an open
      // that throws as this item being unplayable, and moves on to the next one.
      throw StateError('the paired player could not open the pair: ${e.message}');
    }
  }

  Map<String, Object?> _arguments([Map<String, Object?> rest = const {}]) =>
      {'handle': _handle, ...rest};

  @override
  Future<void> play() async {
    if (_handle == null) return;
    await _channel.invokeMethod<void>('play', _arguments());
  }

  @override
  Future<void> pause() async {
    if (_handle == null) return;
    await _channel.invokeMethod<void>('pause', _arguments());
  }

  @override
  Future<void> seekTo(Duration position) async {
    _clock.restart(position);
    if (_handle == null) return;
    await _channel.invokeMethod<void>('seek', _arguments({'positionMs': position.inMilliseconds}));
  }

  @override
  Future<void> setVolume(double volume) async {
    _volume = volume;
    if (_handle == null) return;
    await _channel.invokeMethod<void>('setVolume', _arguments({'volume': volume}));
  }

  @override
  Future<void> setLooping(bool looping) async {
    if (_handle == null) return;
    await _channel.invokeMethod<void>('setLooping', _arguments({'looping': looping}));
  }

  @override
  void selectAudio(List<int> tracks) {
    if (_handle == null) return;
    unawaited(_channel.invokeMethod<void>('selectAudio', _arguments({'tracks': tracks})).catchError((Object e) {
      Log.warn('exo', 'could not select audio tracks $tracks', error: e);
    }));
  }

  @override
  void selectSubtitles(List<int> tracks) {
    // A merged playback here exposes no subtitle track, the same limit the
    // one-source ExoPlayer path already has (see ExoVideoEngine's header).
  }

  @override
  void tune(PlaybackTuning tuning) {
    // Media3's load control is not reachable from here; ExoPlayer's defaults
    // apply, exactly as on the one-source path.
  }

  @override
  Widget buildView() => Texture(key: ValueKey<int>(_textureId), textureId: _textureId);

  @override
  Future<void> dispose() {
    if (_disposed) return Future<void>.value();
    _disposed = true;
    final handle = _handle;
    _handle = null;
    if (handle == null) return Future<void>.value();
    _open.remove(handle);
    // The merged player holds two decoders, so its release is the one that
    // hurts most to run beside another: same FIFO as the plain player.
    return serializeExoTeardown(() async {
      try {
        await _channel.invokeMethod<void>('dispose', {'handle': handle});
      } catch (e) {
        Log.warn('exo', 'could not release the paired player', error: e);
      }
    });
  }

  /// One state sample from the host: 200ms apart, and at every state change.
  void _apply(Map<Object?, Object?> event) {
    final position = event['position'];
    final buffered = event['buffered'];
    final duration = event['duration'];
    final width = event['width'];
    final height = event['height'];
    _ready = event['ready'] == true;
    _playing = event['playing'] == true;
    _buffering = event['buffering'] == true;
    _ended = event['ended'] == true;
    if (position is num) _position = Duration(milliseconds: position.toInt());
    if (buffered is num) _buffered = Duration(milliseconds: buffered.toInt());
    if (duration is num) _duration = Duration(milliseconds: duration.toInt());
    if (width is num && height is num && width > 0 && height > 0) {
      _size = Size(width.toDouble(), height.toDouble());
    }
    final error = event['error'];
    if (error is String && error.isNotEmpty && error != _reportedError) {
      _reportedError = error;
      // The host's message names the error code and the player's own words; the
      // url is reduced to its host so a token never reaches the log.
      Log.error('controller', 'the video engine failed: $error (host ${video.host})');
    }
    _error = error is String ? error : null;
  }

  /// The track list, once the merged timeline is ready: the audio list is the
  /// audio source's own tracks, so the app's positions address what it plays.
  void _applyTracks(Map<Object?, Object?> message) {
    final audio = <AudioTrackInfo>[];
    for (final raw in (message['audio'] as List? ?? const [])) {
      if (raw is! Map) continue;
      audio.add(AudioTrackInfo(
        language: canonicalLanguage(raw['language'] as String?),
        title: _nonEmpty(raw['label'] as String?),
        codec: raw['codec'] as String? ?? '',
        channels: (raw['channels'] as num?)?.toInt() ?? 0,
        sampleRate: (raw['sampleRate'] as num?)?.toInt() ?? 0,
        bitRate: (raw['bitrate'] as num?)?.toInt() ?? 0,
      ));
    }
    final video = <VideoTrackInfo>[];
    for (final raw in (message['video'] as List? ?? const [])) {
      if (raw is! Map) continue;
      video.add(VideoTrackInfo(
        codec: raw['codec'] as String? ?? '',
        width: (raw['width'] as num?)?.toInt() ?? 0,
        height: (raw['height'] as num?)?.toInt() ?? 0,
        frameRate: (raw['frameRate'] as num?)?.toDouble() ?? 0,
        bitRate: (raw['bitrate'] as num?)?.toInt() ?? 0,
      ));
    }
    _tracks = MediaTracks(video: video, audio: audio);
  }

  /// Whether the host reported a playback failure (the caller sees it through
  /// [PlaybackState] only as a position that stopped, so it is kept here).
  String? get error => _error;

  /// Whether the merged timeline reached its end.
  bool get ended => _ended;
}

String? _nonEmpty(String? value) => (value == null || value.trim().isEmpty) ? null : value;
