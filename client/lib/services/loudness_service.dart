// ReplayGain-style leveling, decided per piece of audio and applied to the
// player's own volume, on top of a global pre-amp.
//
// Why a static gain and not a compressor: nothing is re-encoded, nothing is
// transcoded, and the audio path stays exactly as it is. Two knobs together
// cover the whole library:
//   * a fixed pre-amp, applied by the platform effect on the output mix
//     (AudioPreamp.kt), which is the only way to get gain above unity;
//   * the player's own volume, which can only attenuate, for everything louder
//     than the target once the pre-amp is in force.
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

/// Loudness to aim at, in LUFS. With the pre-amp below, this is the level the
/// whole library lands on: quieter material is lifted by the pre-amp and louder
/// material is cut by the player's volume.
const double loudnessTargetLufsDefault = -18.0;

/// Default gain of the global pre-amp, in dB. The library measured -31.3 to
/// -3.2 LUFS (28 dB of spread), so lifting by this much is what makes the quiet
/// end reachable; anything louder than the target keeps being attenuated.
const double loudnessPreampDbDefault = 12.0;

/// Ceiling for the pre-amp. Past this the platform limiter is doing most of the
/// work on every item, which is audible as pumping.
const double loudnessPreampMaxDb = 15.0;

/// Never cut more than this: a walking-tour-quiet measurement (a fade-in, a
/// silent opening) must not mute the channel. Wider than it looks, because the
/// pre-amp already lifted the signal: a destroyed master 15 dB above the target
/// needs the player at 5%.
const double loudnessMinGain = 0.02; // about -34 dB

/// The gain to apply to content measured at [lufs] with a peak of [peakDb], as a
/// 0..1 multiplier for the player's volume, given that [preampDb] is already
/// lifting the output.
///
/// Both terms are attenuations and the smaller one wins: one brings the average
/// level to [targetLufs] after the pre-amp, the other keeps the peak under
/// [peakCeilingDb] after the pre-amp too (a 2010 film measured +0.2 dBTP, which
/// no average-level target catches).
double loudnessGain({
  required double lufs,
  required double peakDb,
  required double targetLufs,
  double preampDb = 0,
  double peakCeilingDb = loudnessPeakCeilingDb,
}) {
  final byLoudness = math.pow(10.0, (targetLufs - lufs - preampDb) / 20.0).toDouble();
  final byPeak = math.pow(10.0, (peakCeilingDb - peakDb - preampDb) / 20.0).toDouble();
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

/// The configured pre-amp from the config file (`loudness_preamp_db`), or the
/// default when absent. Zero is legitimate: it turns the boost off and leaves
/// attenuation only.
double readLoudnessPreamp(Map<String, Object?> file) {
  final value = file['loudness_preamp_db'];
  if (value is num && value.isFinite && value >= 0 && value <= loudnessPreampMaxDb) {
    return value.toDouble();
  }
  return loudnessPreampDbDefault;
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

  /// Forgets every item gain. Needed when the target or the pre-amp changes:
  /// the remembered gains were computed against the old pair.
  Future<void> clearItems() async {
    for (final key in _prefs.getKeys().where((k) => k.startsWith(_itemPrefix)).toList()) {
      await _prefs.remove(key);
    }
  }

  /// Forgets an item's gain. Needed when the audio track changes: the same item
  /// can carry two tracks 20 dB apart (a level-matched dub and an untouched
  /// original), so a gain measured on one is wrong for the other.
  Future<void> forgetItem(String itemId) async {
    if (itemId.isEmpty) return;
    await _prefs.remove('$_itemPrefix$itemId');
  }

  double? _read(String prefix, String id) {
    if (id.isEmpty) return null;
    final value = _prefs.getDouble('$prefix$id');
    if (value == null || !value.isFinite) return null;
    return value.clamp(0.0, 1.0);
  }
}

/// Choices offered in Settings for the leveling target, in LUFS. -18 is the
/// default: with the pre-amp it lifts the library about 4 dB on average and
/// leaves a final spread near 1 dB (measured over 47 items).
const loudnessTargetChoices = <double>[-16, -18, -20, -22];

/// Choices offered in Settings for the pre-amp, in dB. Past +15 the platform
/// limiter dominates every item, which sounds like pumping.
const loudnessPreampChoices = <double>[0, 6, 9, 12, 15];

/// The choice after [current], wrapping. Unknown values fall back to the first.
double _nextChoice(List<double> choices, double current) {
  final index = choices.indexWhere((choice) => (choice - current).abs() < 0.01);
  return choices[(index + 1) % choices.length];
}

double nextLoudnessTargetChoice(double current) => _nextChoice(loudnessTargetChoices, current);

double nextLoudnessPreampChoice(double current) => _nextChoice(loudnessPreampChoices, current);

String loudnessTargetLabel(double value) => '${value.toStringAsFixed(0)} LUFS';

String loudnessPreampLabel(double value) => value <= 0 ? 'Off' : '+${value.toStringAsFixed(0)} dB';
