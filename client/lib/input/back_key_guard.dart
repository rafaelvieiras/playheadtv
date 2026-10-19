import 'package:flutter/widgets.dart';

import 'input_action.dart';

/// App-wide safety net for the Back key, placed above the Navigator.
///
/// Pages react to Back through `KeyBindingsService.resolve` (`InputAction.back`),
/// but many of them listen with a `KeyboardListener`, which never reports the
/// event as handled. On Android that lets the platform apply its default Back
/// (finish the activity), so the app closed without the "Exit Playhead TV?"
/// dialog. Any Back key that bubbles up here unhandled is consumed instead, so
/// the only way out of the app stays that dialog. A page that already handled
/// the key never reaches this widget.
class BackKeyGuard extends StatelessWidget {
  const BackKeyGuard({super.key, required this.child});

  final Widget child;

  @override
  Widget build(BuildContext context) {
    return Focus(
      canRequestFocus: false,
      skipTraversal: true,
      onKeyEvent: (node, event) =>
          backKeyAliases.contains(event.logicalKey) ? KeyEventResult.handled : KeyEventResult.ignored,
      child: child,
    );
  }
}
