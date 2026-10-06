// The headless auto-update engine (docs/ARCHITECTURE.md "Releases and
// updates"). Pure Dart over interfaces (ReleaseSource, ArchiveExtractor, a
// clock), so every branch is unit-tested against a fake release server; the
// CLI in tool/updater.dart wires the real GitHub source and runs `prepare` from
// the launcher before the app starts.
//
// Rules, in the order they matter:
//  - A failure of any kind (offline, bad answer, bad download, bad checksum,
//    bad package, no disk) leaves the installed version untouched: the new
//    version is only swapped in after it was downloaded, verified against the
//    release's SHA-256 checksums, extracted and found complete. The swap is one
//    atomic rename of the `current` symlink.
//  - A swapped-in version is "pending" until the app confirms it is healthy.
//    A start that finds it still unconfirmed after the window rolls back to the
//    previous version and never offers that version again.
//  - Nothing here throws to the caller: prepare always returns a result.
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart';

import 'install_layout.dart';
import 'release_source.dart';
import 'semver.dart';

/// How long a freshly swapped-in version has to confirm it is healthy.
const defaultHealthyWindow = Duration(seconds: 15);

/// Upper bound of one whole check-and-update; past it the update is dropped
/// and the installed version starts.
const defaultUpdateBudget = Duration(seconds: 120);

/// The only release channel today.
const stableChannel = 'stable';

class UpdateSettings {
  final bool autoUpdate;
  final String channel;

  const UpdateSettings({this.autoUpdate = true, this.channel = stableChannel});

  /// From the decoded config file (`auto_update`: bool, `update_channel`:
  /// string); anything missing or of the wrong type keeps its default. The
  /// environment variable PLAYHEADTV_AUTO_UPDATE=0|false|off disables it
  /// regardless of the file.
  factory UpdateSettings.fromConfig(Map<String, Object?> config, {Map<String, String> env = const {}}) {
    var auto = config['auto_update'] is bool ? config['auto_update'] as bool : true;
    final envValue = env['PLAYHEADTV_AUTO_UPDATE']?.toLowerCase();
    if (envValue == '0' || envValue == 'false' || envValue == 'off') auto = false;
    final channel = config['update_channel'] is String ? config['update_channel'] as String : stableChannel;
    return UpdateSettings(autoUpdate: auto, channel: channel);
  }
}

enum UpdateOutcome {
  /// Auto-update is switched off.
  disabled,

  /// The installed version is the latest (or newer, or the latest was
  /// already rolled back).
  upToDate,

  /// A new version was installed; the next start runs it.
  updated,

  /// The release source could not be reached or answered something unusable.
  checkFailed,

  /// The package or the checksums file could not be downloaded.
  downloadFailed,

  /// The package did not match the published SHA-256.
  checksumMismatch,

  /// Anything else that went wrong while staging or swapping.
  failed,

  /// A previous update is still within its confirmation window: nothing is
  /// done on this start.
  pendingStart,

  /// There is no installation to update (no `current`).
  notInstalled,
}

class UpdateResult {
  final UpdateOutcome outcome;

  /// The version installed (for [UpdateOutcome.updated]).
  final SemVer? version;

  /// Set when this start rolled an unconfirmed update back: the version that
  /// was abandoned.
  final String? rolledBackFrom;

  final String message;

  const UpdateResult(this.outcome, this.message, {this.version, this.rolledBackFrom});

  @override
  String toString() => 'UpdateResult($outcome, $message${rolledBackFrom == null ? '' : ', rolled back from $rolledBackFrom'})';
}

class Updater {
  final InstallLayout layout;
  final ReleaseSource source;
  final ArchiveExtractor extractor;
  final UpdateSettings settings;
  final DateTime Function() now;
  final Duration healthyWindow;
  final Duration budget;
  final void Function(String message) log;

  Updater({
    required this.layout,
    required this.source,
    this.extractor = const TarExtractor(),
    this.settings = const UpdateSettings(),
    DateTime Function()? now,
    this.healthyWindow = defaultHealthyWindow,
    this.budget = defaultUpdateBudget,
    void Function(String message)? log,
  })  : now = now ?? DateTime.now,
        log = log ?? ((_) {});

