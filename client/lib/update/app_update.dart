// What the running app does about updates: it only CONFIRMS that it started
// healthy (so the launcher's next `prepare` keeps it). Checking, downloading
// and swapping are the updater CLI's job, before the app starts (see
// docs/ARCHITECTURE.md "Releases and updates" for why).
import 'dart:async';
import 'dart:io';

import 'install_layout.dart';
import 'semver.dart';
import 'updater.dart';

/// This build's version, set by the packaging script with
/// `--dart-define=PLAYHEADTV_CLIENT_VERSION=X.Y.Z`; a development build is
/// "0.0.0-dev" and is never confirmed or updated.
const appVersion = String.fromEnvironment('PLAYHEADTV_CLIENT_VERSION', defaultValue: '0.0.0-dev');

/// When the launcher started this process ([env] `PLAYHEADTV_ROOT` is the
/// install root), confirms the pending update of [appVersion] after
/// [window] of the process staying up. Returns the timer (tests cancel it) or
/// null when not started by the launcher. Never throws.
Timer? scheduleHealthConfirmation({
  Map<String, String>? env,
  Duration window = defaultHealthyWindow,
  String version = appVersion,
}) {
  final root = (env ?? Platform.environment)['PLAYHEADTV_ROOT'];
  final running = SemVer.tryParse(version);
  if (root == null || root.isEmpty || running == null) return null;
  return Timer(window, () {
    try {
      confirmPendingHealthy(InstallLayout(Directory(root)), running);
    } on Object {
      // A failed confirmation only means a later start may roll back; it must
      // never disturb playback.
    }
  });
}
