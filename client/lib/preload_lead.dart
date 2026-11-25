// How far ahead of the end of an item the player starts preparing the next one
// (create + initialize its controller), and what it does when that preparation
// is late or fails. Pure Dart, no Flutter: the policy is unit-tested on its own
// (test/preload_lead_test.dart); main.dart only wires it to the player.
import 'dart:math' as math;

/// The configured lead when the user never chose one.
const defaultPreloadLeadSeconds = 20;

/// Bounds of the configured lead (seconds): a lead below the minimum gives a
/// slow source no chance, one above the maximum holds a network connection and
/// a buffer open for too long.
const minPreloadLeadSeconds = 5;
const maxPreloadLeadSeconds = 60;

/// The values Select cycles through in Settings (seconds).
const preloadLeadChoicesSeconds = [5, 10, 15, 20, 30, 45, 60];

/// A configured value (from the config file or storage) that is usable, else
/// [defaultPreloadLeadSeconds]; an in-range value is kept as is, an out-of-range
/// one is clamped.
int sanitizePreloadLeadSeconds(Object? value) {
  if (value is! int || value <= 0) return defaultPreloadLeadSeconds;
  return value.clamp(minPreloadLeadSeconds, maxPreloadLeadSeconds);
}

/// The next Settings choice when Select is pressed: the first choice above
/// [current], wrapping to the smallest.
int nextPreloadLeadChoice(int current) {
  for (final c in preloadLeadChoicesSeconds) {
    if (c > current) return c;
  }
  return preloadLeadChoicesSeconds.first;
}

String preloadLeadLabel(int seconds) => '$seconds seconds';

/// The most the adaptive lead may grow to, whatever was measured.
const maxAdaptiveLead = Duration(seconds: 90);

/// How many recent preload durations drive the adaptive lead. Older samples
/// drop out, which is how the lead decays back once the source is fast again.
const preloadSampleWindow = 4;

/// Safety factor and fixed margin applied on the slowest recent preload.
const preloadSafetyFactor = 2;
const preloadSafetyMargin = Duration(seconds: 2);

/// Measures how long recent preloads took to become ready and derives the
/// effective lead from them: never below the configured lead, never above
/// [maxAdaptiveLead] (or the configured lead, when that is larger).
class PreloadLeadTracker {
  final List<Duration> _samples = [];

  /// Records how long one preload attempt took (create + initialize), whether
  /// it succeeded or failed: a failure that took long is as much a sign of a
  /// slow source as a success.
  void record(Duration took) {
    _samples.add(took);
    if (_samples.length > preloadSampleWindow) _samples.removeAt(0);
  }

  List<Duration> get samples => List.unmodifiable(_samples);

  /// The lead to use now, for a configured lead of [configured].
  Duration effective(Duration configured) {
    if (_samples.isEmpty) return configured;
    final worst = _samples.reduce((a, b) => a > b ? a : b);
    final needed = worst * preloadSafetyFactor + preloadSafetyMargin;
    final ceiling = configured > maxAdaptiveLead ? configured : maxAdaptiveLead;
    if (needed <= configured) return configured;
    return needed < ceiling ? needed : ceiling;
  }
}

/// How long the preload of an item waits before it starts: the item's playing
/// time [remaining] minus the [lead]. An item shorter than the lead preloads
/// right after it starts (zero), it is never skipped.
Duration preloadDelay({required Duration remaining, required Duration lead}) {
  final d = remaining - lead;
  return d.isNegative ? Duration.zero : d;
}

/// Failed preload attempts before the player reports an error (it keeps
/// retrying until [preloadMaxAttempts], see [preloadRetryDelay]).
const preloadAttemptsBeforeError = 4;

/// The last attempt for one item. Past this the preload stops trying it, the
/// channel keeps playing what is on air, and the item is skipped at its turn
/// (the transition's own "cannot start" path).
///
/// It used to retry forever, on the reasoning that a channel must never stay
/// stopped on a late source. Measured against a real failure, "forever" means a
/// new player (and a new MediaCodec) every sixteen seconds for as long as the
/// item is due: with the picture watchdog's own reopening on top, a TV box
/// created 49 players in fifteen minutes, filled its 160 MB Java heap and had
/// the process aborted by the runtime. An item that has failed four times in a
/// row is not late, it is broken.
const preloadMaxAttempts = 8;

/// The pause before retry number [failures] (1 = after the first failure):
/// [base], doubling each time, capped at [preloadRetryCapFactor] times [base].
const preloadRetryCapFactor = 16;

Duration preloadRetryDelay(int failures, Duration base) {
  final exp = math.min(math.max(failures - 1, 0), 30);
  final factor = math.min(1 << exp, preloadRetryCapFactor);
  return base * factor;
}

/// The default [base] of [preloadRetryDelay]: 1s, 2s, 4s, 8s, then 16s.
const defaultPreloadRetryBase = Duration(seconds: 1);

/// The floor for a source that costs the host real work per attempt. Resolving a
/// plugin source runs an extractor and starts a remux for the item, so retrying
/// it every couple of seconds spends the host's CPU on attempts that just failed.
const costlySourceRetryFloor = Duration(seconds: 15);

/// [preloadRetryDelay] for a source that is expensive to ask again (see
/// [costlySourceRetryFloor]).
Duration preloadRetryDelayFor({required int failures, required Duration base, required bool costlySource}) {
  final delay = preloadRetryDelay(failures, base);
  return costlySource && delay < costlySourceRetryFloor ? costlySourceRetryFloor : delay;
}
