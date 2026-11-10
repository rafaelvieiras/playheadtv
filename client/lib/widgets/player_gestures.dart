// The player's touch surface: on the picture, a tap shows or hides the OSD, a
// horizontal fling switches channel and a swipe up opens the guide.
//
// It exists so a phone is operable without a remote, and it is deliberately
// thin: every callback here calls the same page method the key path calls, so
// touch is an additional route to an outcome, never a second meaning. It draws
// nothing and takes no focus.
//
// Placement matters and is load-bearing: this sits inside the player's Stack
// ABOVE the video and BELOW the OSD, the spinner, the error banner and the EPG,
// so those keep the taps they already had. See docs/ARCHITECTURE.md "Client form
// factors" for why a full-screen child above the video is safe here (the video
// widgets are never rebuilt or reordered) and docs/PHONE_UI_PLAN.md for the
// gesture set and the ones that were rejected.
import 'package:flutter/material.dart';

/// Below this a drag is not a fling: dragging to scroll something else, or a
/// finger that slipped, must not change the channel.
const double gestureFlingVelocity = 300;

class PlayerGestureLayer extends StatelessWidget {
  /// A tap on the picture: toggle the OSD (no focus mode, no armed state).
  final VoidCallback onTapPicture;

  /// A horizontal fling: `1` is "next channel", `-1` is "previous channel".
  final void Function(int direction) onHorizontalFling;

  /// An upward swipe: open the guide.
  final VoidCallback onSwipeUp;

  /// False while something above owns the screen (the guide, a track picker),
  /// so the layer never acts on a screen the viewer is not looking at.
  final bool enabled;

  const PlayerGestureLayer({
    super.key,
    required this.onTapPicture,
    required this.onHorizontalFling,
    required this.onSwipeUp,
    this.enabled = true,
  });

  @override
  Widget build(BuildContext context) {
    if (!enabled) return const SizedBox.shrink();
    return GestureDetector(
      // Opaque so a tap on the letterbox bars counts as a tap on the picture.
      behavior: HitTestBehavior.opaque,
      onTap: onTapPicture,
      onHorizontalDragEnd: (details) {
        final v = details.primaryVelocity ?? 0;
        if (v.abs() < gestureFlingVelocity) return;
        // The picture follows the finger: swiping left brings the next channel.
        onHorizontalFling(v < 0 ? 1 : -1);
      },
      onVerticalDragEnd: (details) {
        final v = details.primaryVelocity ?? 0;
        if (v > -gestureFlingVelocity) return;
        onSwipeUp();
      },
    );
  }
}
