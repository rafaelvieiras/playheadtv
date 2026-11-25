// Whether this device can decode a piece of media, asked before a player is
// built for it.
//
// Why ask at all: a set-top box has a handful of MediaCodec decoders, and the
// formats are not the same set everywhere. A 320x240 MPEG-4 Part 2 file in an
// AVI - which a media library can hold thousands of - is ordinary on a desktop
// and cannot be decoded at all on some boxes: the platform answers with
// OMX_ErrorInsufficientResources, or simply has no decoder for that MIME. The
// player then spent minutes building players that could not work.
//
// So the question is asked first, and an answer of [CodecSupport.unsupported]
// means no player is built at all: the channel says what it cannot play and
// waits for the next program (`waiting_for_next_program` notice in main.dart).
//
// This is a static answer (MediaCodecList on the platform end), so it is cheap
// and cached per codec+size. It cannot see everything: a decoder that exists but
// refuses to instantiate right now (because the box already holds its two
// instances) answers supported and the open still fails. That case is caught by
// the failure path instead (`isUnsupportedMediaError`).
import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

import '../log/log.dart';

/// What the platform says about one media shape.
enum CodecSupport {
  /// A decoder exists for it. Not a promise that it will open: a box whose
  /// decoders are all busy answers the same.
  supported,

  /// No decoder on this device handles it, so opening it can only fail.
  unsupported,

  /// Nobody knows: not Android, no codec metadata, or a codec name this build
  /// does not map. Playback is attempted, as it always was.
  unknown,
}

/// The seam the player asks. One implementation per platform, and a fake in
/// tests (see `test/playback_capability_test.dart`).
abstract class PlaybackCapability {
  /// An answer for [codec] (an ffprobe name such as `h264`, `hevc`, `mpeg4`)
  /// at [width]x[height]. Never throws: a platform that cannot answer returns
  /// [CodecSupport.unknown], which never blocks playback.
  Future<CodecSupport> check({String? codec, int? width, int? height});
}

/// The capability in force. Lazily the platform one, so a harness that boots the
/// app without `main()` still has something to ask (the same shape as
/// [videoEngine]), and a test replaces it with a fake.
PlaybackCapability? _capability;

PlaybackCapability get playbackCapability => _capability ??= PlatformPlaybackCapability();

/// Replaces the capability (tests, and a platform that needs a different
/// answer). Passing null restores the platform one.
@visibleForTesting
set playbackCapability(PlaybackCapability? value) => _capability = value;

/// Asks the Android end (`CodecProbe.kt`); on every other platform nothing can
/// be consulted, so the answer is always [CodecSupport.unknown].
class PlatformPlaybackCapability implements PlaybackCapability {
  PlatformPlaybackCapability({MethodChannel? channel})
      : _channel = channel ?? const MethodChannel('playheadtv/codec');

  final MethodChannel _channel;

  /// Answers already given, by codec and size: a channel that plays a hundred
  /// 320x240 mpeg4 clips asks the same question a hundred times, and the answer
  /// cannot change while the app runs.
  final Map<String, CodecSupport> _cache = <String, CodecSupport>{};

  @override
  Future<CodecSupport> check({String? codec, int? width, int? height}) async {
    final name = codec?.trim().toLowerCase();
    // An item nobody probed (or a backend that does not send the shape) is
    // played, not refused: "unknown" is the pre-existing behavior.
    if (name == null || name.isEmpty) return CodecSupport.unknown;
    final key = '$name|${width ?? 0}x${height ?? 0}';
    final cached = _cache[key];
    if (cached != null) return cached;
    CodecSupport answer;
    try {
      final raw = await _channel.invokeMethod<String>('canDecode', <String, Object?>{
        'codec': name,
        'width': width ?? 0,
        'height': height ?? 0,
      });
      answer = switch (raw) {
        'supported' => CodecSupport.supported,
        'unsupported' => CodecSupport.unsupported,
        _ => CodecSupport.unknown,
      };
    } on MissingPluginException {
      // A desktop build, or an older install: nothing to ask.
      answer = CodecSupport.unknown;
    } on PlatformException catch (e) {
      Log.debug('codec', 'the platform could not answer for $name: ${e.message}');
      answer = CodecSupport.unknown;
    }
    _cache[key] = answer;
    if (answer == CodecSupport.unsupported) {
      Log.info('codec', 'this device has no decoder for $name at ${width ?? 0}x${height ?? 0}');
    }
    return answer;
  }
}

/// Thrown by the open path when this device cannot decode the unit, so no
/// player is built for it at all.
///
/// It is not a failure of the source: the same item plays on another device, and
/// what the viewer gets is "this program cannot be played here", not "the
/// channel broke". See `_enterUnplayableWait` in main.dart.
class UnsupportedMediaException implements Exception {
  UnsupportedMediaException(this.reason, {this.codec});

  /// Short, lowercase, for a log line: `no decoder for mpeg4 at 320x240`.
  final String reason;

  /// The ffprobe codec name, when one was known.
  final String? codec;

  @override
  String toString() => 'UnsupportedMediaException($reason)';
}

/// True when an open failure says the *media* cannot be decoded here, rather
/// than a source that is slow, missing or temporarily busy.
///
/// The two engines word it differently: Media3 (the Android engine) reports
/// "MediaCodecVideoRenderer error" or "Decoder init failed", `fvp`/libmdk
/// reports a codec it could not open. Both mean the same thing for the viewer:
/// this program will not play, whatever the schedule does.
bool isUnsupportedMediaError(Object error) {
  final text = error.toString().toLowerCase();
  const needles = <String>[
    'mediacodecvideorenderer error',
    'mediacodecaudiorenderer error',
    'decoder init failed',
    'decoderinit',
    'no decoder',
    'decoder for the format',
    'unsupported format',
    'codec not found',
  ];
  return needles.any(text.contains);
}
