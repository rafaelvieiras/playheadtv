// Detecting a picture that stopped without saying so. Pure Dart.

/// How long a playing position may stay put before the channel counts as
/// stalled. An HLS player fetches a new segment every few seconds, so a
/// position that has not moved for twelve seconds is not going to move again
/// on its own: the source behind it ended (a remux that died, a live edge that
/// closed), and the engine waits for frames that will never arrive.
const Duration playbackStallBound = Duration(seconds: 12);

/// How often the player looks at the position while a channel plays. Small
/// enough to notice a stall soon after [playbackStallBound] passes, large
/// enough to cost nothing.
const Duration playbackStallCheck = Duration(seconds: 3);

/// How many times the player reopens a channel that stalls before it reports
/// the failure instead. Two reopenings cover a source that died once while the
/// first replacement had not warmed up yet; more would turn one broken source
/// into a loop of resolves.
const int playbackStallResyncsBeforeGivingUp = 3;

/// Data buffered ahead above which a stopped position is the engine's clock and
/// not the source, so the picture is left alone instead of being restarted. Measured on a device whose audio timestamps are unreliable
/// (the platform reports them as "spurious", and the position freezes while the
/// picture keeps moving): the channel does not need a new stream, it needs its
/// clock moved.
const Duration playbackStallBufferedAhead = Duration(seconds: 3);

/// What one observation of the playing position says.
enum StallState {
  /// The position moved since the previous observation.
  progressing,

  /// Nothing moved yet, but not for long enough to call it stalled.
  waiting,

  /// Nothing moved for [StallWatcher.bound].
  stalled,
}

/// Watches one playing position and reports when it stops advancing. An engine
/// that is not playing (buffering, paused, between items) is not stalled: those
/// states restart the clock, so a buffer refill never counts against the bound.
class StallWatcher {
  StallWatcher({this.bound = playbackStallBound}) : _progressAt = null;

  /// The silence after which a position counts as stalled.
  final Duration bound;

  Duration? _position;
  DateTime? _progressAt;

  /// One observation of [position] at [now]. [playing] is the engine's own
  /// claim that it is presenting right now.
  StallState observe({required Duration position, required bool playing, required DateTime now}) {
    if (!playing) {
      _position = null;
      _progressAt = null;
      return StallState.waiting;
    }
    if (_position == null || position != _position) {
      _position = position;
      _progressAt = now;
      return StallState.progressing;
    }
    final since = _progressAt ?? now;
    return now.difference(since) >= bound ? StallState.stalled : StallState.waiting;
  }

  /// Forgets the observations, so the next one counts as the first.
  void reset() {
    _position = null;
    _progressAt = null;
  }
}
