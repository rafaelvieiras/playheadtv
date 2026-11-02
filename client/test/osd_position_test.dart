import 'package:client/widgets/osd_overlay.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('a stream that began at an offset reports the item position', () {
    // Tuned in 33:22 into the item: the engine counts from the stream's zero.
    expect(
      osdPosition(engine: const Duration(seconds: 6), offset: const Duration(minutes: 33, seconds: 22), duration: const Duration(minutes: 44, seconds: 48)),
      const Duration(minutes: 33, seconds: 28),
    );
  });

  test('no offset is the engine position untouched', () {
    expect(
      osdPosition(engine: const Duration(minutes: 2), offset: Duration.zero, duration: const Duration(minutes: 10)),
      const Duration(minutes: 2),
    );
  });

  test('never runs past the end', () {
    expect(
      osdPosition(engine: const Duration(minutes: 5), offset: const Duration(minutes: 9), duration: const Duration(minutes: 10)),
      const Duration(minutes: 10),
    );
  });

  test('a duration the stream has not reported yet clamps nothing away', () {
    expect(
      osdPosition(engine: const Duration(seconds: 3), offset: const Duration(minutes: 1), duration: Duration.zero),
      const Duration(minutes: 1, seconds: 3),
    );
  });
}

// The player adds where the stream began before converting a position into a
// playhead: see _picturePlayhead in main.dart.
