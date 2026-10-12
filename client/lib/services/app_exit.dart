import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import '../log/log.dart';

/// Platform side of "close the app for real". Kept tiny so the teardown order
/// in [AppExitController] can be unit-tested with a fake, and so integration
/// tests never kill their own process. Pages never call `exit()` directly.
abstract class AppExit {
  /// Terminates the app with exit code 0 so a launcher regains control. In
  /// production it does not return.
  Future<void> terminate();
}

/// `exit(0)` on the desktop targets (the Dart process ends, which is what a
/// launcher that started it waits for); Android asks the system to finish the
/// activity instead, as the platform expects.
class ProcessAppExit implements AppExit {
  const ProcessAppExit();

  @override
  Future<void> terminate() async {
    if (Platform.isAndroid) {
      await SystemNavigator.pop();
      return;
    }
    exit(0);
  }
}

/// Replaced by tests to keep the real process alive.
@visibleForTesting
AppExit appExitOverride = const ProcessAppExit();

/// Policy: run the teardown steps in order (stop playback and dispose every
/// controller, release the display wake lock, stop background work), then
/// terminate. A step that throws or hangs never keeps the app from closing: the
/// user asked to leave. Runs once; a second request while the first is in
/// flight (a key pressed twice) is ignored.
class AppExitController {
  AppExitController({required this.steps, this.platform, this.stepTimeout = const Duration(seconds: 3)});

  /// Teardown steps, run one after the other in this order.
  final List<Future<void> Function()> steps;
  final Duration stepTimeout;
  final AppExit? platform;
  bool _started = false;

  Future<void> exit() async {
    if (_started) return;
    _started = true;
    for (final step in steps) {
      try {
        await step().timeout(stepTimeout);
      } catch (e) {
        Log.error('exit', 'teardown step failed', error: e);
      }
    }
    await (platform ?? appExitOverride).terminate();
  }
}
