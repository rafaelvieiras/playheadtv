// The global pre-amp: gain above unity, applied to everything the app plays.
//
// The player's volume stops at 1, so the quiet half of the library cannot be
// brought up by the player alone. The platform's LoudnessEnhancer on the output
// mix (session 0) can, and it comes with a limiter, so peaks pushed past full
// scale compress instead of clipping — measured on API 28 and API 36 before this
// was written, applying the requested gain exactly.
//
// The other half of the policy lives in [loudnessGain]: whatever ends up louder
// than the target after the pre-amp is cut by the player's own volume.
//
// The platform end is AudioPreamp.kt, behind the same `playheadtv/loudness`
// channel as the measurement.
import 'dart:async';

import 'package:flutter/services.dart';

import '../log/log.dart';

/// What the platform reports about the pre-amp.
class PreampState {
  const PreampState({
    required this.requestedMb,
    required this.targetMb,
    required this.enabled,
    required this.control,
  });

  /// Gain asked for, in millibels.
  final int requestedMb;

  /// Gain the effect reports holding, in millibels.
  final int targetMb;

  final bool enabled;

  /// False when the platform refused the effect (no implementation, or the
  /// output mix is off limits). The app then plays unleveled rather than silent.
  final bool control;

  /// True when the pre-amp is really lifting the output.
  bool get active => control && enabled && targetMb > 0;

  @override
  String toString() =>
      'requested=${(requestedMb / 100).toStringAsFixed(1)}dB '
      'target=${(targetMb / 100).toStringAsFixed(1)}dB enabled=$enabled control=$control';
}

/// The `playheadtv/loudness` channel's pre-amp end.
class AudioPreamp {
  const AudioPreamp();

  static const MethodChannel _channel = MethodChannel('playheadtv/loudness');

  /// Sets the global gain to [db] and reports what the platform did. Never
  /// throws: [PreampState.control] false means the boost is not available.
  ///
  /// The effect is an Android one (`AudioPreamp.kt`, LoudnessEnhancer on the
  /// output mix), so on every other platform the method channel has no handler
  /// at all and answers with [MissingPluginException] — not a
  /// [PlatformException], which is why that case is caught on its own. It used
  /// to escape as an uncaught async error, and because the pre-amp is applied
  /// at boot it took the whole desktop GUI suite down with it
  /// ("MissingPluginException ... after the test had completed").
  Future<PreampState?> set(double db) async {
    try {
      final answer = await _channel
          .invokeMapMethod<String, Object?>('setPreamp', {'db': db})
          .timeout(const Duration(seconds: 5));
      if (answer == null) return null;
      final state = PreampState(
        requestedMb: (answer['requestedMb'] as num?)?.toInt() ?? 0,
        targetMb: (answer['targetMb'] as num?)?.toInt() ?? 0,
        enabled: answer['enabled'] == true,
        control: answer['control'] == true,
      );
      Log.info('loudness', 'pre-amp $state');
      return state;
    } on MissingPluginException {
      // No platform end here: leveling degrades to attenuation only.
      Log.debug('loudness', 'the pre-amp is not available on this platform');
      return null;
    } on PlatformException catch (e) {
      Log.warn('loudness', 'pre-amp refused by the platform', error: e);
      return null;
    } on TimeoutException catch (e) {
      Log.warn('loudness', 'pre-amp did not answer', error: e);
      return null;
    }
  }
}
