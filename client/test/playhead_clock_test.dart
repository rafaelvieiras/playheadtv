import 'package:flutter_test/flutter_test.dart';
import 'package:client/video/playhead_clock.dart';

void main() {
  late DateTime clockTime;
  DateTime now() => clockTime;

  PlayheadClock fresh() {
    clockTime = DateTime.utc(2026, 1, 1, 12);
    return PlayheadClock(now: now);
  }

  void pass(int seconds) => clockTime = clockTime.add(Duration(seconds: seconds));

  test('takes the engine position as it is', () {
    final clock = fresh();
    expect(
      clock.advance(engine: const Duration(seconds: 3), playing: true, buffering: false),
      const Duration(seconds: 3),
    );
  });

  test('follows an engine that keeps advancing', () {
    final clock = fresh();
    clock.advance(engine: const Duration(seconds: 3), playing: true, buffering: false);
    pass(3);
    expect(
      clock.advance(engine: const Duration(seconds: 6), playing: true, buffering: false),
      const Duration(seconds: 6),
    );
    pass(3);
    expect(
      clock.advance(engine: const Duration(seconds: 9), playing: true, buffering: false),
      const Duration(seconds: 9),
    );
  });

  test('keeps time while the engine repeats one frozen position', () {
    final clock = fresh();
    const frozen = Duration(milliseconds: 7700);
    clock.advance(engine: frozen, playing: true, buffering: false);
    pass(3);
    expect(clock.advance(engine: frozen, playing: true, buffering: false),
        frozen + const Duration(seconds: 3));
    pass(3);
    expect(clock.advance(engine: frozen, playing: true, buffering: false),
        frozen + const Duration(seconds: 6));
  });

  test('a frozen clock does not run past what is buffered', () {
    final clock = fresh();
    const frozen = Duration(seconds: 8);
    const buffered = Duration(seconds: 20);
    clock.advance(engine: frozen, playing: true, buffering: false, bufferedUntil: buffered);
    pass(30);
    expect(
      clock.advance(engine: frozen, playing: true, buffering: false, bufferedUntil: buffered),
      buffered,
    );
  });

  test('a buffer range that wobbles never pulls the position backwards', () {
    final clock = fresh();
    const frozen = Duration(seconds: 7);
    clock.advance(engine: frozen, playing: true, buffering: false, bufferedUntil: const Duration(seconds: 70));
    pass(10);
    expect(clock.advance(engine: frozen, playing: true, buffering: false, bufferedUntil: const Duration(seconds: 60)),
        frozen + const Duration(seconds: 10));
    pass(10);
    expect(clock.advance(engine: frozen, playing: true, buffering: false, bufferedUntil: const Duration(seconds: 65)),
        frozen + const Duration(seconds: 20));
  });

  test('a source that died stops the reported position', () {
    final clock = fresh();
    const frozen = Duration(seconds: 8);
    const buffered = Duration(seconds: 20);
    clock.advance(engine: frozen, playing: true, buffering: false, bufferedUntil: buffered);
    pass(12);
    final capped = clock.advance(engine: frozen, playing: true, buffering: false, bufferedUntil: buffered);
    expect(capped, buffered);
    // Nothing more is buffered, so nothing more is reported: this is what lets
    // the stall guard notice.
    pass(12);
    expect(
      clock.advance(engine: frozen, playing: true, buffering: false, bufferedUntil: buffered),
      buffered,
    );
  });

  test('does not count time that passed while paused or buffering', () {
    final clock = fresh();
    clock.advance(engine: const Duration(seconds: 3), playing: true, buffering: false);
    pass(10);
    expect(
      clock.advance(engine: const Duration(seconds: 3), playing: false, buffering: false),
      const Duration(seconds: 3),
    );
    pass(10);
    expect(
      clock.advance(engine: const Duration(seconds: 3), playing: true, buffering: true),
      const Duration(seconds: 3),
    );
    // The first call after a pause only anchors: time starts passing again
    // from there.
    pass(2);
    expect(
      clock.advance(engine: const Duration(seconds: 3), playing: true, buffering: false),
      const Duration(seconds: 3),
    );
    pass(2);
    expect(
      clock.advance(engine: const Duration(seconds: 3), playing: true, buffering: false),
      const Duration(seconds: 5),
    );
  });

  test('ignores an engine answer that jumps backwards on its own', () {
    final clock = fresh();
    const frozen = Duration(milliseconds: 7700);
    clock.advance(engine: frozen, playing: true, buffering: false);
    pass(10);
    expect(clock.advance(engine: frozen, playing: true, buffering: false),
        frozen + const Duration(seconds: 10));
    pass(3);
    expect(clock.advance(engine: frozen, playing: true, buffering: false),
        frozen + const Duration(seconds: 13));
  });

  test('an engine that heals pulls the position forward', () {
    final clock = fresh();
    const frozen = Duration(seconds: 7);
    clock.advance(engine: frozen, playing: true, buffering: false);
    pass(5);
    clock.advance(engine: frozen, playing: true, buffering: false);
    pass(1);
    expect(
      clock.advance(engine: const Duration(seconds: 30), playing: true, buffering: false),
      const Duration(seconds: 30),
    );
  });

  test('restart forgets where it was', () {
    final clock = fresh();
    clock.advance(engine: const Duration(seconds: 40), playing: true, buffering: false);
    clock.restart(const Duration(seconds: 2));
    expect(
      clock.advance(engine: const Duration(seconds: 2), playing: true, buffering: false),
      const Duration(seconds: 2),
    );
  });
}
