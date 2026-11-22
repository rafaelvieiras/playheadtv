import 'package:flutter_test/flutter_test.dart';
import 'package:client/playback_promotion.dart';
import 'package:client/video/video_engine.dart';

PlaybackState state({Duration position = Duration.zero, bool hasError = false, bool isPlaying = true}) =>
    PlaybackState(position: position, hasError: hasError, isPlaying: isPlaying, isInitialized: true);

void main() {
  group('judgePromotion', () {
    test('an item that is reading its source is promoted without waiting out the bound', () {
      final verdict = judgePromotion(
        state: state(position: const Duration(milliseconds: 200)),
        startedAt: Duration.zero,
        waited: const Duration(milliseconds: 100),
      );
      expect(verdict, PromotionVerdict.promote);
    });

    test('an item with no progress yet is given time', () {
      final verdict = judgePromotion(
        state: state(),
        startedAt: Duration.zero,
        waited: const Duration(milliseconds: 100),
      );
      expect(verdict, PromotionVerdict.wait);
    });

    test('an item that never says anything is promoted at the bound', () {
      final verdict = judgePromotion(
        state: state(),
        startedAt: Duration.zero,
        waited: playbackPromotionBound,
      );
      expect(verdict, PromotionVerdict.promote);
    });

    test('a failure reopens the unit even after it had started', () {
      final verdict = judgePromotion(
        state: state(position: const Duration(seconds: 2), hasError: true),
        startedAt: Duration.zero,
        waited: const Duration(milliseconds: 300),
      );
      expect(verdict, PromotionVerdict.reopen);
    });

    test('a failure outranks the bound as well', () {
      final verdict = judgePromotion(
        state: state(hasError: true),
        startedAt: Duration.zero,
        waited: playbackPromotionBound,
      );
      expect(verdict, PromotionVerdict.reopen);
    });

    test('progress is measured from where the item was told to play', () {
      const startedAt = Duration(seconds: 12);
      expect(
        judgePromotion(
          state: state(position: startedAt),
          startedAt: startedAt,
          waited: const Duration(milliseconds: 100),
        ),
        PromotionVerdict.wait,
      );
      expect(
        judgePromotion(
          state: state(position: startedAt + const Duration(milliseconds: 120)),
          startedAt: startedAt,
          waited: const Duration(milliseconds: 100),
        ),
        PromotionVerdict.promote,
      );
    });
  });
}
