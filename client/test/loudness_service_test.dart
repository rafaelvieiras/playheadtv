import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'package:client/services/loudness_service.dart';

/// What a viewer hears for a level under a plan (boost plus volume), in LUFS.
double heardLufs(LoudnessLevel level, {required double boostDb, required double volume}) =>
    level.lufs + boostDb + volumeGainDb(volume);

/// The peak that leaves the app, in dBFS.
double outPeakDb(LoudnessLevel level, {required double boostDb, required double volume}) =>
    level.peakDb + boostDb + volumeGainDb(volume);

/// The volume the app applies to [level] under [boostDb].
double plannedVolume(LoudnessLevel level, {required double boostDb, double targetLufs = -14}) =>
    clampLoudnessGain(loudnessGain(
        lufs: level.lufs, peakDb: level.peakDb, targetLufs: targetLufs, boostDb: boostDb));

void main() {
  group('loudnessGain', () {
    test('brings a level down to the target', () {
      // 6 dB louder than the target: half the amplitude.
      final gain = loudnessGain(lufs: -18.0, peakDb: -1.0, targetLufs: -24.0);
      expect(gain, closeTo(0.5012, 0.001));
    });

    test('asks for a boost that the volume clamp then refuses', () {
      // 4 dB below the target: the raw gain is above 1, and attenuation is all a
      // player volume can do — which is why the boost, not the volume, is what
      // lifts a quiet item (see loudnessBoostDb).
      final raw = loudnessGain(lufs: -28.0, peakDb: -20.0, targetLufs: -24.0);
      expect(raw, closeTo(1.585, 0.001));
      expect(clampLoudnessGain(raw), 1.0);
    });

    test('the peak wins over the average when the content clips', () {
      // Loud on average is handled by the level term, but a peak over the
      // ceiling decides on its own: this is the +0.2 dBTP film.
      final gain = loudnessGain(lufs: -24.0, peakDb: 0.2, targetLufs: -24.0);
      expect(gain, closeTo(0.8710, 0.001)); // -1.2 dB
    });
  });

  group('loudnessGain with the boost in force', () {
    test('a boost that reaches the target still answers what the peak allows', () {
      // -39.4 LUFS (the quietest item measured) with the +25.4 dB its average
      // needs. Its crest is 16.7 dB, so +25.4 would put the peak at +2.7 dBFS:
      // the peak term takes 3.7 dB back and the item tops out 3.7 dB under the
      // target. Physics, not a bug — the alternative is clipping.
      final gain = loudnessGain(lufs: -39.4, peakDb: -22.7, targetLufs: -14.0, boostDb: 25.4);
      expect(gain, closeTo(0.6531, 0.001)); // -3.7 dB
      expect(clampLoudnessGain(gain), closeTo(0.6531, 0.001));
    });

    test('a boost the ceiling cut short asks for more than a volume can give', () {
      // The same item when the ceiling only allows +15: the raw gain says "10.4 dB
      // more", the clamp refuses, and the item lands 10.4 dB under the target.
      // That gap is why the ceiling is a setting and not a constant fact.
      final raw = loudnessGain(lufs: -39.4, peakDb: -22.7, targetLufs: -14.0, boostDb: 15);
      expect(raw, closeTo(2.1627, 0.01));
      expect(clampLoudnessGain(raw), 1.0);
    });

    test('a loud item is cut by what is left after the boost', () {
      // -10 LUFS with +12 dB of boost is 16 dB above the -14 target: a sixth of
      // the amplitude, which is the job the player volume does. The peak term
      // would allow more (-8 dB), so the level term is the one that decides.
      final gain = loudnessGain(lufs: -10.0, peakDb: -5.0, targetLufs: -14.0, boostDb: 12);
      expect(gain, closeTo(0.1585, 0.001));
    });

    test('the boost counts toward the peak ceiling too', () {
      // A clipped master: -25 LUFS average with a peak already at +2 dBFS. The
      // boost would push it to +17, so the peak term decides, not the level.
      final gain = loudnessGain(lufs: -25.0, peakDb: 2.0, targetLufs: -14.0, boostDb: 15);
      expect(gain, closeTo(0.1259, 0.001)); // -18 dB, the peak term
    });
  });

  group('loudnessBoostDb', () {
    test('an item quieter than the target gets exactly what it needs', () {
      // The item measured on a real box: -21.90 LUFS at a -14.78 dBFS peak (a
      // 7 dB crest, which is what a film's audio looks like). It needs 7.9 dB,
      // the volume stays at 1, and it lands on the target with headroom to spare.
      const level = LoudnessLevel(lufs: -21.90, peakDb: -14.78);
      final boost = loudnessBoostDb([loudnessNeedDb(lufs: level.lufs, targetLufs: -14)]);
      expect(boost, closeTo(7.9, 1e-9));
      final volume = plannedVolume(level, boostDb: boost);
      expect(volume, 1.0);
      expect(heardLufs(level, boostDb: boost, volume: volume), closeTo(-14, 0.01));
      expect(outPeakDb(level, boostDb: boost, volume: volume), closeTo(-6.88, 0.01));
    });

    test('a peak stops the lift before the target does', () {
      // Very dynamic, very quiet: 16.7 dB of crest. Lifting it to the target
      // would put its peak at +2.7 dBFS, so the peak ceiling decides and the item
      // tops out 3.7 dB under the target. No clipping, and no pretending.
      const level = LoudnessLevel(lufs: -39.4, peakDb: -22.7);
      final boost = loudnessBoostDb([loudnessNeedDb(lufs: level.lufs, targetLufs: -14)], maxBoostDb: 40);
      expect(boost, closeTo(25.4, 1e-9));
      final volume = plannedVolume(level, boostDb: boost);
      expect(volume, closeTo(0.6531, 0.001));
      expect(outPeakDb(level, boostDb: boost, volume: volume), closeTo(loudnessPeakCeilingDb, 0.01));
      expect(heardLufs(level, boostDb: boost, volume: volume), closeTo(-17.7, 0.05));
    });

    test('with the default ceiling the ceiling, not the peak, is what stops it', () {
      // The same item under the shipped +15: still 10.4 dB short of the target
      // and its peak 6.7 dB below the ceiling. The lift simply ran out.
      const level = LoudnessLevel(lufs: -39.4, peakDb: -22.7);
      final boost = loudnessBoostDb([loudnessNeedDb(lufs: level.lufs, targetLufs: -14)]);
      expect(boost, loudnessMaxBoostDbDefault);
      final volume = plannedVolume(level, boostDb: boost);
      expect(volume, 1.0);
      expect(heardLufs(level, boostDb: boost, volume: volume), closeTo(-24.4, 0.01));
    });

    test('loud material never pulls the mix down', () {
      // Nothing needs lifting: the boost is off and the volume does the cutting.
      expect(loudnessBoostDb([loudnessNeedDb(lufs: -6, targetLufs: -14)]), 0);
      expect(loudnessBoostDb([]), 0);
    });

    test('the quietest item of a fade decides, and each side keeps its own level', () {
      // Two items 9.4 dB apart crossing over. One number for the mix, and each
      // volume stays whatever puts its own side on the target: the loud one is
      // cut by the boost the quiet one needs, instead of being heard 9 dB loud
      // for the length of the fade.
      const quiet = LoudnessLevel(lufs: -23.4, peakDb: -16.0);
      const loud = LoudnessLevel(lufs: -14.0, peakDb: -7.0);
      final boost = loudnessBoostDb([
        loudnessNeedDb(lufs: quiet.lufs, targetLufs: -14),
        loudnessNeedDb(lufs: loud.lufs, targetLufs: -14),
      ]);
      expect(boost, closeTo(9.4, 1e-9));
      expect(heardLufs(quiet, boostDb: boost, volume: plannedVolume(quiet, boostDb: boost)),
          closeTo(-14, 0.05));
      final loudVolume = plannedVolume(loud, boostDb: boost);
      expect(loudVolume, closeTo(0.3388, 0.001)); // -9.4 dB
      expect(heardLufs(loud, boostDb: boost, volume: loudVolume), closeTo(-14, 0.05));
    });

    test('never past the ceiling it is given, and a zero ceiling means no lift', () {
      expect(loudnessBoostDb([40], maxBoostDb: 15), 15);
      expect(loudnessBoostDb([40], maxBoostDb: 9), 9);
      expect(loudnessBoostDb([40], maxBoostDb: 0), 0);
      // The setting is what keeps a ceiling sane (readLoudnessMaxBoost refuses
      // anything past loudnessMaxBoostDbCeiling); this function honours its input.
      expect(readLoudnessMaxBoost({'loudness_max_boost_db': 60}), loudnessMaxBoostDbDefault);
    });
  });

  group('the regression this replaced', () {
    test('a fixed lift could not reach an item 25 dB under the target', () {
      // The measured case: -39.4 LUFS, a -14 target, and the fixed +9 dB the
      // device was running. The volume is already at 1.0 and 11.6 dB are missing
      // — "everything plays quiet and the television volume gets raised".
      const level = LoudnessLevel(lufs: -39.4, peakDb: -22.7);
      final fixed = loudnessGain(lufs: level.lufs, peakDb: level.peakDb, targetLufs: -14, boostDb: 9);
      expect(clampLoudnessGain(fixed), 1.0);
      expect(heardLufs(level, boostDb: 9, volume: 1.0), closeTo(-30.4, 0.01));
      // With the boost following the item and the default +15 ceiling, the same
      // material is lifted to its own headroom (the peak ceiling, not the level,
      // is what stops it): 6.0 dB louder than the fixed lift could make it.
      final boost = loudnessBoostDb(
          [loudnessNeedDb(lufs: level.lufs, targetLufs: -14)],
          maxBoostDb: loudnessMaxBoostDbDefault);
      expect(boost, 15);
      final volume = plannedVolume(level, boostDb: boost);
      expect(heardLufs(level, boostDb: boost, volume: volume), closeTo(-24.4, 0.01));
    });
  });

  group('LoudnessLevel', () {
    test('a stored level round-trips, and nonsense is refused', () {
      const level = LoudnessLevel(lufs: -23.75, peakDb: -1.5);
      expect(LoudnessLevel.decode(level.encode()), level);
      expect(LoudnessLevel.decode(null), isNull);
      expect(LoudnessLevel.decode(''), isNull);
      expect(LoudnessLevel.decode('-23.75'), isNull);
      expect(LoudnessLevel.decode('abc|-1'), isNull);
      expect(LoudnessLevel.decode('NaN|-1'), isNull);
    });

    test('the conservative combination takes the harder half of each window', () {
      const a = LoudnessLevel(lufs: -24, peakDb: -3);
      const b = LoudnessLevel(lufs: -21, peakDb: -6);
      expect(a.louderOf(b), const LoudnessLevel(lufs: -21, peakDb: -3));
    });
  });

  group('readLoudnessMaxBoost', () {
    test('reads the config file value', () {
      expect(readLoudnessMaxBoost({'loudness_max_boost_db': 6}), 6.0);
      expect(readLoudnessMaxBoost({'loudness_max_boost_db': 0}), 0.0);
    });

    test('falls back to the default and ignores an impossible value', () {
      expect(readLoudnessMaxBoost({}), loudnessMaxBoostDbDefault);
      expect(readLoudnessMaxBoost({'loudness_max_boost_db': -3}), loudnessMaxBoostDbDefault);
      expect(readLoudnessMaxBoost({'loudness_max_boost_db': 40}), loudnessMaxBoostDbDefault);
    });

    test('does not read the old fixed pre-amp key as a ceiling', () {
      // A stored 9 was a fixed lift; as a ceiling it would cap a quiet item 6 dB
      // under the target, which is the bug this key replaced.
      expect(readLoudnessMaxBoost({'loudness_preamp_db': 9}), loudnessMaxBoostDbDefault);
    });
  });

  group('settings choices', () {
    test('cycle and wrap, and describe what they mean', () {
      expect(nextLoudnessTargetChoice(-14), -16);
      expect(nextLoudnessTargetChoice(-22), loudnessTargetChoices.first);
      expect(nextLoudnessMaxBoostChoice(12), 15);
      expect(nextLoudnessMaxBoostChoice(15), loudnessMaxBoostChoices.first);
      expect(loudnessMaxBoostLabel(0), 'Off');
      expect(loudnessMaxBoostLabel(12), '+12 dB');
      expect(loudnessTargetLabel(-14), '-14 LUFS');
    });

    test('the default target is one of the choices, so the row starts on it', () {
      expect(loudnessTargetChoices.first, loudnessTargetLufsDefault);
      expect(loudnessMaxBoostChoices.last, loudnessMaxBoostDbDefault);
    });
  });

  group('clampLoudnessGain', () {
    test('never boosts and never mutes', () {
      expect(clampLoudnessGain(2.5), 1.0);
      expect(clampLoudnessGain(0.001), loudnessMinGain);
      expect(clampLoudnessGain(double.negativeInfinity), 1.0);
    });
  });

  group('readLoudnessTarget', () {
    test('reads the config file value', () {
      expect(readLoudnessTarget({'loudness_target_lufs': -22}), -22.0);
    });

    test('ignores a value no sane installation would set', () {
      expect(readLoudnessTarget({'loudness_target_lufs': 12}), loudnessTargetLufsDefault);
      expect(readLoudnessTarget({'loudness_target_lufs': -80}), loudnessTargetLufsDefault);
      expect(readLoudnessTarget(const {}), loudnessTargetLufsDefault);
    });

    test('the default is the level the streaming services sit at', () {
      expect(loudnessTargetLufsDefault, -14);
    });
  });

  group('loudnessMeasureWindow', () {
    test('an item long enough gets the standard window', () {
      expect(
        loudnessMeasureWindow(clipDuration: const Duration(minutes: 22)),
        const Duration(seconds: 5),
      );
      // Measured 20 minutes in, with 2 minutes left: still the standard window.
      expect(
        loudnessMeasureWindow(
            clipDuration: const Duration(minutes: 22), startAt: const Duration(minutes: 20)),
        const Duration(seconds: 5),
      );
    });

    test('a bumper is measured on half of what is left of it', () {
      // The case that matters: a 5 s clip took a full window to measure and the
      // decode ran past its end.
      expect(
        loudnessMeasureWindow(clipDuration: const Duration(seconds: 5)),
        const Duration(milliseconds: 2500),
      );
      expect(
        loudnessMeasureWindow(clipDuration: const Duration(seconds: 30)),
        const Duration(seconds: 5),
      );
    });

    test('never under the floor the meter needs, and never after the end', () {
      expect(
        loudnessMeasureWindow(clipDuration: const Duration(seconds: 3)),
        loudnessMeasureFloor,
      );
      expect(
        loudnessMeasureWindow(clipDuration: const Duration(seconds: 10), startAt: const Duration(seconds: 10)),
        loudnessMeasureStandardWindow,
      );
    });
  });

  group('LoudnessService', () {
    setUp(() => SharedPreferences.setMockInitialValues({}));

    test('remembers an item and leaves it as the channel level', () async {
      final service = await LoudnessService.create();
      expect(service.itemLevel('item-1'), isNull);
      expect(service.channelLevel('ch-1'), isNull);

      const level = LoudnessLevel(lufs: -23.4, peakDb: -6);
      await service.remember('item-1', 'ch-1', level);

      expect(service.itemLevel('item-1'), level);
      expect(service.channelLevel('ch-1'), level);
    });

    test('a stored volume under the old key is not read as a level', () async {
      // The prefix changed with the meaning: a volume means nothing without the
      // boost it was computed against, so an install that measured under the old
      // model simply measures again.
      SharedPreferences.setMockInitialValues({'loudness_gain_item_old': 0.35});
      final service = await LoudnessService.create();
      expect(service.itemLevel('old'), isNull);
      expect(service.itemLevel('absent'), isNull);
      expect(service.itemLevel(''), isNull);
    });
  });

  group('LoudnessService forgetting', () {
    test('a track change drops the item level but keeps the channel prior', () async {
      SharedPreferences.setMockInitialValues({});
      final service = await LoudnessService.create();
      const level = LoudnessLevel(lufs: -20, peakDb: -4);
      await service.remember('item-1', 'channel-1', level);
      expect(service.itemLevel('item-1'), level);
      await service.forgetItem('item-1');
      expect(service.itemLevel('item-1'), isNull);
      expect(service.channelLevel('channel-1'), level);
    });

    test('clearing items leaves the channel prior alone', () async {
      SharedPreferences.setMockInitialValues({});
      final service = await LoudnessService.create();
      const first = LoudnessLevel(lufs: -20, peakDb: -4);
      const second = LoudnessLevel(lufs: -28, peakDb: -9);
      await service.remember('item-1', 'channel-1', first);
      await service.remember('item-2', 'channel-1', second);
      await service.clearItems();
      expect(service.itemLevel('item-1'), isNull);
      expect(service.itemLevel('item-2'), isNull);
      expect(service.channelLevel('channel-1'), isNull);
    });
  });

  group('conservative entry level', () {
    test('is the level of the loudest material, never of an average one', () {
      final guess = loudnessConservativeGain(targetLufs: -14);
      // Quiet material needs far more than this: the correction goes up.
      expect(guess, lessThan(loudnessGain(lufs: -30.0, peakDb: -20.0, targetLufs: -14)));
      // Nothing louder than the guess exists in the library, so it is not risked.
      final loudest = loudnessGain(lufs: loudnessConservativeLufs, peakDb: -1.0, targetLufs: -14);
      expect(guess, closeTo(loudest, 1e-9));
    });

    test('drops when the boost rises, like every other gain', () {
      final off = loudnessConservativeGain(targetLufs: -14, boostDb: 0);
      final on = loudnessConservativeGain(targetLufs: -14, boostDb: 12);
      expect(on, lessThan(off));
    });

    test('never goes under the floor that keeps a channel audible', () {
      final guess = loudnessConservativeGain(targetLufs: -14, boostDb: 15);
      expect(guess, greaterThanOrEqualTo(loudnessMinGain));
    });
  });
}
