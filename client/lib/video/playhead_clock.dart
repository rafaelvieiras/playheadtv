// What to show as the playing position when the engine's own answer cannot be
// trusted. Pure Dart.

/// A playing position that keeps time for itself.
///
/// Some devices report an unusable position for a perfectly healthy stream: the
/// platform's audio timestamps come back as "spurious", and the player freezes
/// its position a few seconds in while the picture keeps moving and tens of
/// seconds of media sit buffered. Every reader of the position (the OSD clock,
/// the guide, the stall guard) then believes playback has stopped.
///
/// This clock keeps the engine's answer as the anchor and only runs on its own
/// where the engine has stopped advancing: while it is playing, wall time is
/// added since the last sample. It never runs past what
/// is already buffered or past the end of the media, so a source that really
/// died still reports a position that stops moving, and the stall guard keeps
/// working.
class PlayheadClock {
  PlayheadClock({DateTime Function()? now}) : _now = now ?? DateTime.now;

  /// Where the wall clock comes from; injectable so a test owns time.
  final DateTime Function() _now;

  /// The position reported before, engine anchor included.
  Duration _reported = Duration.zero;

  /// The last position the engine itself answered.
  Duration _engine = Duration.zero;

  /// False until the engine has answered once, so the first sample is taken as
  /// it is instead of being treated as a frozen clock.
  bool _anchored = false;

  /// When [_reported] was last computed, null while time is not passing (paused,
  /// closed).
  DateTime? _sampledAt;

  /// The furthest point ever reported as buffered since the engine last
  /// advanced. The engine's own answer wobbles (a sliding window drops the front
  /// of the range), and clamping against the current answer would pull the
  /// reported position backwards while playback is fine.
  Duration? _bufferCeiling;

  /// The position to use from now on.
  ///
  /// [engine] is what the engine answered just now, [bufferedUntil] the end of
  /// what it has buffered (null when unknown) and [duration] the length of the
  /// media (zero when unknown). Only [playing] stops time: an engine that claims
  /// to be buffering while it is playing is not evidence of anything (a growing
  /// playlist reports that for as long as it is followed), and the buffer cap
  /// below already holds the position where the media ends, which is the case
  /// that matters.
  Duration advance({
    required Duration engine,
    required bool playing,
    Duration? bufferedUntil,
    Duration duration = Duration.zero,
  }) {
    final now = _now();

    if (!_anchored || engine > _engine) {
      // The engine advanced (or this is the first sample): it is authoritative,
      // and it is allowed to pull the reported position forward.
      _anchored = true;
      _engine = engine;
      if (engine > _reported) _reported = engine;
      _sampledAt = now;
      _bufferCeiling = bufferedUntil;
    } else if (engine != _engine) {
      // The engine went backwards without being asked to, which its own seeks
      // never do (those restart this clock): keep the time already reported and
      // re-anchor, so the bad answer is not adopted.
      _engine = engine;
      _sampledAt = now;
    } else if (bufferedUntil != null && (_bufferCeiling == null || bufferedUntil > _bufferCeiling!)) {
      _bufferCeiling = bufferedUntil;
    }

    if (!playing) {
      _sampledAt = null;
      return _reported;
    }
    final since = _sampledAt;
    _sampledAt = now;
    if (since != null) {
      final elapsed = now.difference(since);
      if (elapsed > Duration.zero) _reported += elapsed;
    }

    final ceiling = _bufferCeiling;
    if (ceiling != null && _reported > ceiling) _reported = ceiling;
    if (duration > Duration.zero && _reported > duration) _reported = duration;
    return _reported;
  }

  /// Starts over at [position], for a seek or a freshly opened media.
  void restart(Duration position) {
    _reported = position;
    _engine = position;
    _anchored = false;
    _sampledAt = null;
    _bufferCeiling = null;
  }
}
