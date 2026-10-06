// What the running app does about updates: it only CONFIRMS that it started
// healthy (so the launcher's next `prepare` keeps it). Checking, downloading
// and swapping are the updater CLI's job, before the app starts (see
// docs/ARCHITECTURE.md "Releases and updates" for why).
import 'dart:async';
import 'dart:io';

import 'install_layout.dart';
import 'install_target.dart';
import 'semver.dart';
import 'updater.dart';

/// This build's version, set by the packaging script with
/// `--dart-define=PLAYHEADTV_CLIENT_VERSION=X.Y.Z`; a development build is
/// "0.0.0-dev" and is never confirmed or updated.
const appVersion = String.fromEnvironment('PLAYHEADTV_CLIENT_VERSION', defaultValue: '0.0.0-dev');

/// Where the update state of the running install lives: `<root>/state.json`
/// when the launcher started this process (`PLAYHEADTV_ROOT`), the AppImage's
/// state directory when it runs from an AppImage (`APPIMAGE`), else null (a
/// source build or a package manager's copy has no update state).
File? updateStateFile(Map<String, String> env) {
  final root = env['PLAYHEADTV_ROOT'];
  if (root != null && root.isNotEmpty) return InstallLayout(Directory(root)).stateFile;
  final appImage = env['APPIMAGE'];
  if (appImage != null && appImage.isNotEmpty) {
    final dir = appImageStateDir(env);
    if (dir != null) return File('${dir.path}/state.json');
  }
  return null;
}

/// When the launcher (or the AppImage) started this process, confirms the pending update of [appVersion] after
/// [window] of the process staying up. Returns the timer (tests cancel it) or
/// null when not started by the launcher or an AppImage. Never throws.
Timer? scheduleHealthConfirmation({
  Map<String, String>? env,
  Duration window = defaultHealthyWindow,
  String version = appVersion,
}) {
  final stateFile = updateStateFile(env ?? Platform.environment);
  final running = SemVer.tryParse(version);
  if (stateFile == null || running == null) return null;
  return Timer(window, () {
    try {
      confirmPendingHealthyAt(stateFile, running);
    } on Object {
      // A failed confirmation only means a later start may roll back; it must
      // never disturb playback.
    }
  });
}
