// ReplayGain-style leveling, decided per piece of audio: one lift for the whole
// output mix, plus the player's own volume.
//
// Why a static gain and not a compressor: nothing is re-encoded, nothing is
// transcoded, and the audio path stays exactly as it is. Two knobs together
// cover the whole library:
//   * one boost, applied by the platform effect on the output mix
//     (AudioPreamp.kt), which is the only way to get gain above unity;
//   * the player's own volume, which can only attenuate, for everything the
//     boost pushed past the target.
//
// The boost follows the item that needs it. It used to be a fixed number, and
// that is what made the whole app quiet: with a fixed +9 dB of pre-amp, nothing
// could be delivered above "its own level + 9", so every item quieter than
// `target - 9` (the library reaches -39 LUFS) played below the target with the
// volume already at 1.0 and nothing left to raise it with. Measured on the
// library: 7 of 13 consecutive items reported gain 0.0 dB — the ceiling.
//
// So the boost is `target - lufs` of the quietest item alive (0 when everything
// is already loud enough, and capped: past the cap the platform limiter works on
// every item, which is heard as pumping), and each player's volume is the
// attenuation that is left over. The lift applies to the mix, so during a fade
// both sides take the same boost and each one's volume keeps it on the target.
//
// Two memories live here, and both store the MEASUREMENT, never a volume: a
// volume only means anything together with the boost it was computed against.
//   * per item, the level measured for that item (exact, survives restarts);
//   * per channel, the last level applied (a channel change has no lead time, so
//     something has to be in force for the first seconds).
import 'dart:math' as math;

import 'package:shared_preferences/shared_preferences.dart';

/// The loudest a stream is allowed to be after the gain, in dBFS. Leaves a
/// decibel of headroom for inter-sample peaks, which a sample peak does not see.
const double loudnessPeakCeilingDb = -1.0;

/// Loudness to aim at, in LUFS.
///
/// -14 is where the streaming services sit, and it is the number that decides
/// what the viewer's television volume has to be: an app that lands 4 dB under
/// its neighbours makes everyone raise the volume for it, and every other app is
/// then too loud when they leave.
const double loudnessTargetLufsDefault = -14.0;

/// The most the app lifts any item, in dB.
///
/// Past this the platform limiter is doing most of the work on every quiet item,
/// which is audible as pumping. It is a ceiling, not a fixed lift: an item that
/// needs less gets less.
const double loudnessMaxBoostDbDefault = 15.0;

/// Ceiling of the setting itself: a boost beyond this is a mixer, not leveling.
const double loudnessMaxBoostDbCeiling = 15.0;

/// Never cut more than this: a walking-tour-quiet measurement (a fade-in, a
/// silent opening) must not mute the channel. Wider than it looks, because the
/// boost already lifted the signal: a destroyed master 30 dB above the target
/// needs the player at 3%.
const double loudnessMinGain = 0.02; // about -34 dB

/// The loud end of what the library holds, in LUFS. The measured sample reached
/// -3.2 (music clips go higher); this leaves room above it so that a guess made
/// from it is never quieter than the material it is guessing about.
const double loudnessConservativeLufs = -9.0;

/// One measured piece of audio: how loud it is, and how high its peak goes.
class LoudnessLevel {
  const LoudnessLevel({required this.lufs, required this.peakDb});

  /// Integrated loudness (ITU-R BS.1770), in LUFS.
  final double lufs;

  /// Sample peak, in dBFS.
  final double peakDb;

  /// The conservative combination of two windows of the same item: the louder
  /// average and the higher peak. A level is trusted forever once remembered, so
  /// a wrong one is worse than none — each term takes the side that needs less
  /// help.
  LoudnessLevel louderOf(LoudnessLevel other) => LoudnessLevel(
        lufs: math.max(lufs, other.lufs),
        peakDb: math.max(peakDb, other.peakDb),
      );

  @override
  bool operator ==(Object other) =>
      other is LoudnessLevel && other.lufs == lufs && other.peakDb == peakDb;

  @override
  int get hashCode => Object.hash(lufs, peakDb);

  @override
  String toString() => 'lufs=${lufs.toStringAsFixed(2)} peak=${peakDb.toStringAsFixed(2)}dB';

  /// `lufs|peak` — what is stored on the device. A volume is deliberately not
  /// stored: it only means anything next to the boost it was computed against.
  String encode() => '$lufs|$peakDb';

  static LoudnessLevel? decode(String? raw) {
    if (raw == null) return null;
    final parts = raw.split('|');
    if (parts.length != 2) return null;
    final lufs = double.tryParse(parts[0]);
    final peak = double.tryParse(parts[1]);
    if (lufs == null || peak == null || !lufs.isFinite || !peak.isFinite) return null;
    return LoudnessLevel(lufs: lufs, peakDb: peak);
  }
}

