// The always-on clock: the same local time the OSD shows in its top-right
// corner, drawn by itself so it stays on screen with the OSD closed.
// This file holds the policy (bounds, choice lists, labels) and the shared
// state; the widget is widgets/clock_overlay.dart.
//
// The three choices are the user's, persisted by SettingsService and read once
// per app start by the player: whether the clock is always on, how opaque it
// is, and how big. They are deliberately not a full RGB/position editor — the
// corner is fixed (top-right, where the OSD puts its own clock) and the colors
// are the player's white/grey.
import 'package:flutter/foundation.dart';

/// Whether the clock is always on when the user never chose: off, so nothing
/// is drawn over the picture unasked.
const defaultClockAlwaysVisible = false;

/// Defaults of the two looks, when the user never chose.
const defaultClockOpacityPct = 80;
const defaultClockScale = 1.0;

/// The opacity choices, in percent: below 20 the time is unreadable over bright
/// picture, above 100 impossible.
const clockOpacityChoices = [20, 40, 60, 80, 100];

/// The size choices, as a factor of the clock's normal font size (the one the
/// OSD uses): small enough not to intrude, big enough to read from a couch.
const clockScaleChoices = [0.6, 0.8, 1.0, 1.25, 1.5, 2.0];

/// [value] as an opacity percent, snapped to the nearest choice; the default
/// when it is not a usable number.
int sanitizeClockOpacityPct(Object? value) => _nearest(clockOpacityChoices, value, defaultClockOpacityPct).toInt();

/// [value] as a scale factor, snapped to the nearest choice; the default when
/// it is not a usable number.
double sanitizeClockScale(Object? value) =>
    _nearest(clockScaleChoices, value, defaultClockScale).toDouble();

num _nearest(List<num> choices, Object? value, num fallback) {
  if (value is! num || !value.isFinite) return fallback;
  var best = choices.first;
  for (final c in choices) {
    if ((c - value).abs() < (best - value).abs()) best = c;
  }
  return best;
}

/// The opacity that follows [current] when Select steps it forward, wrapping
/// around: the row in Settings has no left/right, so it cycles.
int nextClockOpacityChoice(int current) => _next(clockOpacityChoices, current).toInt();

/// The scale that follows [current] when Select steps it forward, wrapping.
double nextClockScaleChoice(double current) => _next(clockScaleChoices, current).toDouble();

num _next(List<num> choices, num current) {
  final i = choices.indexWhere((c) => c == current);
  return choices[(i + 1) % choices.length];
}

String clockOpacityLabel(int pct) => '$pct%';

/// "100%" for 1.0; "125%" for 1.25.
String clockScaleLabel(double scale) => '${(scale * 100).round()}%';

/// The clock's state, shared by the player page (which draws it) and the
/// Settings page (which edits it). Static on purpose, like `Dimmer`: there is
/// one player per process. The player seeds these from SettingsService at boot
/// and Settings updates them as it saves, so a change is visible immediately
/// the next time the player is on screen.
class ClockOverlaySettings {
  static final ValueNotifier<bool> alwaysVisible = ValueNotifier<bool>(defaultClockAlwaysVisible);
  static final ValueNotifier<int> opacityPct = ValueNotifier<int>(defaultClockOpacityPct);
  static final ValueNotifier<double> scale = ValueNotifier<double>(defaultClockScale);

  /// Back to a fresh launch (tests).
  static void reset() {
    alwaysVisible.value = defaultClockAlwaysVisible;
    opacityPct.value = defaultClockOpacityPct;
    scale.value = defaultClockScale;
  }
}
