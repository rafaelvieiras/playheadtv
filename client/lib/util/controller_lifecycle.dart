import 'dart:async';

import 'package:video_player/video_player.dart';

import '../log/log.dart';
import 'gl_serial.dart';

// Bookkeeping for every `VideoPlayerController` the app creates (the player's
// crossfade pair, preloads, the gap slate, the mosaic preview). A controller is
// tracked from creation until its dispose has really finished, so a test (and
// the debug tooling) can tell a controller that outlived the page that made it.
final Set<VideoPlayerController> _live = <VideoPlayerController>{};

/// The controllers created and not yet disposed. Read-only snapshot; meant for
/// tests and diagnostics, never for playback decisions.
List<VideoPlayerController> get liveControllers => List.unmodifiable(_live);

/// Registers a freshly created controller. Returns it for chaining.
VideoPlayerController trackController(VideoPlayerController controller) {
  _live.add(controller);
  Log.debug('controller', 'created (live=${_live.length})');
  return controller;
}

/// Disposes [controller] through the shared GL queue (see [serializeGlOp]).
/// Every dispose of a controller goes through here.
Future<void> disposeController(VideoPlayerController controller) {
  return serializeGlOp(() async {
    try {
      await controller.dispose();
    } catch (e, st) {
      Log.error('controller', 'dispose failed', error: e, stack: st);
      rethrow;
    } finally {
      _live.remove(controller);
      Log.debug('controller', 'disposed (live=${_live.length})');
    }
  }, label: 'dispose');
}

/// For a controller that may already be playing but can no longer be attached
/// to anything (its page is gone, its channel was left): silences and pauses it
/// first so nothing is audible while the dispose waits its turn in the GL
/// queue, then disposes it. Never throws.
Future<void> discardController(VideoPlayerController controller) async {
  Future<void> quiet(Future<void> Function() call) async {
    try {
      await call().timeout(const Duration(seconds: 2));
    } catch (_) {}
  }

  await quiet(() => controller.setVolume(0));
  await quiet(() => controller.pause());
  try {
    await disposeController(controller);
  } catch (_) {}
}
