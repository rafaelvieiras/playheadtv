// The two update messages the running app can show, and when:
//
//  - a one-time changelog after an update: the updater recorded the last update
//    (previous and new version, the release body) in its state file; when the
//    running version is that version and the notice was not shown yet, show it;
//  - a banner when a newer release exists, worded for how the client is
//    installed (update_banner.dart). It reuses the updater's release listing
//    (GithubReleaseSource); no new network surface.
//
// Both are strictly best effort, in the offline-safe style of the rest of the
// client: an unreachable GitHub, a repository that answers 404, a missing or
// corrupt state file: all silent, nothing is shown, nothing is retried in a
// loop. The UI (widgets/update_overlay.dart) only listens to [state]; nothing
// here touches a page's state (see the GL-texture gotcha in AGENTS.md).
import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';

import '../config/config_file.dart';
import 'app_update.dart';
import 'install_kind.dart';
import 'install_layout.dart';
import 'release_source.dart';
import 'semver.dart';
import 'update_banner.dart';
import 'updater.dart';

enum UpdateOverlayKind {
  /// "A newer version exists": Select dismisses the version, Back closes it.
  banner,

  /// "Updated to X": the changelog, plain text; Select or Back closes it.
  changelog,
}

/// What the overlay shows right now.
class UpdateOverlay {
  final UpdateOverlayKind kind;
  final SemVer version;
  final String title;

  /// Plain text, never markup (the changelog is the body of a GitHub Release).
  final String body;

  const UpdateOverlay({required this.kind, required this.version, required this.title, required this.body});
}

/// What is remembered between runs (the app's SettingsService in production).
abstract class UpdateNoticeStore {
  /// The newest version whose banner the user dismissed.
  SemVer? get dismissedVersion;
  Future<void> setDismissedVersion(SemVer version);

  /// The version whose changelog was already shown.
  String? get changelogShownFor;
  Future<void> setChangelogShownFor(String version);
}

/// Everything the notices read from the outside, in one injectable place (the
/// integration tests substitute it; production uses [UpdateNoticeEnvironment.system]).
class UpdateNoticeEnvironment {
  /// The running version; a development build ("0.0.0-dev") never shows
  /// anything.
  final String runningVersion;
  final InstallKind kind;

  /// The updater's state file, or null when this install has none.
  final File? stateFile;

  /// The release listing; null disables the banner.
  final ReleaseSource? releaseSource;

  /// Reads the `auto_update` setting (the config file).
  final bool Function() autoUpdate;

  /// How long the banner stays up unattended (it is not dismissed by that: it
  /// comes back on the next start) and how long the changelog does.
  final Duration bannerDuration;
  final Duration changelogDuration;

  /// Wait before the network check, so it never competes with booting.
  final Duration startDelay;

  const UpdateNoticeEnvironment({
    required this.runningVersion,
    required this.kind,
    required this.stateFile,
    required this.releaseSource,
    required this.autoUpdate,
    this.bannerDuration = const Duration(seconds: 25),
    this.changelogDuration = const Duration(seconds: 90),
    this.startDelay = const Duration(seconds: 5),
  });

  /// The real environment of this process.
  factory UpdateNoticeEnvironment.system() {
    final env = Platform.environment;
    final config = readConfigFile(configFileLocation(env));
    final repo = env['PLAYHEADTV_UPDATE_REPO'] ??
        (config['update_repo'] is String ? config['update_repo'] as String : defaultUpdateRepo);
    return UpdateNoticeEnvironment(
      runningVersion: appVersion,
      kind: detectInstallKindHere(),
      stateFile: updateStateFile(env),
      releaseSource: GithubReleaseSource(repo),
      // Re-read each time: the config file may be edited while the app runs.
      autoUpdate: () => UpdateSettings.fromConfig(readConfigFile(configFileLocation(env)), env: env).autoUpdate,
    );
  }
}

/// Decides what to show and when, and holds the answer in [state].
class UpdateNoticeController {
  final UpdateNoticeEnvironment environment;
  final UpdateNoticeStore store;

  /// The overlay on screen, or null. The only thing the UI listens to.
  final ValueNotifier<UpdateOverlay?> state = ValueNotifier<UpdateOverlay?>(null);

  Timer? _timer;
  bool _disposed = false;

  UpdateNoticeController({required this.environment, required this.store});

  /// Works out what to show: the changelog of an update just applied, else the
  /// banner for a newer release. Never throws.
  Future<void> start() async {
    try {
      final running = SemVer.tryParse(environment.runningVersion);
      if (running == null || running.isPrerelease) return; // a development build
      if (_showChangelog(running)) return;
      await Future<void>.delayed(environment.startDelay);
      if (_disposed) return;
      await _checkBanner(running);
    } on Object {
      // Update notices must never disturb the app.
    }
  }

  bool _showChangelog(SemVer running) {
    final file = environment.stateFile;
    if (file == null) return false;
    final last = UpdateState.read(file).lastUpdate;
    if (last == null || last.version != running.toString()) return false;
    if (store.changelogShownFor == last.version) return false;
    // One-time: remembered as shown from the moment it is shown.
    unawaited(store.setChangelogShownFor(last.version));
    _show(
      UpdateOverlay(
        kind: UpdateOverlayKind.changelog,
        version: running,
        title: 'Updated to ${last.version}',
        body: last.notes.isEmpty ? 'No changelog was published for this release.' : last.notes,
      ),
      environment.changelogDuration,
    );
    return true;
  }

  Future<void> _checkBanner(SemVer running) async {
    final source = environment.releaseSource;
    if (source == null) return;
    final ReleaseInfo? latest;
    try {
      latest = await source.latestStable();
    } on Object {
      return; // offline, rate limited, 404: silent
    }
    if (latest == null || _disposed) return;
    final file = environment.stateFile;
    final pending = file == null ? null : SemVer.tryParse(UpdateState.read(file).pending?.version ?? '');
    final banner = bannerFor(
      kind: environment.kind,
      autoUpdate: environment.autoUpdate(),
      running: running,
      latest: latest.version,
      dismissed: store.dismissedVersion,
      pending: pending,
    );
    if (banner == null) return;
    _show(
      UpdateOverlay(
        kind: UpdateOverlayKind.banner,
        version: banner.version,
        title: 'Update available',
        body: banner.message,
      ),
      environment.bannerDuration,
    );
  }

  void _show(UpdateOverlay overlay, Duration visibleFor) {
    if (_disposed) return;
    _timer?.cancel();
    state.value = overlay;
    // Unattended it goes away on its own; it is not remembered as dismissed.
    _timer = Timer(visibleFor, close);
  }

  /// Select: dismiss. A banner is remembered as dismissed for its version (not
  /// shown again until a newer release exists); the changelog was already marked
  /// as shown.
  void select() {
    final overlay = state.value;
    if (overlay == null) return;
    if (overlay.kind == UpdateOverlayKind.banner) unawaited(store.setDismissedVersion(overlay.version));
    close();
  }

  /// Back: close without remembering anything (a banner is shown again on the
  /// next start).
  void close() {
    _timer?.cancel();
    _timer = null;
    if (!_disposed) state.value = null;
  }

  void dispose() {
    _disposed = true;
    _timer?.cancel();
    state.dispose();
  }
}
