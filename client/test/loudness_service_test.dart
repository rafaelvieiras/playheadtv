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
}
