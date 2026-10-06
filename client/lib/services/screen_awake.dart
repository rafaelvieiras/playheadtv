import 'dart:async';
import 'dart:io';

import 'package:dbus/dbus.dart';
import 'package:flutter/widgets.dart';
import 'package:wakelock_plus/wakelock_plus.dart';

/// Platform side of "keep the display awake". Kept tiny so the policy in
/// [ScreenAwakeController] can be unit-tested with a fake.
abstract class ScreenAwake {
  Future<void> enable();
  Future<void> disable();
}

/// `wakelock_plus` on every platform (Windows SetThreadExecutionState, Android
/// window flag, macOS/iOS/web). On Linux it first talks to the freedesktop
/// `org.freedesktop.ScreenSaver` D-Bus service (GNOME, KDE, Xfce, Cinnamon,
/// MATE; X11 and Wayland) and only falls back to wakelock_plus's
/// xdg-desktop-portal path when that service is absent or refuses: the portal
/// call was observed to be accepted yet create no inhibitor on GNOME/X11.
/// Both are no-ops without a session bus.
class WakelockPlusScreenAwake implements ScreenAwake {
  const WakelockPlusScreenAwake();

  static DBusClient? _bus;
  static int? _cookie;
  static bool _viaPortal = false;

  // enable/disable run one at a time and in call order, so a quick
  // enable+disable can't leave the lock taken after the release.
  static Future<void> _queue = Future<void>.value();

  static Future<void> _serial(Future<void> Function() op) {
    final run = _queue.then((_) => op());
    _queue = run.catchError((Object _) {});
    return run;
  }

  @override
  Future<void> enable() => _serial(_enable);

  @override
  Future<void> disable() => _serial(_disable);

  Future<void> _enable() async {
    if (!Platform.isLinux) return WakelockPlus.enable();
    if (_cookie != null || _viaPortal) return;
    try {
      final bus = _bus ??= DBusClient.session();
      final reply = await DBusRemoteObject(bus, name: 'org.freedesktop.ScreenSaver', path: DBusObjectPath('/org/freedesktop/ScreenSaver'))
          .callMethod('org.freedesktop.ScreenSaver', 'Inhibit', [const DBusString('Playhead TV'), const DBusString('Playing a channel')],
              replySignature: DBusSignature('u'));
      _cookie = reply.returnValues.single.asUint32();
    } catch (e) {
      debugPrint('ScreenSaver inhibit unavailable ($e), falling back to the portal');
      _viaPortal = true;
      await WakelockPlus.enable();
    }
  }

  Future<void> _disable() async {
    if (!Platform.isLinux) return WakelockPlus.disable();
    if (_viaPortal) {
      _viaPortal = false;
      return WakelockPlus.disable();
    }
    final cookie = _cookie;
    if (cookie == null) return;
    _cookie = null;
    await DBusRemoteObject(_bus!, name: 'org.freedesktop.ScreenSaver', path: DBusObjectPath('/org/freedesktop/ScreenSaver'))
        .callMethod('org.freedesktop.ScreenSaver', 'UnInhibit', [DBusUint32(cookie)], replySignature: DBusSignature.empty);
  }
}

/// Replaced by tests to keep the real OS out.
@visibleForTesting
ScreenAwake screenAwakeOverride = const WakelockPlusScreenAwake();

/// Policy: the display is held awake only while [playing] (a channel is on
/// screen and playing) AND the app is in the foreground. Edge-triggered, so
/// repeated identical inputs never double-enable. Driven from lifecycle/route
/// hooks only, never through widget state (see AGENTS.md, GL-texture gotcha).
/// The mosaic's decorative previews never hold the lock: only the player
/// page owns a controller.
class ScreenAwakeController {
  ScreenAwakeController([ScreenAwake? platform]) : _platform = platform ?? screenAwakeOverride;

  final ScreenAwake _platform;
  bool _playing = false;
  bool _foreground = true;
  bool _held = false;
  bool _disposed = false;

  bool get held => _held;

  set playing(bool value) {
    _playing = value;
    _apply();
  }

  /// Fed from [AppLifecycleState]: `inactive` (focus lost, window still
  /// visible) keeps the lock; hidden/paused/detached release it.
  void lifecycle(AppLifecycleState state) {
    _foreground = state == AppLifecycleState.resumed || state == AppLifecycleState.inactive;
    _apply();
  }

  void dispose() {
    _playing = false;
    _apply();
    _disposed = true;
  }

  void _apply() {
    if (_disposed) return;
    final want = _playing && _foreground;
    if (want == _held) return;
    _held = want;
    unawaited((want ? _platform.enable() : _platform.disable()).catchError((Object e) {
      debugPrint('screen awake ${want ? 'enable' : 'disable'} failed: $e');
    }));
  }
}
