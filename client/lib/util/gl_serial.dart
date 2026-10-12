import 'dart:async';

import '../log/log.dart';

// Serializes every native video-controller lifecycle call (`initialize()`/
// `dispose()`) across the WHOLE app behind a single FIFO chain.
//
// fvp/libmdk creates its GL resources (fbo/tex) on its own internal raster
// thread during `initialize()` and frees them on a separate dispose thread
// during `dispose()`, with no synchronization of its own between the two —
// and those threads are process-global, shared by every
// `VideoPlayerController` instance, not one pair per controller. Two of
// these calls overlapping in real time can therefore reuse the same GL
// object id mid-teardown. This is possible even though the app already
// awaits every `dispose()` before treating a controller as gone (see the
// comment on `MosaicPage._activateFocused`): a channel switch's
// `_loadGeneration` counter lets an *aborted* generation keep running
// concurrently with the new one until its next check — it doesn't cancel
// the old generation's Dart execution, so its own (properly awaited)
// dispose of a stale controller can still be in flight on the native side
// while an unrelated, already-in-flight generation calls `initialize()` for
// its own controller at the same wall-clock time. Reproduced as a hard
// segfault switching channels in quick succession (2026-09-30) — a more
// severe case of the GL-texture instability class already documented in
// AGENTS.md (torn/frozen frames).
//
// Routing every create()/dispose() through this one chain guarantees the
// native GL work never overlaps, regardless of which unrelated Future chain
// (a channel-switch generation, the mosaic preview, ...) enqueued it.
Future<void> _glOpChain = Future.value();

// How long the shared queue waits for one op before letting the next one in
// line start anyway. This is a safety valve, not a normal-path timeout:
// AGENTS.md already documents a pre-existing fvp/libmdk raster-thread stall
// under machine load that can hang a create()/dispose() indefinitely. Without
// a bound, centralizing every GL lifecycle call behind one FIFO (the whole
// point of this file) would turn that into a single wedged op blocking every
// *other*, unrelated controller's create()/dispose() forever too — strictly
// worse than before this queue existed, when a stalled controller only ever
// froze itself. Comfortably above any real initialize()/dispose() latency
// (network stream open, Jellyfin transcode probe) seen in this codebase.
const defaultGlOpQueueTimeout = Duration(seconds: 20);

// Mutable only so a test can shrink it; the app never assigns it.
Duration glOpQueueTimeout = defaultGlOpQueueTimeout;

Future<T> serializeGlOp<T>(Future<T> Function() op, {String label = 'op'}) {
  final queued = Stopwatch()..start();
  final result = _glOpChain.then((_) async {
    final waited = queued.elapsed;
    final ran = Stopwatch()..start();
    // Logging only observes: it never changes when or whether the op runs.
    if (waited >= glWaitWarn) {
      Log.warn('gl', '$label waited ${waited.inMilliseconds}ms behind the queue');
    } else {
      Log.trace('gl', '$label waited ${waited.inMilliseconds}ms');
    }
    try {
      return await op();
    } finally {
      Log.debug('gl', () => '$label ran ${ran.elapsed.inMilliseconds}ms (waited ${waited.inMilliseconds}ms)');
    }
  });
  // The queue itself only ever waits up to glOpQueueTimeout for this op
  // before moving on — but `result` (what the caller awaits) is NOT
  // time-limited: it still resolves/throws whenever the real native call
  // actually finishes, however long that takes. The timeout only stops a
  // stuck op from starving everyone queued behind it; it never cancels the
  // op or misreports its outcome to its own caller.
  final gate = result.then<void>((_) {}, onError: (_) {});
  _glOpChain = gate.timeout(glOpQueueTimeout, onTimeout: () {
    Log.error('gl', '$label did not finish within ${glOpQueueTimeout.inSeconds}s; the queue moves on');
  });
  return result;
}

// A queue wait at or above this is worth a warning: something ahead of the op
// was slow.
const glWaitWarn = Duration(seconds: 2);