  /// What the launcher runs before starting the app: resolve an unconfirmed
  /// update (roll back), then, unless disabled, look for a newer release and
  /// install it. Never throws.
  Future<UpdateResult> prepare() async {
    String? rolledBackFrom;
    try {
      final resolution = _resolvePending();
      if (resolution.inWindow) {
        return const UpdateResult(UpdateOutcome.pendingStart, 'previous update still within its confirmation window');
      }
      rolledBackFrom = resolution.rolledBackFrom;
    } on Object catch (e) {
      log('resolving the pending update failed: $e');
    }

    UpdateResult result;
    try {
      result = await _checkAndInstall().timeout(budget);
    } on TimeoutException {
      result = UpdateResult(UpdateOutcome.failed, 'update exceeded its ${budget.inSeconds}s budget');
    } on Object catch (e) {
      result = UpdateResult(UpdateOutcome.failed, 'unexpected error: $e');
    }
    _cleanStaging();
    if (rolledBackFrom != null) {
      return UpdateResult(result.outcome, result.message, version: result.version, rolledBackFrom: rolledBackFrom);
    }
    return result;
  }

  /// Called by the running app once it considers itself healthy: confirms the
  /// pending update of [runningVersion] (no-op for any other version).
  void confirmHealthy(SemVer runningVersion) => confirmPendingHealthy(layout, runningVersion);

  // ---- pending update ----

  _Resolution _resolvePending() {
    final state = UpdateState.read(layout.stateFile);
    final pending = state.pending;
    if (pending == null) return const _Resolution();

    final installed = layout.installedVersion;
    if (installed == null || installed.toString() != pending.version) {
      // The swap never happened (or was already undone): the marker is stale.
      state.copyWith(clearPending: true).write(layout.stateFile);
      return const _Resolution();
    }
    if (pending.confirmed) {
      state.copyWith(clearPending: true).write(layout.stateFile);
      layout.prune({pending.version, pending.previous});
      return const _Resolution();
    }
    if (now().difference(pending.startedAt) < healthyWindow) {
      return const _Resolution(inWindow: true);
    }

    // Unconfirmed past the window: the new version did not come up healthy.
    final failed = {...state.failed, pending.version};
    final previous = SemVer.tryParse(pending.previous);
    if (previous != null && layout.executableOf(previous).existsSync()) {
      layout.pointAt(layout.currentLink, previous);
      log('rolled back ${pending.version} -> ${pending.previous}');
      layout.prune({pending.previous});
    } else {
      log('cannot roll back ${pending.version}: previous version ${pending.previous} is missing');
    }
    state.copyWith(clearPending: true, failed: failed).write(layout.stateFile);
    return _Resolution(rolledBackFrom: pending.version);
  }

  // ---- check, download, verify, swap ----

