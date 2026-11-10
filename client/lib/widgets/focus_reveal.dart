import 'dart:async';

import 'package:flutter/widgets.dart';

/// Keeps the focused row of a scrollable list in view.
///
/// A `ListView` builds only what its viewport holds, and a row that was never
/// built does not exist for focus traversal: the viewport's neighbour can be a
/// row the list has not created, so Up or Down finds nowhere to go and the focus
/// stops moving. That is what happened on Settings after the window shrank with
/// "Interface size" at 200% — the focused row stayed where it was, the rows above
/// it fell out of the built range, and the next Up moved nothing
/// (docs/ROADMAP.md, "the focus that stops moving after a resize").
///
/// Revealing the row whenever it takes focus centres it in the viewport, which
/// keeps a row built on each side, so traversal keeps working. It also shows the
/// viewer which row a remote is on, which is the reason a ten-foot list needs it
/// at all. A resize reveals again, because that is the case with no focus change
/// to react to: the same row is still focused, only its neighbours are gone.
class FocusReveal extends StatefulWidget {
  const FocusReveal({super.key, required this.child});

  final Widget child;

  /// How long the reveal takes. Short enough to read as instant on a remote and
  /// long enough not to look like a jump; it does not run when nothing moves.
  static const Duration duration = Duration(milliseconds: 120);

  @override
  State<FocusReveal> createState() => _FocusRevealState();
}

class _FocusRevealState extends State<FocusReveal> with WidgetsBindingObserver {
  @override
  void initState() {
    super.initState();
    FocusManager.instance.addListener(_onFocusChanged);
    WidgetsBinding.instance.addObserver(this);
  }

  @override
  void dispose() {
    FocusManager.instance.removeListener(_onFocusChanged);
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  void didChangeMetrics() {
    // The window changed size: the focused row is the same, so there is no focus
    // event to react to, and this is exactly when its neighbours disappear from
    // the built range. After the frame, so the viewport already has its new
    // size.
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) reveal();
    });
  }

  void _onFocusChanged() {
    if (mounted) reveal();
  }

  /// Scrolls the nearest scrollable so the focused row is centred, if that row
  /// belongs to this subtree. Returns nothing: a row already centred is left
  /// alone by [Scrollable.ensureVisible].
  @visibleForTesting
  void reveal() {
    final focused = FocusManager.instance.primaryFocus?.context;
    if (focused == null || !_owns(focused)) return;
    // A row the viewport dropped keeps its focus and loses its render object, and
    // `ensureVisible` on it throws instead of fixing anything. This is the state
    // the bug left the page in, so it has to be a no-op here, not a crash.
    final object = focused.findRenderObject();
    if (object is! RenderBox || !object.attached || !object.hasSize) return;
    unawaited(
      Scrollable.ensureVisible(
        focused,
        alignment: 0.5,
        duration: FocusReveal.duration,
        curve: Curves.easeOut,
      ),
    );
  }

  /// Whether [context] sits below this widget. The primary focus is global: on
  /// another route it belongs to that route's list, which reveals itself.
  bool _owns(BuildContext context) {
    var owned = false;
    context.visitAncestorElements((element) {
      if (element == this.context as Element) {
        owned = true;
        return false;
      }
      return true;
    });
    return owned;
  }

  @override
  Widget build(BuildContext context) => widget.child;
}
