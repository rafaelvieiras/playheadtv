// One UI scale for the whole app: text, icons, paddings and focus rings are
// written as "pixels at 1080p" and multiplied by this factor, so the same
// screen reads the same on a small window and on a 4K television.
//
// The factor comes from the shortest side of the window relative to a 1080p
// reference (clamped), or from a fixed user choice in Settings for displays
// whose reported size is misleading. It is exposed once, above the Navigator
// (`UiScaleHost` in main.dart's MaterialApp builder), as an InheritedWidget:
// only widgets that ask for it (`context.ui(...)`) rebuild when it changes,
// never a page that merely sits below, so the player's video surfaces are not
// touched (see the GL-texture gotcha in AGENTS.md).
import 'dart:async';
import 'dart:math' as math;

import 'package:flutter/material.dart';

import '../services/settings_service.dart';
import 'form_factor.dart';

/// The shortest side, in logical pixels, at which the factor is exactly 1.
const double uiScaleReferenceSide = 1080;

/// The same reference for a phone: a 420 dp short side is about the largest
/// handheld, so 360-430 dp lands between 0.86 and 1.02 — i.e. stock Material
/// sizes, which is what a finger-sized layout wants. Using 1080 here would put
/// every phone on the 0.6 floor and shrink body text to ~9.6 sp.
const double phoneUiScaleReferenceSide = 420;

/// The phone's automatic range. The floor keeps text legible and controls
/// near a touch target; the ceiling keeps a large phone or a foldable from
/// ballooning a layout that is already finger-sized.
const double phoneUiScaleMin = 0.85;
const double phoneUiScaleMax = 1.3;

/// Clamp of the automatic factor: below the minimum text would be illegible,
/// above the maximum a tiny window on a huge canvas would balloon.
const double uiScaleMin = 0.6;
const double uiScaleMax = 3.0;

/// Fixed multipliers offered in Settings besides "Automatic" (null).
///
/// They multiply the automatic factor instead of replacing it, which is what
/// makes one choice portable: a viewer who wants larger text on a television
/// gets the same relationship on a desktop window or a phone, instead of an
/// absolute factor that is right on the display it was chosen on and wrong
/// everywhere else (a 1.5 chosen on a 4K set drew everything at 150% of a
/// 1080p layout on a 1080p desktop, where the automatic factor is 1.0).
const List<double> uiScaleSteps = [0.85, 1.0, 1.15, 1.3];

/// The automatic factor for a window of [size] on [formFactor].
///
/// The 1080p branch is the one every television, desktop window and golden has
/// always used; the phone branch only differs in its reference side and range.
double autoUiScale(Size size, {ClientFormFactor formFactor = ClientFormFactor.tv}) {
  final side = size.shortestSide;
  if (!side.isFinite || side <= 0) return 1.0;
  if (formFactor == ClientFormFactor.phone) {
    return (side / phoneUiScaleReferenceSide).clamp(phoneUiScaleMin, phoneUiScaleMax).toDouble();
  }
  return (side / uiScaleReferenceSide).clamp(uiScaleMin, uiScaleMax).toDouble();
}

/// What the widest fixed layout (the OSD) needs, in pixels at 1080p: its bottom
/// panel with every row plus the clock above it needs this much height, and its
/// controls row plus a channel card this much width. A fixed choice is capped so
/// that the layout still fits the window; the automatic factor needs no cap
/// (shortest side over 1080 always fits).
const double uiScaleFitHeight = 440;
const double uiScaleFitWidth = 640;

/// What the phone player's compact panel needs, in the same design units. A
/// phone panel drops the dead transport row and the channel card strip, so its
/// budget is a fraction of the television one — the 440x640 budget above is
/// what makes "Interface size" a no-op (or worse, a shrink) on a phone.
const double uiScaleFitHeightPhone = 200;
const double uiScaleFitWidthPhone = 360;

/// The largest factor at which the OSD still fits a window of [size], never
/// below the automatic minimum of [formFactor].
double maxFitUiScale(Size size, {ClientFormFactor formFactor = ClientFormFactor.tv}) {
  final phone = formFactor == ClientFormFactor.phone;
  final min = phone ? phoneUiScaleMin : uiScaleMin;
  final max = phone ? phoneUiScaleMax : uiScaleMax;
  final fitHeight = phone ? uiScaleFitHeightPhone : uiScaleFitHeight;
  final fitWidth = phone ? uiScaleFitWidthPhone : uiScaleFitWidth;
  if (!size.width.isFinite || !size.height.isFinite || size.width <= 0 || size.height <= 0) {
    return max;
  }
  final fit = math.min(size.height / fitHeight, size.width / fitWidth);
  return fit.clamp(min, max).toDouble();
}

