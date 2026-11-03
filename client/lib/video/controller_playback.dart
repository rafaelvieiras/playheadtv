// What the engines built on the `video_player` plugin's `VideoPlayerController`
// (fvp on every platform, ExoPlayer on Android) have in common: playback
// control, the state snapshot and the view. What differs (how it is opened,
// where its tracks come from, engine tuning) stays with each engine.
import 'package:flutter/widgets.dart';
import 'package:video_player/video_player.dart';

import '../log/log.dart';
import 'playhead_clock.dart';
import 'video_engine.dart';

abstract class ControllerPlayback implements VideoPlayback {
  @protected
  final VideoPlayerController controller;

  /// What to report as the position while the engine's own answer is unusable.
  final PlayheadClock _clock = PlayheadClock();

  ControllerPlayback(this.controller) {
    // An engine that fails under a playing picture leaves no other trace: the
    // player only sees a position that stopped moving. The engine's own reason
    // (a bad segment, a closed stream) is worth keeping, with the url reduced to
    // its host so a token never reaches the log.
    controller.addListener(_logEngineError);
  }

  /// The last error already reported, so one failure is logged once.
  String? _reportedError;

  void _logEngineError() {
    final v = controller.value;
    if (!v.hasError) {
      _reportedError = null;
      return;
    }
    final description = v.errorDescription ?? 'no description';
    if (description == _reportedError) return;
    _reportedError = description;
    Log.error('controller', 'the video engine failed: $description (host ${Uri.tryParse(controller.dataSource)?.host})');
  }

  @override
  String get source => controller.dataSource;

  @override
  PlaybackState get state {
    final v = controller.value;
    final bufferedUntil = v.buffered.isEmpty ? null : v.buffered.last.end;
    return PlaybackState(
      isInitialized: v.isInitialized,
      isPlaying: v.isPlaying,
      isBuffering: v.isBuffering,
      // Some devices freeze this while the picture keeps moving; the clock keeps
      // time on its own only where the engine stopped advancing.
      position: _clock.advance(
        engine: v.position,
        playing: v.isPlaying,
        buffering: v.isBuffering,
        bufferedUntil: bufferedUntil,
        duration: v.duration,
      ),
      duration: v.duration,
      bufferedUntil: bufferedUntil,
      size: v.size,
      volume: v.volume,
    );
  }

  @override
  Future<void> play() => controller.play();

  @override
  Future<void> pause() => controller.pause();

  @override
  Future<void> seekTo(Duration position) {
    _clock.restart(position);
    return controller.seekTo(position);
  }

  @override
  Future<void> setVolume(double volume) => controller.setVolume(volume);

  @override
  Future<void> setLooping(bool looping) => controller.setLooping(looping);

  @override
  Widget buildView() => VideoPlayer(controller);
}
