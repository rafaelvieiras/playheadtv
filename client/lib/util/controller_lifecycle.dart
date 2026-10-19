import 'dart:async';

import '../log/log.dart';
import '../video/video_engine.dart';

// Bookkeeping for every `VideoPlayback` the app creates (the player's
// crossfade pair, preloads, the gap slate, the mosaic preview). A playback is
// tracked from creation until its dispose has really finished, so a test (and
// the debug tooling) can tell one that outlived the page that made it.
final Set<VideoPlayback> _live = <VideoPlayback>{};

/// The playbacks created and not yet disposed. Read-only snapshot; meant for
/// tests and diagnostics, never for playback decisions.
List<VideoPlayback> get liveControllers => List.unmodifiable(_live);

/// Registers a freshly created playback. Returns it for chaining.
VideoPlayback trackController(VideoPlayback playback) {
  _live.add(playback);
  Log.debug('controller', 'created (live=${_live.length})');
  return playback;
}

/// Disposes [playback]. Every dispose of a playback goes through here. The
/// engine serializes its own native teardown (see `VideoPlayback.dispose`), so
/// this must not queue it a second time.
Future<void> disposeController(VideoPlayback playback) async {
  try {
    await playback.dispose();
  } catch (e, st) {
    Log.error('controller', 'dispose failed', error: e, stack: st);
    rethrow;
  } finally {
    _live.remove(playback);
    Log.debug('controller', 'disposed (live=${_live.length})');
  }
}

/// For a playback that may already be playing but can no longer be attached
/// to anything (its page is gone, its channel was left): silences and pauses it
/// first so nothing is audible while the dispose waits its turn in the native
/// queue, then disposes it. Never throws.
Future<void> discardController(VideoPlayback playback) async {
  Future<void> quiet(Future<void> Function() call) async {
    try {
      await call().timeout(const Duration(seconds: 2));
    } catch (_) {}
  }

  await quiet(() => playback.setVolume(0));
  await quiet(() => playback.pause());
  try {
    await disposeController(playback);
  } catch (_) {}
}
