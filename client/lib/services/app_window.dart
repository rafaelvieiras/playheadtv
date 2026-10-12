import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import '../log/log.dart';

/// Platform side of the application window. Kept tiny so the policy in
/// [FullScreenController] can be unit-tested with a fake, and so a platform
/// whose window is always full screen (Android, Android TV) simply does
/// nothing. Pages never talk to the window directly.
abstract class AppWindow {
  /// Whether this platform has a window the app can resize at all.
  bool get supportsFullScreen;

  Future<void> setFullScreen(bool on);
}

/// The desktop window, through one method channel (`playheadtv/window`) served
/// by the runner (`linux/runner/my_application.cc`: `gtk_window_fullscreen` /
/// `gtk_window_unfullscreen`). No plugin and no extra Dart dependency: the
/// call is a single boolean. A Windows runner implements the same channel when
/// it exists; every other platform is a silent no-op.
class NativeAppWindow implements AppWindow {
  const NativeAppWindow();

  static const _channel = MethodChannel('playheadtv/window');

  @override
  bool get supportsFullScreen => Platform.isLinux || Platform.isWindows;

  @override
  Future<void> setFullScreen(bool on) async {
    if (!supportsFullScreen) return;
    try {
      await _channel.invokeMethod<void>('setFullScreen', on);
    } on MissingPluginException {
      // A runner without the channel (a build that predates it): stay windowed.
    }
  }
}

/// Replaced by tests to keep the real window out.
@visibleForTesting
AppWindow appWindowOverride = const NativeAppWindow();

/// Policy: put the window in or out of full screen, edge-triggered (a repeated
/// request for the state already applied does nothing) and never throwing, so a
/// window the platform refuses to resize can't stop the app. Driven from
/// startup and the Settings toggle only, never through widget state (see
/// AGENTS.md, GL-texture gotcha).
class FullScreenController {
  FullScreenController([AppWindow? platform]) : _platform = platform;

  /// The one the app uses.
  static final FullScreenController shared = FullScreenController();

  final AppWindow? _platform;
  bool? _applied;
  AppWindow? _appliedTo;

  AppWindow get _window => _platform ?? appWindowOverride;

  /// False where the window is not the app's to resize (Android): the Settings
  /// row is hidden there.
  bool get supported => _window.supportsFullScreen;

  /// The last state applied successfully, null before the first request (or
  /// when the platform window was swapped since, as tests do).
  bool? get applied => identical(_appliedTo, _window) ? _applied : null;

  Future<void> apply(bool on) async {
    final window = _window;
    if (!window.supportsFullScreen || applied == on) return;
    try {
      await window.setFullScreen(on);
      _applied = on;
      _appliedTo = window;
    } catch (e) {
      Log.error('window', 'full screen ${on ? 'on' : 'off'} failed', error: e);
    }
  }
}