  Future<UpdateResult> _checkAndInstall() async {
    final installed = layout.installedVersion;
    if (installed == null) {
      return const UpdateResult(UpdateOutcome.notInstalled, 'no current version installed');
    }
    if (!settings.autoUpdate) {
      return const UpdateResult(UpdateOutcome.disabled, 'auto-update is disabled');
    }
    if (settings.channel != stableChannel) {
      log('unsupported update channel "${settings.channel}", using $stableChannel');
    }

    ReleaseInfo? release;
    try {
      release = await source.latestStable();
    } on Object catch (e) {
      return UpdateResult(UpdateOutcome.checkFailed, 'update check failed: $e');
    }
    if (release == null || !(release.version > installed)) {
      return UpdateResult(UpdateOutcome.upToDate, 'installed $installed is the latest');
    }
    final state = UpdateState.read(layout.stateFile);
    if (state.failed.contains(release.version.toString())) {
      return UpdateResult(UpdateOutcome.upToDate, '${release.version} was rolled back earlier, not offered again');
    }
    // The checksums and the package are only ever fetched over HTTPS.
    if (release.packageUrl.scheme != 'https' || release.checksumsUrl.scheme != 'https') {
      return const UpdateResult(UpdateOutcome.checkFailed, 'release assets are not served over HTTPS');
    }

    final stage = Directory('${layout.stagingDir.path}/${release.version}');
    _cleanStaging();
    stage.createSync(recursive: true);
    final archive = File('${stage.path}/package.tar.gz');

    String checksums;
    try {
      checksums = await source.fetchText(release.checksumsUrl);
      await source.download(release.packageUrl, archive);
    } on Object catch (e) {
      return UpdateResult(UpdateOutcome.downloadFailed, 'download failed: $e');
    }

    final expected = expectedChecksum(checksums, clientPackageName(release.version));
    if (expected == null) {
      return UpdateResult(UpdateOutcome.checksumMismatch,
          '${clientPackageName(release.version)} is not listed in the release checksums');
    }
    final actual = (await sha256.bind(archive.openRead()).first).toString();
    if (actual != expected) {
      return UpdateResult(UpdateOutcome.checksumMismatch, 'SHA-256 mismatch for ${release.version}');
    }

    try {
      final unpacked = Directory('${stage.path}/unpacked')..createSync();
      await extractor.extract(archive, unpacked);
      final top = Directory('${unpacked.path}/playheadtv-client-${release.version}-$clientPlatform');
      if (!File('${top.path}/bundle/client').existsSync()) {
        return UpdateResult(UpdateOutcome.failed, 'package of ${release.version} has no bundle/client');
      }
      _swapIn(top, release.version, installed, state);
    } on Object catch (e) {
      return UpdateResult(UpdateOutcome.failed, 'installing ${release.version} failed: $e');
    }
    log('installed ${release.version} (was $installed)');
    return UpdateResult(UpdateOutcome.updated, 'updated $installed -> ${release.version}', version: release.version);
  }

  void _swapIn(Directory extracted, SemVer version, SemVer installed, UpdateState state) {
    layout.versionsDir.createSync(recursive: true);
    final target = layout.versionDir(version);
    if (target.existsSync()) target.deleteSync(recursive: true);
    extracted.renameSync(target.path); // same filesystem: staging is inside the root
    final executable = layout.executableOf(version);
    if (!executable.existsSync()) throw FileSystemException('installed bundle is incomplete', target.path);
    // chmod +x is preserved by tar; make sure anyway.
    Process.runSync('chmod', ['+x', executable.path]);

    // Marker first: if the process dies between the marker and the swap, the
    // stale marker is recognised (installed != pending.version) and dropped.
    state
        .copyWith(
          pending: PendingUpdate(version: version.toString(), previous: installed.toString(), startedAt: now()),
        )
        .write(layout.stateFile);
    layout.pointAt(layout.previousLink, installed);
    layout.pointAt(layout.currentLink, version);
  }

  void _cleanStaging() {
    try {
      if (layout.stagingDir.existsSync()) layout.stagingDir.deleteSync(recursive: true);
    } on FileSystemException {
      // Best effort.
    }
  }
}

class _Resolution {
  final bool inWindow;
  final String? rolledBackFrom;
  const _Resolution({this.inWindow = false, this.rolledBackFrom});
}

/// The SHA-256 (lowercase hex) listed for [fileName] in a `sha256sum`-format
/// checksums file (`<hex>  <name>` or `<hex> *<name>`), or null.
String? expectedChecksum(String checksums, String fileName) {
  for (final line in const LineSplitter().convert(checksums)) {
    final m = RegExp(r'^([0-9a-fA-F]{64})\s+\*?(.+?)\s*$').firstMatch(line);
    if (m != null && m.group(2) == fileName) return m.group(1)!.toLowerCase();
  }
  return null;
}

/// Confirms the pending update of [runningVersion] (no-op when there is none,
/// it is already confirmed, or it belongs to another version). The running app
/// calls this once it has been healthy for [defaultHealthyWindow].
void confirmPendingHealthy(InstallLayout layout, SemVer runningVersion) {
  final state = UpdateState.read(layout.stateFile);
  final pending = state.pending;
  if (pending == null || pending.confirmed || pending.version != runningVersion.toString()) return;
  state.copyWith(pending: pending.confirm()).write(layout.stateFile);
}