/// The factor in force: the automatic one for [size] on [formFactor], scaled by
/// the user's fixed [multiplier] when it is a positive number, then capped by
/// what fits [size] (`maxFitUiScale`).
///
/// The multiplier is relative on purpose: 1.15 means "a little larger than this
/// screen asks for", so the same choice reads the same on a television and on a
/// desktop window.
double resolveUiScale(
  Size size, {
  double? override,
  ClientFormFactor formFactor = ClientFormFactor.tv,
}) {
  final base = autoUiScale(size, formFactor: formFactor);
  if (override == null || !override.isFinite || override <= 0) return base;
  final min = formFactor == ClientFormFactor.phone ? phoneUiScaleMin : uiScaleMin;
  final max = formFactor == ClientFormFactor.phone ? phoneUiScaleMax : uiScaleMax;
  final scaled = (base * override).clamp(min, max).toDouble();
  return math.min(scaled, maxFitUiScale(size, formFactor: formFactor));
}

/// Label of a Settings choice: the automatic factor is the reference.
String uiScaleLabel(double? choice) => choice == null ? 'Automatic' : '${(choice * 100).round()}%';

/// The next Settings choice when Select is pressed: Automatic, then each fixed
/// step, then back to Automatic.
double? nextUiScaleChoice(double? current) {
  if (current == null) return uiScaleSteps.first;
  final i = uiScaleSteps.indexOf(current);
  if (i < 0 || i == uiScaleSteps.length - 1) return null;
  return uiScaleSteps[i + 1];
}

/// The user's fixed multiplier (null = Automatic), shared by the host and the
/// Settings row. Static on purpose: there is one window per process.
class UiScaleOverride {
  static final ValueNotifier<double?> notifier = ValueNotifier<double?>(null);
}

class UiScale extends InheritedWidget {
  final double factor;

  const UiScale({super.key, required this.factor, required super.child});

  /// The factor in force, or 1 outside a [UiScaleHost] (a bare widget test).
  static double of(BuildContext context) => context.dependOnInheritedWidgetOfExactType<UiScale>()?.factor ?? 1.0;

  @override
  bool updateShouldNotify(UiScale oldWidget) => factor != oldWidget.factor;
}

extension UiScaleContext on BuildContext {
  /// [px] (pixels at 1080p) scaled to this screen.
  double ui(double px) => px * UiScale.of(this);

  /// [px] scaled to this screen, with a floor at Material's minimum touch
  /// target. A television measures nothing, so a 28 px icon keeping its size is
  /// fine there; a finger needs 48 dp, and no design unit may go below it. A
  /// caller that wants no floor on a television passes `min: 0`.
  double uiTouch(double px, {double min = kMinInteractiveDimension}) => math.max(ui(px), min);
}

/// Provides [UiScale] to everything below it. Sits in the MaterialApp builder,
/// above the Navigator and the update overlay.
class UiScaleHost extends StatefulWidget {
  final Widget child;
  const UiScaleHost({super.key, required this.child});

  @override
  State<UiScaleHost> createState() => _UiScaleHostState();
}

class _UiScaleHostState extends State<UiScaleHost> {
  @override
  void initState() {
    super.initState();
    UiScaleOverride.notifier.addListener(_changed);
    unawaited(_loadOverride());
  }

  // A saved choice that cannot be read leaves the automatic scale.
  Future<void> _loadOverride() async {
    try {
      final settings = await SettingsService.create();
      UiScaleOverride.notifier.value = settings.uiScaleOverride;
    } catch (_) {}
  }

  @override
  void dispose() {
    UiScaleOverride.notifier.removeListener(_changed);
    super.dispose();
  }

  void _changed() {
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final factor = resolveUiScale(
      MediaQuery.sizeOf(context),
      override: UiScaleOverride.notifier.value,
      formFactor: ClientFormFactorScope.of(context),
    );
    // `widget.child` is the same instance on every rebuild, so only the
    // widgets that depend on UiScale rebuild.
    return UiScale(factor: factor, child: widget.child);
  }
}

/// Scales the stock Material widgets below it (text through the text scaler,
/// icons through the icon theme) for pages and dialogs built from them.
class UiScaled extends StatelessWidget {
  final Widget child;
  const UiScaled({super.key, required this.child});

  @override
  Widget build(BuildContext context) {
    final factor = UiScale.of(context);
    return MediaQuery(
      data: MediaQuery.of(context).copyWith(textScaler: TextScaler.linear(factor)),
      child: IconTheme.merge(
        data: IconThemeData(size: 24 * factor),
        child: child,
      ),
    );
  }
}
