import 'dart:async';

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

Future<T> serializeGlOp<T>(Future<T> Function() op) {
  final result = _glOpChain.then((_) => op());
  // Swallow the error here so a failed op doesn't wedge the chain for
  // everyone after it — the caller of serializeGlOp still sees it via
  // `result`.
  _glOpChain = result.then((_) {}, onError: (_) {});
  return result;
}