/// How far [lufs] is from [targetLufs], in dB. Positive means it needs lifting.
double loudnessNeedDb({required double lufs, required double targetLufs}) => targetLufs - lufs;

/// The lift the output mix has to carry for everything alive at once.
///
/// The mix is one number, so overlapping items (a crossfade) share it: it is the
/// biggest need of the set, never negative (a loud item is cut by its own volume,
/// not by pulling the mix down under a quiet neighbour), and never past
/// [maxBoostDb] — the setting, which [readLoudnessMaxBoost] already keeps inside
/// [loudnessMaxBoostDbCeiling].
double loudnessBoostDb(
  Iterable<double> needDbs, {
  double maxBoostDb = loudnessMaxBoostDbDefault,
}) {
  var boost = 0.0;
  for (final need in needDbs) {
    if (need.isFinite && need > boost) boost = need;
  }
  final ceiling = maxBoostDb.isFinite ? math.max(0.0, maxBoostDb) : 0.0;
  return boost.clamp(0.0, ceiling);
}

/// The gain to apply to content measured at [lufs] with a peak of [peakDb], as a
/// 0..1 multiplier for the player's volume, given that [boostDb] is already
/// lifting the output.
///
/// Both terms are attenuations and the smaller one wins: one brings the average
/// level to [targetLufs] after the boost, the other keeps the peak under
/// [peakCeilingDb] after the boost too (a 2010 film measured +0.2 dBTP, which no
/// average-level target catches).
double loudnessGain({
  required double lufs,
  required double peakDb,
  required double targetLufs,
  double boostDb = 0,
  double peakCeilingDb = loudnessPeakCeilingDb,
}) {
  final byLoudness = math.pow(10.0, (targetLufs - lufs - boostDb) / 20.0).toDouble();
  final byPeak = math.pow(10.0, (peakCeilingDb - peakDb - boostDb) / 20.0).toDouble();
  return byLoudness < byPeak ? byLoudness : byPeak;
}

/// A player volume as dB, for a log line or a comparison: 1.0 is 0 dB.
double volumeGainDb(double volume) => volume <= 0 ? -double.infinity : 20 * math.log(volume) / math.ln10;

/// [loudnessGain] clamped to what a player volume can express and to the floor
/// that keeps a bad measurement from silencing a channel.
double clampLoudnessGain(double gain) {
  if (!gain.isFinite) return 1.0;
  return gain.clamp(loudnessMinGain, 1.0);
}

/// The volume to enter an item nothing is known about yet: the loudest material
/// the library holds, peak on the ceiling.
///
/// The first correction after this can only go up, which is the whole point. A
/// channel change used to inherit the outgoing item's level instead, so a quiet
/// item (volume near 1.0) handing over to a loud one played the loud one at the
/// quiet one's volume: several dB above target until the measurement and the
/// fade caught up, heard as "it was much louder for a moment". Entering low
/// costs a short swell on a quiet item; entering high costs a burst, and only
/// one of the two is worth risking.
double loudnessConservativeGain({
  double targetLufs = loudnessTargetLufsDefault,
  double boostDb = 0,
  double peakCeilingDb = loudnessPeakCeilingDb,
}) =>
    clampLoudnessGain(loudnessGain(
      lufs: loudnessConservativeLufs,
      peakDb: peakCeilingDb,
      targetLufs: targetLufs,
      boostDb: boostDb,
      peakCeilingDb: peakCeilingDb,
    ));

/// The configured target from the config file (`loudness_target_lufs`), or the
/// default when it is absent or out of range. A target above 0 LUFS or below
/// -40 cannot come from a sane installation.
double readLoudnessTarget(Map<String, Object?> file) {
  final value = file['loudness_target_lufs'];
  if (value is num && value.isFinite && value <= 0 && value >= -40) return value.toDouble();
  return loudnessTargetLufsDefault;
}

/// The configured ceiling for the automatic boost (`loudness_max_boost_db`), or
/// the default when it is absent or out of range. Zero is legitimate: it turns
/// the lift off and leaves attenuation only.
///
/// The old key was `loudness_preamp_db`, a fixed lift. It is not read: the same
/// number now means a ceiling, and a stored "9" would silently cap a quiet item
/// 6 dB under the target — the exact problem this ceiling replaced.
double readLoudnessMaxBoost(Map<String, Object?> file) {
  final value = file['loudness_max_boost_db'];
  if (value is num && value.isFinite && value >= 0 && value <= loudnessMaxBoostDbCeiling) {
    return value.toDouble();
  }
  return loudnessMaxBoostDbDefault;
}

