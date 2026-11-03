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
      clock.advance(engine: const Duration(seconds: 3), playing: true),
      const Duration(seconds: 3),
    );
  });

  test('follows an engine that keeps advancing', () {
    final clock = fresh();
    clock.advance(engine: const Duration(seconds: 3), playing: true);
    pass(3);
    expect(
      clock.advance(engine: const Duration(seconds: 6), playing: true),
      const Duration(seconds: 6),
    );
    pass(3);
    expect(
      clock.advance(engine: const Duration(seconds: 9), playing: true),
      const Duration(seconds: 9),
    );
  });

  test('keeps time while the engine repeats one frozen position', () {
    final clock = fresh();
    const frozen = Duration(milliseconds: 7700);
    clock.advance(engine: frozen, playing: true);
    pass(3);
    expect(clock.advance(engine: frozen, playing: true),
        frozen + const Duration(seconds: 3));
    pass(3);
    expect(clock.advance(engine: frozen, playing: true),
        frozen + const Duration(seconds: 6));
  });

  test('a frozen clock does not run past what is buffered', () {
    final clock = fresh();
    const frozen = Duration(seconds: 8);
    const buffered = Duration(seconds: 20);
    clock.advance(engine: frozen, playing: true, bufferedUntil: buffered);
    pass(30);
    expect(
      clock.advance(engine: frozen, playing: true, bufferedUntil: buffered),
      buffered,
    );
  });

  test('a buffer range that wobbles never pulls the position backwards', () {
    final clock = fresh();
    const frozen = Duration(seconds: 7);
    clock.advance(engine: frozen, playing: true, bufferedUntil: const Duration(seconds: 70));
    pass(10);
    expect(clock.advance(engine: frozen, playing: true, bufferedUntil: const Duration(seconds: 60)),
        frozen + const Duration(seconds: 10));
    pass(10);
    expect(clock.advance(engine: frozen, playing: true, bufferedUntil: const Duration(seconds: 65)),
        frozen + const Duration(seconds: 20));
  });

  test('a source that died stops the reported position', () {
    final clock = fresh();
    const frozen = Duration(seconds: 8);
    const buffered = Duration(seconds: 20);
    clock.advance(engine: frozen, playing: true, bufferedUntil: buffered);
    pass(12);
    final capped = clock.advance(engine: frozen, playing: true, bufferedUntil: buffered);
    expect(capped, buffered);
    // Nothing more is buffered, so nothing more is reported: this is what lets
    // the stall guard notice.
    pass(12);
    expect(
      clock.advance(engine: frozen, playing: true, bufferedUntil: buffered),
      buffered,
    );
  });

  test('keeps time for a growth playlist that says it is buffering', () {
    // A playlist that keeps growing reports buffering for as long as it is
    // followed; the picture keeps moving, so the position has to keep moving too.
    final clock = fresh();
    const frozen = Duration(milliseconds: 7700);
    clock.advance(engine: frozen, playing: true);
    pass(10);
    expect(clock.advance(engine: frozen, playing: true),
        frozen + const Duration(seconds: 10));
  });

  test('keeps time past a length the stream has already outgrown', () {
    // Measured on an HLS playlist that grows while it is followed: the engine
    // answered a length of 20387ms while holding 88s buffered, and its position
    // was pinned at that length. The length was the window it opened on, not the
    // media, so it must not cap the position.
    final clock = fresh();
    const length = Duration(milliseconds: 20387);
    const buffered = Duration(seconds: 88);
    expect(
      clock.advance(engine: length, playing: true, bufferedUntil: buffered, duration: length),
      length,
    );
    pass(10);
    expect(
      clock.advance(engine: length, playing: true, bufferedUntil: buffered, duration: length),
      length + const Duration(seconds: 10),
    );
  });

  test('a length that can be believed still caps the position', () {
    final clock = fresh();
    const length = Duration(minutes: 41);
    clock.advance(engine: const Duration(seconds: 5), playing: true, bufferedUntil: const Duration(minutes: 2), duration: length);
    pass(3000);
    expect(
      clock.advance(engine: const Duration(seconds: 5),
          playing: true, bufferedUntil: const Duration(minutes: 40), duration: length),
      const Duration(minutes: 40),
    );
  });

  test('a length shorter than what is buffered is read as unknown', () {
    expect(trustedDuration(duration: Duration.zero, bufferedUntil: null), Duration.zero);
    expect(trustedDuration(duration: const Duration(seconds: 20), bufferedUntil: null),
        const Duration(seconds: 20));
    expect(
      trustedDuration(
          duration: const Duration(milliseconds: 20387), bufferedUntil: const Duration(seconds: 88)),
      Duration.zero,
    );
    expect(
      trustedDuration(duration: const Duration(minutes: 41), bufferedUntil: const Duration(seconds: 88)),
      const Duration(minutes: 41),
    );
  });

  test('does not count time that passed while paused', () {
    final clock = fresh();
    clock.advance(engine: const Duration(seconds: 3), playing: true);
    pass(10);
    expect(
      clock.advance(engine: const Duration(seconds: 3), playing: false),
      const Duration(seconds: 3),
    );
    pass(10);
    expect(
      clock.advance(engine: const Duration(seconds: 3), playing: true),
      const Duration(seconds: 3),
    );
    // The call that resumed only anchored: time passes from there again.
    pass(2);
    expect(
      clock.advance(engine: const Duration(seconds: 3), playing: true),
      const Duration(seconds: 5),
    );
  });

  test('ignores an engine answer that jumps backwards on its own', () {
    final clock = fresh();
    const frozen = Duration(milliseconds: 7700);
    clock.advance(engine: frozen, playing: true);
    pass(10);
    expect(clock.advance(engine: frozen, playing: true),
        frozen + const Duration(seconds: 10));
    pass(3);
    expect(clock.advance(engine: frozen, playing: true),
        frozen + const Duration(seconds: 13));
  });

  test('an engine that heals pulls the position forward', () {
    final clock = fresh();
    const frozen = Duration(seconds: 7);
    clock.advance(engine: frozen, playing: true);
    pass(5);
    clock.advance(engine: frozen, playing: true);
    pass(1);
    expect(
      clock.advance(engine: const Duration(seconds: 30), playing: true),
      const Duration(seconds: 30),
    );
  });

  test('restart forgets where it was', () {
    final clock = fresh();
    clock.advance(engine: const Duration(seconds: 40), playing: true);
    clock.restart(const Duration(seconds: 2));
    expect(
      clock.advance(engine: const Duration(seconds: 2), playing: true),
      const Duration(seconds: 2),
    );
  });
}
