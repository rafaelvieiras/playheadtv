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

/// The shortest side, in logical pixels, at which the factor is exactly 1.
const double uiScaleReferenceSide = 1080;

/// Clamp of the automatic factor: below the minimum text would be illegible,
/// above the maximum a tiny window on a huge canvas would balloon.
const double uiScaleMin = 0.6;
const double uiScaleMax = 3.0;

/// Fixed choices offered in Settings besides "Automatic" (null).
const List<double> uiScaleSteps = [0.75, 1.0, 1.25, 1.5, 2.0];

/// The automatic factor for a window of [size].
double autoUiScale(Size size) {
  final side = size.shortestSide;
  if (!side.isFinite || side <= 0) return 1.0;
  return (side / uiScaleReferenceSide).clamp(uiScaleMin, uiScaleMax).toDouble();
}

/// What the widest fixed layout (the OSD) needs, in pixels at 1080p: its bottom
/// panel with every row plus the clock above it needs this much height, and its
/// controls row plus a channel card this much width. A fixed choice is capped so
/// that the layout still fits the window; the automatic factor needs no cap
/// (shortest side over 1080 always fits).
const double uiScaleFitHeight = 440;
const double uiScaleFitWidth = 640;

/// The largest factor at which the OSD still fits a window of [size], never
/// below [uiScaleMin].
double maxFitUiScale(Size size) {
  if (!size.width.isFinite || !size.height.isFinite || size.width <= 0 || size.height <= 0) {
    return uiScaleMax;
  }
  final fit = math.min(size.height / uiScaleFitHeight, size.width / uiScaleFitWidth);
  return fit.clamp(uiScaleMin, uiScaleMax).toDouble();
}

/// The factor in force: the user's fixed [override] when it is a positive
/// number, capped by what fits [size] (`maxFitUiScale`), otherwise the
/// automatic one for [size].
double resolveUiScale(Size size, {double? override}) {
  if (override != null && override.isFinite && override > 0) {
    return math.min(override, maxFitUiScale(size));
  }
  return autoUiScale(size);
}

/// Label of a Settings choice.
String uiScaleLabel(double? choice) => choice == null ? 'Automatic' : '${(choice * 100).round()}%';

/// The next Settings choice when Select is pressed: Automatic, then each fixed
/// step, then back to Automatic.
double? nextUiScaleChoice(double? current) {
  if (current == null) return uiScaleSteps.first;
  final i = uiScaleSteps.indexOf(current);
  if (i < 0 || i == uiScaleSteps.length - 1) return null;
  return uiScaleSteps[i + 1];
}

/// The user's fixed choice (null = Automatic), shared by the host and the
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
    final factor = resolveUiScale(MediaQuery.sizeOf(context), override: UiScaleOverride.notifier.value);
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
