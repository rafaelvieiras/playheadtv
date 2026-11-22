import 'package:flutter_test/flutter_test.dart';
import 'package:client/playback_stall.dart';

void main() {
  final t0 = DateTime.utc(2026, 11, 2, 12);

  test('a moving position is never a stall', () {
    final watcher = StallWatcher();
    var position = Duration.zero;
    var now = t0;
    for (var i = 0; i < 10; i++) {
      expect(watcher.observe(position: position, playing: true, now: now), StallState.progressing);
      position += const Duration(seconds: 6);
      now = now.add(const Duration(seconds: 6));
    }
  });

  test('a position that stops moving is a stall once the bound passes', () {
    final watcher = StallWatcher();
    expect(watcher.observe(position: const Duration(seconds: 30), playing: true, now: t0), StallState.progressing);
    expect(watcher.observe(position: const Duration(seconds: 30), playing: true, now: t0.add(const Duration(seconds: 6))),
        StallState.waiting);
    expect(watcher.observe(position: const Duration(seconds: 30), playing: true, now: t0.add(playbackStallBound)),
        StallState.stalled);
  });

  test('a paused or buffering engine never counts against the bound', () {
    final watcher = StallWatcher();
    expect(watcher.observe(position: const Duration(seconds: 30), playing: true, now: t0), StallState.progressing);
    expect(watcher.observe(position: const Duration(seconds: 30), playing: false, now: t0.add(const Duration(seconds: 9))),
        StallState.waiting);
    // The clock restarted: the same position is not instantly stale.
    expect(watcher.observe(position: const Duration(seconds: 30), playing: true, now: t0.add(const Duration(seconds: 10))),
        StallState.progressing);
    expect(watcher.observe(position: const Duration(seconds: 30), playing: true, now: t0.add(const Duration(seconds: 15))),
        StallState.waiting);
  });

  test('reset forgets the observations', () {
    final watcher = StallWatcher();
    expect(watcher.observe(position: const Duration(seconds: 30), playing: true, now: t0), StallState.progressing);
    watcher.reset();
    expect(watcher.observe(position: const Duration(seconds: 30), playing: true, now: t0.add(playbackStallBound)),
        StallState.progressing);
  });

  group('PictureLostWatcher', () {
    test('a picture present is never a loss', () {
      final watcher = PictureLostWatcher();
      var now = t0;
      for (var i = 0; i < 20; i++) {
        expect(watcher.observe(onAir: true, healthy: true, now: now), isFalse);
        now = now.add(const Duration(seconds: 5));
      }
    });

    test('a channel on air with no picture is lost once the bound passes', () {
      final watcher = PictureLostWatcher();
      expect(watcher.observe(onAir: true, healthy: false, now: t0), isFalse);
      expect(watcher.observe(onAir: true, healthy: false, now: t0.add(const Duration(seconds: 20))), isFalse);
      expect(watcher.observe(onAir: true, healthy: false, now: t0.add(playbackPictureLostBound)), isTrue);
    });

    test('a channel that is not on air never counts', () {
      final watcher = PictureLostWatcher();
      expect(watcher.observe(onAir: false, healthy: false, now: t0), isFalse);
      expect(watcher.observe(onAir: false, healthy: false, now: t0.add(playbackPictureLostBound * 2)), isFalse);
    });

    test('a picture that comes back restarts the clock', () {
      final watcher = PictureLostWatcher();
      expect(watcher.observe(onAir: true, healthy: false, now: t0), isFalse);
      expect(watcher.observe(onAir: true, healthy: true, now: t0.add(const Duration(seconds: 10))), isFalse);
      // The absence counted so far does not carry over.
      expect(watcher.observe(onAir: true, healthy: false, now: t0.add(const Duration(seconds: 11))), isFalse);
      expect(watcher.observe(onAir: true, healthy: false, now: t0.add(const Duration(seconds: 30))), isFalse);
      expect(watcher.observe(onAir: true, healthy: false, now: t0.add(const Duration(seconds: 56))), isTrue);
    });

    test('reset forgets the absence', () {
      final watcher = PictureLostWatcher();
      expect(watcher.observe(onAir: true, healthy: false, now: t0), isFalse);
      watcher.reset();
      expect(watcher.observe(onAir: true, healthy: false, now: t0.add(playbackPictureLostBound)), isFalse);
      expect(watcher.observe(onAir: true, healthy: false, now: t0.add(playbackPictureLostBound * 2)), isTrue);
    });
  });
}
