// What the engines built on the `video_player` plugin's `VideoPlayerController`
// (fvp on every platform, ExoPlayer on Android) have in common: playback
// control, the state snapshot and the view. What differs (how it is opened,
// where its tracks come from, engine tuning) stays with each engine.
import 'package:flutter/widgets.dart';
import 'package:video_player/video_player.dart';

import 'video_engine.dart';

abstract class ControllerPlayback implements VideoPlayback {
  @protected
  final VideoPlayerController controller;

  ControllerPlayback(this.controller);

  @override
  String get source => controller.dataSource;

  @override
  PlaybackState get state {
    final v = controller.value;
    return PlaybackState(
      isInitialized: v.isInitialized,
      isPlaying: v.isPlaying,
      isBuffering: v.isBuffering,
      position: v.position,
      duration: v.duration,
      bufferedUntil: v.buffered.isEmpty ? null : v.buffered.last.end,
      size: v.size,
      volume: v.volume,
    );
  }

  @override
  Future<void> play() => controller.play();

  @override
  Future<void> pause() => controller.pause();

  @override
  Future<void> seekTo(Duration position) => controller.seekTo(position);

  @override
  Future<void> setVolume(double volume) => controller.setVolume(volume);

  @override
  Future<void> setLooping(bool looping) => controller.setLooping(looping);

  @override
  Widget buildView() => VideoPlayer(controller);
}
