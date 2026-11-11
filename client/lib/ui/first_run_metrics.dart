// What the first-run screens need from the layout family.
//
// The codebase measures everything against one UI scale (lib/ui/ui_scale.dart),
// which is why these screens already fit a phone without overflowing. Fitting
// is not the same as being built for a finger, though: a Material button sized
// by its own label is a desktop control, and on a phone the primary action
// stretches across the content width and is at least a finger tall. That
// difference is the whole of this object — there is no `if (isPhone)` in the
// pages, and the television keeps the intrinsic widths it has always drawn.
import 'form_factor.dart';

class FirstRunMetrics {
  /// Whether a primary/secondary action takes the content width instead of
  /// hugging its label.
  final bool fullWidthActions;

  /// The smallest an action may be, in logical pixels (a finger, not a design
  /// choice: this is the one value that is NOT written in 1080p pixels and
  /// scaled).
  final double minActionHeight;

  const FirstRunMetrics({required this.fullWidthActions, required this.minActionHeight});

  /// A television or a desktop window: the layout every existing golden and
  /// field report shows. Both values are no-ops, so wrapping a button in the
  /// helper below changes nothing there.
  static const tv = FirstRunMetrics(fullWidthActions: false, minActionHeight: 0);

  /// A phone: full-width actions, a thumb-height floor, and a page margin that
  /// leaves more room for the content than a 10-foot layout does.
  static const phone = FirstRunMetrics(fullWidthActions: true, minActionHeight: 48);

  static FirstRunMetrics of(ClientFormFactor formFactor) =>
      formFactor == ClientFormFactor.phone ? phone : tv;
}
