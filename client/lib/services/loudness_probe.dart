// Leveling the channels needs one number per piece of audio: how loud it is.
// The backend does not measure it — the device does, on the stream it is about
// to play, decoding with the same platform decoders playback uses. That is not
// a shortcut, it is the accurate path: the platform decoder applies Dolby
// downmix/dialnorm, so a server-side ffmpeg measurement of an AC3 film lands up
// to 8 dB away from what a viewer actually hears.
//
// The measurement itself lives in the Android side (LoudnessChannel.kt, on top
// of Media3), because it has to decode and it has to run where the player runs.
// This file is the channel's Dart end.
import 'dart:async';

import 'package:flutter/services.dart';

import '../log/log.dart';

/// One measurement of a piece of a stream.
class LoudnessMeasurement {
  const LoudnessMeasurement({
    required this.lufs,
    required this.peakDb,
    required this.seconds,
    required this.blocks,
  });

  /// Integrated loudness (ITU-R BS.1770), in LUFS.
  final double lufs;

  /// Sample peak in dBFS.
  final double peakDb;

  /// How much audio was actually decoded (the window can end early at EOS).
  final double seconds;

  /// Completed 400 ms blocks. Fewer than [minBlocks] means the window was
  /// mostly silence, and a gain derived from it would be a guess.
  final int blocks;

  /// 4 blocks = 1.6 s of audio that carried signal.
  static const minBlocks = 4;

  /// True when the measurement says something about the content.
  bool get usable => blocks >= minBlocks && lufs.isFinite;

  @override
  String toString() =>
      'lufs=${lufs.toStringAsFixed(2)} peak=${peakDb.toStringAsFixed(2)}dB '
      'seconds=${seconds.toStringAsFixed(2)} blocks=$blocks';
}

/// Thrown when the platform side cannot measure (no audio track, timeout).
class LoudnessProbeException implements Exception {
  LoudnessProbeException(this.message);
  final String message;
  @override
  String toString() => 'LoudnessProbeException: $message';
}

/// The `playheadtv/loudness` channel: measures a piece of a stream on device.
class LoudnessProbe {
  const LoudnessProbe();

  static const MethodChannel _channel = MethodChannel('playheadtv/loudness');

  /// Measures [window] of audio of [url] starting at [startAt].
  ///
  /// A channel change has no lead time, so the point asked for is the point
  /// being watched (ExoPlayer seeks there by reading the container index).
  /// Returns null when the platform side gave up without a verdict; throws
  /// [LoudnessProbeException] when there was audio but it could not be read.
  Future<LoudnessMeasurement?> measure({
    required String url,
    Duration startAt = Duration.zero,
    Duration window = const Duration(seconds: 5),
  }) async {
    final started = DateTime.now();
    try {
      final answer = await _channel
          .invokeMapMethod<String, Object?>('measure', {
            'url': url,
            'startSeconds': startAt.inMilliseconds / 1000.0,
            'seconds': window.inMilliseconds / 1000.0,
          })
          // The platform side has its own timeout; this one only covers a
          // channel call that never answers at all.
          .timeout(const Duration(seconds: 40));
      if (answer == null) {
        Log.debug('loudness', 'no measurement for $url at ${startAt.inSeconds}s');
        return null;
      }
      final result = LoudnessMeasurement(
        lufs: (answer['lufs'] as num).toDouble(),
        peakDb: (answer['peakDb'] as num).toDouble(),
        seconds: (answer['seconds'] as num).toDouble(),
        blocks: (answer['blocks'] as num).toInt(),
      );
      Log.debug(
        'loudness',
        'measured ${startAt.inSeconds}s+${window.inSeconds}s of $url in '
        '${DateTime.now().difference(started).inMilliseconds}ms: $result',
      );
      return result;
    } on PlatformException catch (e) {
      Log.warn('loudness', 'measure failed for $url', error: e);
      return null;
    } on TimeoutException catch (e) {
      Log.warn('loudness', 'measure timed out for $url', error: e);
      return null;
    }
  }
}
