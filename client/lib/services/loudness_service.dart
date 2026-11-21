// ReplayGain-style leveling, decided per piece of audio and applied to the
// player's own volume.
//
// Why a static gain and not a compressor: nothing is re-encoded, nothing is
// transcoded, and the audio path stays exactly as it is. The player's volume
// only goes down (setVolume takes 0..1), so the whole library lands at the
// level of its quietest content and the system volume makes up for it — the
// alternative, boosting the quiet half, would need native gain on the player's
// audio session, which the `video_player` plugin does not expose.
//
// Two memories live here, because the two situations are different:
//   * per item, the gain measured for that item (exact, survives restarts);
//   * per channel, the last gain applied (a channel change has no lead time, so
//     something has to be in force for the first seconds).
import 'dart:math' as math;

import 'package:shared_preferences/shared_preferences.dart';

/// The loudest a stream is allowed to be after the gain, in dBFS. Leaves a
/// decibel of headroom for inter-sample peaks, which a sample peak does not see.
const double loudnessPeakCeilingDb = -1.0;

/// Loudness to aim at, in LUFS. Attenuation only, so this has to sit near the
/// bottom of what the library contains; anything louder than this comes down to
/// it and nothing is pushed up.
const double loudnessTargetLufsDefault = -24.0;

/// Never cut more than this: a walking-tour-quiet measurement (a fade-in, a
/// silent opening) must not mute the channel.
const double loudnessMinGain = 0.18; // about -15 dB

/// The gain to apply to content measured at [lufs] with a peak of [peakDb], as a
/// 0..1 multiplier for the player's volume.
///
/// Both terms are attenuations and the smaller one wins: one brings the average
/// level to [targetLufs], the other keeps the peak under [peakCeilingDb] (a 2010
/// film measured +0.2 dBTP, which no average-level target catches).
double loudnessGain({
  required double lufs,
  required double peakDb,
  required double targetLufs,
  double peakCeilingDb = loudnessPeakCeilingDb,
}) {
  final byLoudness = math.pow(10.0, (targetLufs - lufs) / 20.0).toDouble();
  final byPeak = math.pow(10.0, (peakCeilingDb - peakDb) / 20.0).toDouble();
  return byLoudness < byPeak ? byLoudness : byPeak;
}

/// [loudnessGain] clamped to what a player volume can express and to the floor
/// that keeps a bad measurement from silencing a channel.
double clampLoudnessGain(double gain) {
  if (!gain.isFinite) return 1.0;
  return gain.clamp(loudnessMinGain, 1.0);
}

/// The configured target from the config file (`loudness_target_lufs`), or the
/// default when it is absent or out of range. A target above 0 LUFS or below
/// -40 cannot come from a sane installation.
double readLoudnessTarget(Map<String, Object?> file) {
  final value = file['loudness_target_lufs'];
  if (value is num && value.isFinite && value <= 0 && value >= -40) return value.toDouble();
  return loudnessTargetLufsDefault;
}

/// Items and channels whose gain is remembered on the device.
class LoudnessService {
  LoudnessService(this._prefs);

  static const _itemPrefix = 'loudness_gain_item_';
  static const _channelPrefix = 'loudness_gain_channel_';

  final SharedPreferences _prefs;

  static Future<LoudnessService> create() async =>
      LoudnessService(await SharedPreferences.getInstance());

  /// Gain measured for this item before, if any.
  double? itemGain(String itemId) => _read(_itemPrefix, itemId);

  /// Gain the channel was last leveled to, used while the new item's own
  /// measurement is still in flight.
  double? channelGain(String channelId) => _read(_channelPrefix, channelId);

  /// Remembers an item's gain, and leaves it as the channel's too: the next
  /// channel change starts from something that was right a moment ago.
  Future<void> remember(String itemId, String? channelId, double gain) async {
    if (!gain.isFinite) return;
    await _prefs.setDouble('$_itemPrefix$itemId', gain);
    if (channelId != null && channelId.isNotEmpty) {
      await _prefs.setDouble('$_channelPrefix$channelId', gain);
    }
  }

  double? _read(String prefix, String id) {
    if (id.isEmpty) return null;
    final value = _prefs.getDouble('$prefix$id');
    if (value == null || !value.isFinite) return null;
    return value.clamp(0.0, 1.0);
  }
}
