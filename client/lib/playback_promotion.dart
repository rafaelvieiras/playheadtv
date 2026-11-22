// Whether the item the channel is about to fade into can be put on air.
//
// A preloaded controller is parked off screen for a preload lead before it
// airs, and while it is parked nothing reads from its source, so the
// connection it holds can be closed as idle by the peer or by the network
// stack. The engine only says so when the item is asked to play, which is the
// transition itself: promoting that controller leaves a dead player on air and
// nobody left to ask. This judges the answer instead of trusting the
// promotion.
//
// Pure on purpose: the polling loop lives in the player, the decision is
// testable without an engine.

import 'video/video_engine.dart';

/// How long an incoming item gets to show progress or a failure before the
/// fade starts on it anyway. Only ever spent on an item that is not on screen
/// yet, and a healthy one shows progress within a poll or two: the bound is
/// for the engine that says nothing at all, which is no worse than promoting it
/// blind.
const playbackPromotionBound = Duration(milliseconds: 1200);

/// How often the incoming state is read while it is being promoted.
const playbackPromotionPoll = Duration(milliseconds: 50);

/// How many times a unit that failed to start is opened again before the
/// channel resumes it with a hard cut.
const playbackPromotionAttempts = 2;

/// What to do with an incoming controller that was told to play.
enum PromotionVerdict {
  /// Nothing yet, keep waiting.
  wait,

  /// The item is playing: the fade can start.
  promote,

  /// The engine failed on this source: open it again.
  reopen,
}

/// Judges one poll of an incoming controller. [startedAt] is the position the
/// engine reported before it was told to play, [waited] the time since.
///
/// A failure outranks everything: an item that advanced and then errored is
/// still dead. Progress means it is reading its source, so the fade can start
/// without waiting out the bound.
PromotionVerdict judgePromotion({
  required PlaybackState state,
  required Duration startedAt,
  required Duration waited,
  Duration bound = playbackPromotionBound,
}) {
  if (state.hasError) return PromotionVerdict.reopen;
  if (state.position > startedAt) return PromotionVerdict.promote;
  if (waited >= bound) return PromotionVerdict.promote;
  return PromotionVerdict.wait;
}
