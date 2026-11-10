// The system bars: hidden while a channel plays, back on every other screen.
//
// A phone is the reason this exists. The player draws its own black background
// edge to edge; a status bar and a navigation bar sitting on top of it eat two
// strips of the picture, and on a handheld that is the difference between a
// 16:9 frame and a letterboxed one. The television never needed it: a set-top
// box has no bars to hide.
//
// Same shape as `services/screen_awake.dart`, and for the same reason: a tiny
// platform interface plus an edge-triggered policy that is unit-tested with a
// fake. It must be driven from lifecycle/route hooks, never from `build` and
// never through `setState` on the player page (AGENTS.md, the GL-texture
// gotcha). Desktop and web are no-ops — `SystemChrome` would reach a platform
// channel that has no opinion about system UI.
import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

import '../log/log.dart';

/// Platform side of "the bars are hidden". Kept tiny so the policy in
/// [SystemBarsController] can be unit-tested with a fake.
abstract class SystemBars {
  /// Hide the bars and keep them hidden after a swipe from an edge
  /// (`immersiveSticky`): a swipe reveals them for a moment and they slide
  /// away again, instead of staying up over the picture.
  Future<void> immersive();

  /// The ordinary mode: the bars are drawn over the app's edge-to-edge layout.
  Future<void> edgeToEdge();
}

/// The real thing, through Flutter's `SystemChrome`: Android and iOS only. On
/// every other platform both calls return without touching anything.
class FlutterSystemBars implements SystemBars {
  const FlutterSystemBars();

  static bool get _mobile => !kIsWeb &&
      (defaultTargetPlatform == TargetPlatform.android || defaultTargetPlatform == TargetPlatform.iOS);

  @override
  Future<void> immersive() async {
    if (!_mobile) return;
    await SystemChrome.setEnabledSystemUIMode(SystemUiMode.immersiveSticky);
  }

  @override
  Future<void> edgeToEdge() async {
    if (!_mobile) return;
    await SystemChrome.setEnabledSystemUIMode(SystemUiMode.edgeToEdge);
  }
}

/// Replaced by tests to keep the real OS out.
@visibleForTesting
SystemBars systemBarsOverride = const FlutterSystemBars();

/// Restores the bars whatever hid them (the exit flow: leaving the app with the
/// status bar gone looks like a crash to the launcher that takes over).
/// Idempotent.
Future<void> restoreSystemBars() => systemBarsOverride.edgeToEdge();

/// Policy: the bars are hidden only while [player] is true (a channel page is on
/// screen) AND the app is in the foreground. Edge-triggered, so repeated
/// identical inputs cost nothing — `immersiveSticky` is a platform call, not a
/// widget flag, and calling it on every rebuild would churn the window.
class SystemBarsController {
  SystemBarsController([SystemBars? platform]) : _platform = platform ?? systemBarsOverride;

  final SystemBars _platform;
  bool _player = false;
  bool _foreground = true;
  bool _hidden = false;
  bool _disposed = false;

  /// Whether the bars are hidden right now (what the platform was last told).
  bool get hidden => _hidden;

  set player(bool value) {
    _player = value;
    _apply();
  }

  /// Fed from [AppLifecycleState]: `inactive` (focus lost, window still
  /// visible) keeps the player's bars hidden; hidden/paused/detached give them
  /// back, because the screen the user sees next is not ours.
  void lifecycle(AppLifecycleState state) {
    _foreground = state == AppLifecycleState.resumed || state == AppLifecycleState.inactive;
    _apply();
  }

  void dispose() {
    _player = false;
    _apply();
    _disposed = true;
  }

  void _apply() {
    if (_disposed) return;
    final want = _player && _foreground;
    if (want == _hidden) return;
    _hidden = want;
    unawaited((want ? _platform.immersive() : _platform.edgeToEdge()).catchError((Object e) {
      Log.error('ui', 'system bars ${want ? 'hide' : 'restore'} failed', error: e);
    }));
  }
}
