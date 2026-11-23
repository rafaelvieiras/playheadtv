// Software dimmer: a black layer of adjustable opacity over the player page.
// This file holds the policy (level bounds and steps) and the shared state;
// the layer itself is widgets/dimmer_overlay.dart and the level dialog
// widgets/dimmer_level_dialog.dart.
import 'package:flutter/foundation.dart';

/// The dim level (percent of black laid over the picture) when the user never
/// chose one.
const defaultDimLevelPercent = 50;

/// Bounds and step of the level: below the minimum the dim is invisible, above
/// the maximum the picture is unreadable.
const minDimLevelPercent = 10;
const maxDimLevelPercent = 80;
const dimLevelStepPercent = 10;

/// How long Select is held on the OSD's dimmer button to open the level dialog
/// (an accelerator: the dialog is also reachable without holding).
const dimmerHoldDuration = Duration(milliseconds: 700);

/// A configured value (config file or storage) that is usable, else
/// [defaultDimLevelPercent]; an in-range value is snapped to the step grid, an
/// out-of-range one is clamped.
int sanitizeDimLevelPercent(Object? value) {
  if (value is! num || !value.isFinite) return defaultDimLevelPercent;
  return stepDimLevel(value.round(), 0);
}

/// [current] moved by [steps] steps (negative = less dim), snapped to the step
/// grid and clamped to the bounds.
int stepDimLevel(int current, int steps) {
  final snapped = ((current + steps * dimLevelStepPercent) / dimLevelStepPercent).round() * dimLevelStepPercent;
  return snapped.clamp(minDimLevelPercent, maxDimLevelPercent);
}

String dimLevelLabel(int percent) => '$percent%';

/// The opacity (0 to 1) of the black layer.
double dimOpacity({required bool active, required int levelPercent}) => active ? levelPercent / 100 : 0.0;

/// The dimmer's state, shared by the player page, its OSD and the Settings
/// page. Static on purpose: there is one screen per process. Both the toggle and
/// the level are persisted by SettingsService, so a restart (an update, a crash,
/// a power cycle) brings the picture back as the viewer left it; a fresh install
/// starts undimmed.
class Dimmer {
  static final ValueNotifier<bool> active = ValueNotifier<bool>(false);
  static final ValueNotifier<int> level = ValueNotifier<int>(defaultDimLevelPercent);

  /// Back to a fresh launch (tests).
  static void reset() {
    active.value = false;
    level.value = defaultDimLevelPercent;
  }
}
