// Which layout family the app draws: the 10-foot television layout it has
// always drawn, or the handheld one a phone needs. See docs/ARCHITECTURE.md
// "Client form factors".
//
// Resolved ONCE, at boot, before the first page builds, and never re-derived
// from a window resize. Two reasons, both load-bearing: a resized desktop
// window must not change the app's personality, and a form factor that flips
// after the player page is already up would rebuild the tree AROUND a live
// video surface (the GL-texture gotcha in AGENTS.md). Rotation is safe by
// construction: it changes the window's width and height but not its shortest
// side, which is the only size input to the decision.
import 'package:flutter/widgets.dart';

/// The two layout families the client draws.
enum ClientFormFactor { tv, phone }

/// The largest shortest side, in dp, that still counts as a phone.
///
/// Only ever consulted on a mobile platform that is not reporting a television.
/// Every phone is at most 430 dp on its shortest side, so 500 leaves a margin
/// without reaching a set-top box: a 1080p box at 320 dpi is 960x540 dp, and
/// calling that a phone is the one mistake this number can make (measured on
/// the lab's TV emulator, which declares a touchscreen and so rules out the
/// feature test). A 720p box at the same density is 640x360 dp and cannot be
/// told from a phone by size at all — the television flag has to answer, which
/// is why it has two sources on the Android side.
const double phoneMaxShortestSide = 500;

/// The form factor a device is, from the two things a window cannot tell:
/// whether the platform says it is a television, and whether it is a mobile
/// operating system at all.
///
/// * a television (Android TV, or any desktop) is always [ClientFormFactor.tv];
/// * a mobile device is a phone only while its shortest side is under
///   [phoneMaxShortestSide] — a tablet is a big screen, and treating it as a
///   10-foot display is both harmless and what the layout already does well;
/// * an unusable size falls back to [ClientFormFactor.tv], i.e. to today.
ClientFormFactor resolveFormFactor({
  required Size size,
  required bool isTelevision,
  required bool isMobilePlatform,
}) {
  if (isTelevision) return ClientFormFactor.tv;
  if (!isMobilePlatform) return ClientFormFactor.tv;
  final side = size.shortestSide;
  if (!side.isFinite || side <= 0) return ClientFormFactor.tv;
  return side < phoneMaxShortestSide ? ClientFormFactor.phone : ClientFormFactor.tv;
}

/// The form factor in force. Static on purpose: there is one form factor per
/// process, like [UiScaleOverride], and it is set before the first page builds
/// so nothing ever sees it change at runtime.
class ClientFormFactorState {
  static final ValueNotifier<ClientFormFactor> notifier =
      ValueNotifier<ClientFormFactor>(ClientFormFactor.tv);
}

class ClientFormFactorScope extends InheritedWidget {
  final ClientFormFactor formFactor;

  const ClientFormFactorScope({super.key, required this.formFactor, required super.child});

  /// The form factor in force, or [ClientFormFactor.tv] outside a
  /// [ClientFormFactorHost] (a bare widget test, a golden).
  static ClientFormFactor of(BuildContext context) =>
      context.dependOnInheritedWidgetOfExactType<ClientFormFactorScope>()?.formFactor ?? ClientFormFactor.tv;

  @override
  bool updateShouldNotify(ClientFormFactorScope oldWidget) => formFactor != oldWidget.formFactor;
}

extension ClientFormFactorContext on BuildContext {
  ClientFormFactor get formFactor => ClientFormFactorScope.of(this);
  bool get isPhone => ClientFormFactorScope.of(this) == ClientFormFactor.phone;
}

/// Provides [ClientFormFactorScope] to everything below it. Sits ABOVE
/// `UiScaleHost` in the MaterialApp builder, because the UI scale depends on
/// the form factor.
class ClientFormFactorHost extends StatelessWidget {
  final Widget child;
  const ClientFormFactorHost({super.key, required this.child});

  @override
  Widget build(BuildContext context) {
    return ValueListenableBuilder<ClientFormFactor>(
      valueListenable: ClientFormFactorState.notifier,
      // `child` is the same instance on every rebuild, so only widgets that ask
      // for the form factor rebuild when it is set.
      builder: (context, factor, child) =>
          ClientFormFactorScope(formFactor: factor, child: child ?? const SizedBox.shrink()),
      child: child,
    );
  }
}
