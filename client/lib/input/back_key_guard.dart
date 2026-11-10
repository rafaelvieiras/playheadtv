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
///
/// One exception, and it is a real one: an **open, dismissible popup** owns Back
/// while it is up, and Flutter already closes it through `DismissIntent` /
/// `DismissAction`. This widget sits *inside* `WidgetsApp`'s `Shortcuts`, so
/// consuming the key here would close nothing and the dialog would stay open
/// forever — which is exactly what "Clear the offline cache?" did on Escape
/// (`settings_offline_test`). The route on top decides; the guard only refuses a
/// Back that would otherwise reach the platform.
class BackKeyGuard extends StatelessWidget {
  const BackKeyGuard({super.key, required this.child});

  final Widget child;

  @override
  Widget build(BuildContext context) {
    return Focus(
      canRequestFocus: false,
      skipTraversal: true,
      onKeyEvent: (node, event) {
        if (!backKeyAliases.contains(event.logicalKey)) return KeyEventResult.ignored;
        if (popupOwnsBack(FocusManager.instance.primaryFocus?.context)) return KeyEventResult.ignored;
        return KeyEventResult.handled;
      },
      child: child,
    );
  }
}

/// Whether the route holding [focused] closes itself on Back, so the key must
/// keep travelling to Flutter's own `DismissAction` instead of being eaten.
///
/// A non-dismissible popup (a progress dialog, say) does not qualify: nothing
/// would close it, and the guard's job is to make sure nothing silently falls
/// through to the platform.
bool popupOwnsBack(BuildContext? focused) {
  if (focused == null) return false;
  final route = ModalRoute.of(focused);
  return route is PopupRoute && route.barrierDismissible;
}
