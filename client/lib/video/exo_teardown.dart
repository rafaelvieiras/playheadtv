// The Android engine's native teardown, one at a time.
//
// `fvp` serializes create and dispose inside the engine (see `serializeGlOp`).
// This engine has no such queue: `video_player_android` disposes each player on
// its own, so a channel change that drops two slots starts two teardowns at
// once, beside the player already being prepared for the channel that arrives.
// A MediaCodec instance is scarce on a set-top box, and releasing one while
// another is being allocated is how a tune-in ends in "Failed to allocate
// decoder" or in a picture that never comes. Every teardown here goes through
// the FIFO below instead, which is the discipline the other engine already has.
//
// What it costs: a teardown waits for the one before it. Nothing on screen
// waits with it — the app disposes off the playback path.
import 'dart:async';

import 'package:flutter/foundation.dart';

import '../log/log.dart';

/// The most a single teardown may hold the FIFO. A plugin call that never
/// answers must not stop every later one, or a channel left mid-teardown keeps
/// its decoder for the rest of the session.
const exoTeardownTimeout = Duration(seconds: 10);

/// The teardown that runs last. Never rejects: a failed teardown is reported to
/// its caller and the FIFO moves on.
Future<void> _tail = Future<void>.value();

/// Runs [release] after every earlier teardown has finished. The returned
/// future carries [release]'s error, so the caller can report it; the FIFO
/// itself does not stall on it.
Future<void> serializeExoTeardown(Future<void> Function() release, {Duration timeout = exoTeardownTimeout}) {
  final next = _tail.then((_) async {
    try {
      await release().timeout(timeout);
    } on TimeoutException {
      Log.warn('exo', 'a native teardown did not answer in ${timeout.inSeconds}s; moving on');
    }
  });
  _tail = next.catchError((Object _) {});
  return next;
}

/// The FIFO's tail, for a test that has to wait for every teardown to land.
@visibleForTesting
Future<void> get exoTeardownTail => _tail;
