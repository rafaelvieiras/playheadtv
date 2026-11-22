import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'package:client/services/loudness_service.dart';

void main() {
  group('loudnessGain', () {
    test('brings a level down to the target', () {
      // 6 dB louder than the target: half the amplitude.
      final gain = loudnessGain(lufs: -18.0, peakDb: -1.0, targetLufs: -24.0);
      expect(gain, closeTo(0.5012, 0.001));
    });

    test('asks for a boost that the volume clamp then refuses', () {
      // 4 dB below the target: the raw gain is above 1, and attenuation is all a
      // player volume can do, so the clamp leaves it where it is.
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

  group('loudnessGain with the pre-amp in force', () {
    test('the pre-amp alone is enough for some items', () {
      // -30 LUFS plus +12 dB lands exactly on the -18 target: nothing left for
      // the player volume to do, so it stays at 1.
      final gain = loudnessGain(lufs: -30.0, peakDb: -20.0, targetLufs: -18.0, preampDb: 12);
      expect(gain, closeTo(1.0, 0.001));
      expect(clampLoudnessGain(gain), 1.0);
    });

    test('a level the pre-amp cannot reach asks for more than a volume can give', () {
      // -33 LUFS plus +12 dB is still 3 dB under the target; the raw gain says
      // "boost", and the clamp refuses — that is what the pre-amp is for.
      final raw = loudnessGain(lufs: -33.0, peakDb: -20.0, targetLufs: -18.0, preampDb: 12);
      expect(raw, closeTo(1.412, 0.01));
      expect(clampLoudnessGain(raw), 1.0);
    });

    test('a loud item is cut by what is left after the pre-amp', () {
      // -10 LUFS with +12 dB of pre-amp is 20 dB above -18: a tenth of the
      // amplitude, which is exactly the job the player volume does now.
      final gain = loudnessGain(lufs: -10.0, peakDb: -5.0, targetLufs: -18.0, preampDb: 12);
      expect(gain, closeTo(0.1, 0.001));
    });

    test('the pre-amp counts toward the peak ceiling too', () {
      // A clipped master: -25 LUFS average with a peak already at +2 dBFS. The
      // pre-amp would push it to +14, so the peak term decides, not the level.
      final gain = loudnessGain(lufs: -25.0, peakDb: 2.0, targetLufs: -18.0, preampDb: 12);
      expect(gain, closeTo(0.1778, 0.001)); // -15 dB, the peak term
    });
  });

  group('readLoudnessPreamp', () {
    test('reads the config file value', () {
      expect(readLoudnessPreamp({'loudness_preamp_db': 6}), 6.0);
      expect(readLoudnessPreamp({'loudness_preamp_db': 0}), 0.0);
    });

    test('falls back to the default and ignores an impossible value', () {
      expect(readLoudnessPreamp({}), loudnessPreampDbDefault);
      expect(readLoudnessPreamp({'loudness_preamp_db': -3}), loudnessPreampDbDefault);
      expect(readLoudnessPreamp({'loudness_preamp_db': 40}), loudnessPreampDbDefault);
    });
  });

  group('settings choices', () {
    test('cycle and wrap, and describe what they mean', () {
      expect(nextLoudnessTargetChoice(-18), -20);
      expect(nextLoudnessTargetChoice(-22), loudnessTargetChoices.first);
      expect(nextLoudnessPreampChoice(12), 15);
      expect(nextLoudnessPreampChoice(15), loudnessPreampChoices.first);
      expect(loudnessPreampLabel(0), 'Off');
      expect(loudnessPreampLabel(12), '+12 dB');
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
  });

  group('LoudnessService', () {
    setUp(() => SharedPreferences.setMockInitialValues({}));

    test('remembers an item and leaves it as the channel level', () async {
      final service = await LoudnessService.create();
      expect(service.itemGain('item-1'), isNull);
      expect(service.channelGain('ch-1'), isNull);

      await service.remember('item-1', 'ch-1', 0.5);

      expect(service.itemGain('item-1'), 0.5);
      expect(service.channelGain('ch-1'), 0.5);
    });

    test('a stored value outside 0..1 is clamped, nonsense is ignored', () async {
      SharedPreferences.setMockInitialValues({
        'loudness_gain_item_high': 4.0,
        'loudness_gain_item_nan': 0.0,
      });
      final service = await LoudnessService.create();
      expect(service.itemGain('high'), 1.0);
      expect(service.itemGain('nan'), 0.0);
      expect(service.itemGain('absent'), isNull);
      expect(service.itemGain(''), isNull);
    });
  });

  group('LoudnessService forgetting', () {
    test('a track change drops the item gain but keeps the channel prior', () async {
      SharedPreferences.setMockInitialValues({});
      final service = await LoudnessService.create();
      await service.remember('item-1', 'channel-1', 0.4);
      expect(service.itemGain('item-1'), 0.4);
      await service.forgetItem('item-1');
      expect(service.itemGain('item-1'), isNull);
      expect(service.channelGain('channel-1'), closeTo(0.4, 1e-9));
    });

    test('clearing items leaves the channel prior alone', () async {
      SharedPreferences.setMockInitialValues({});
      final service = await LoudnessService.create();
      await service.remember('item-1', 'channel-1', 0.5);
      await service.remember('item-2', 'channel-1', 0.7);
      await service.clearItems();
      expect(service.itemGain('item-1'), isNull);
      expect(service.itemGain('item-2'), isNull);
      expect(service.channelGain('channel-1'), closeTo(0.7, 1e-9));
    });
  });
}