/// The window to measure for an item, from [startAt] to the end of the item.
///
/// A bumper is five seconds long: asking for a full window from its own start
/// decodes past its end and costs the wall time of a window for a level that
/// half of it already settles. So the window is half of what is left of the
/// item, never more than [standard] and never less than [floor] (the meter needs
/// 1.6 s of signal before it counts four blocks and says anything at all).
Duration loudnessMeasureWindow({
  required Duration clipDuration,
  Duration startAt = Duration.zero,
  Duration standard = loudnessMeasureStandardWindow,
}) {
  final remaining = clipDuration - startAt;
  if (remaining <= Duration.zero) return standard;
  final half = Duration(microseconds: remaining.inMicroseconds ~/ 2);
  if (half >= standard) return standard;
  return half < loudnessMeasureFloor ? loudnessMeasureFloor : half;
}

/// The standard measurement window: 5 s is enough for the integrated loudness of
/// a stretch, and long enough for the meter to see past a quiet opening.
const loudnessMeasureStandardWindow = Duration(seconds: 5);

/// The shortest window worth asking for: the meter counts 400 ms blocks and
/// needs four of them, so anything under 1.6 s of signal cannot become a level.
const loudnessMeasureFloor = Duration(seconds: 2);

/// Items and channels whose level is remembered on the device.
class LoudnessService {
  LoudnessService(this._prefs);

  // A volume stored under the old prefix would be read as a level, so the
  // prefix changed with the meaning: an install that measured under the old
  // model simply measures again (and the old keys are ignored, not trusted).
  static const _itemPrefix = 'loudness_level_item_';
  static const _channelPrefix = 'loudness_level_channel_';
  static const _legacyPrefixes = ['loudness_gain_item_', 'loudness_gain_channel_'];

  final SharedPreferences _prefs;

  static Future<LoudnessService> create() async =>
      LoudnessService(await SharedPreferences.getInstance());

  /// Level measured for this item before, if any.
  LoudnessLevel? itemLevel(String itemId) => _read(_itemPrefix, itemId);

  /// Level the channel was last leveled to, used at boot, before anything is
  /// playing, so a channel change from an earlier session does not start from
  /// nothing.
  LoudnessLevel? channelLevel(String channelId) => _read(_channelPrefix, channelId);

  /// Remembers an item's level, and leaves it as the channel's too: the next
  /// boot starts from something that was right a moment ago.
  Future<void> remember(String itemId, String? channelId, LoudnessLevel level) async {
    if (!level.lufs.isFinite || !level.peakDb.isFinite) return;
    await _prefs.setString('$_itemPrefix$itemId', level.encode());
    if (channelId != null && channelId.isNotEmpty) {
      await _prefs.setString('$_channelPrefix$channelId', level.encode());
    }
  }

  /// Forgets every measured level (the Settings pages call this when leveling is
  /// turned off or a knob changes, so nothing is trusted from a model the viewer
  /// no longer uses).
  Future<void> clearItems() async {
    final prefixes = [_itemPrefix, _channelPrefix, ..._legacyPrefixes];
    for (final key in _prefs.getKeys().where((k) => prefixes.any(k.startsWith)).toList()) {
      await _prefs.remove(key);
    }
  }

  /// Forgets an item's level. Needed when the audio track changes: the same item
  /// can carry two tracks 20 dB apart (a level-matched dub and an untouched
  /// original), so a level measured on one is wrong for the other.
  Future<void> forgetItem(String itemId) async {
    if (itemId.isEmpty) return;
    await _prefs.remove('$_itemPrefix$itemId');
  }

  LoudnessLevel? _read(String prefix, String id) {
    if (id.isEmpty) return null;
    return LoudnessLevel.decode(_prefs.getString('$prefix$id'));
  }
}

/// Choices offered in Settings for the leveling target, in LUFS. -14 is the
/// default: the level the streaming services sit at, so the app needs the same
/// television volume as everything else.
const loudnessTargetChoices = <double>[-14, -16, -18, -20, -22];

/// Choices offered in Settings for the maximum boost, in dB. Past +15 the
/// platform limiter dominates every quiet item, which sounds like pumping.
const loudnessMaxBoostChoices = <double>[0, 6, 9, 12, 15];

/// The choice after [current], wrapping. Unknown values fall back to the first.
double _nextChoice(List<double> choices, double current) {
  final index = choices.indexWhere((choice) => (choice - current).abs() < 0.01);
  return choices[(index + 1) % choices.length];
}

double nextLoudnessTargetChoice(double current) => _nextChoice(loudnessTargetChoices, current);

double nextLoudnessMaxBoostChoice(double current) => _nextChoice(loudnessMaxBoostChoices, current);

String loudnessTargetLabel(double value) => '${value.toStringAsFixed(0)} LUFS';

String loudnessMaxBoostLabel(double value) => value <= 0 ? 'Off' : '+${value.toStringAsFixed(0)} dB';
