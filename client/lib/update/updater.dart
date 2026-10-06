// The headless auto-update engine (docs/ARCHITECTURE.md "Releases and
// updates"). Pure Dart over interfaces (ReleaseSource, ArchiveExtractor, a
// clock), so every branch is unit-tested against a fake release server; the
// CLI in tool/updater.dart wires the real GitHub source and runs `prepare` from
// the launcher before the app starts.
//
// Rules, in the order they matter:
//  - A failure of any kind (offline, bad answer, bad download, missing or
//    invalid signature, bad checksum, bad package, no disk) leaves the
//    installed version untouched: the new version is only swapped in after the
//    release's checksums file was authenticated (Ed25519 signature against the
//    pinned keys), the artifact was downloaded and matched that signed list,
//    and it was found complete. The swap is one atomic rename (the `current`
//    symlink, or the AppImage file).
//  - A swapped-in version is "pending" until the app confirms it is healthy.
//    A start that finds it still unconfirmed after the window rolls back to the
//    previous version and never offers that version again.
//  - Nothing here throws to the caller: prepare always returns a result.
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart';

import 'install_layout.dart';
import 'install_target.dart';
import 'release_source.dart';
import 'semver.dart';
import 'signature.dart';
import 'update_notes.dart';

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

  /// The checksums file could not be authenticated: the release has no
  /// signature, the signature is invalid or made with another key, or this
  /// build pins no key. Nothing was downloaded or installed.
  signatureRejected,

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
  final UpdateTarget target;
  final ReleaseSource source;
  final UpdateVerifier verifier;
  final UpdateSettings settings;
  final DateTime Function() now;
  final Duration healthyWindow;
  final Duration budget;
  final void Function(String message) log;

  Updater({
    required this.target,
    required this.source,
    required this.verifier,
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
  void confirmHealthy(SemVer runningVersion) => confirmPendingHealthyAt(target.stateFile, runningVersion);

  // ---- pending update ----

  _Resolution _resolvePending() {
    final state = UpdateState.read(target.stateFile);
    final pending = state.pending;
    if (pending == null) return const _Resolution();

    final installed = target.installedVersion;
    if (installed == null || installed.toString() != pending.version) {
      // The swap never happened (or was already undone): the marker is stale.
      state.copyWith(clearPending: true).write(target.stateFile);
      return const _Resolution();
    }
    if (pending.confirmed) {
      state.copyWith(clearPending: true).write(target.stateFile);
      target.afterConfirmed(pending);
      return const _Resolution();
    }
    if (now().difference(pending.startedAt) < healthyWindow) {
      return const _Resolution(inWindow: true);
    }

    // Unconfirmed past the window: the new version did not come up healthy.
    final failed = {...state.failed, pending.version};
    final previous = SemVer.tryParse(pending.previous);
    if (previous != null && target.rollback(previous: previous, bad: installed)) {
      log('rolled back ${pending.version} -> ${pending.previous}');
    } else {
      log('cannot roll back ${pending.version}: previous version ${pending.previous} is missing');
    }
    final last = state.lastUpdate;
    state
        .copyWith(clearPending: true, failed: failed, clearLastUpdate: last != null && last.version == pending.version)
        .write(target.stateFile);
    return _Resolution(rolledBackFrom: pending.version);
  }

  // ---- check, download, verify, swap ----

  Future<UpdateResult> _checkAndInstall() async {
    final installed = target.installedVersion;
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
    final version = release.version;
    final notes = sanitizeNotes(release.notes);
    final state = UpdateState.read(target.stateFile);
    if (state.failed.contains(version.toString())) {
      return UpdateResult(UpdateOutcome.upToDate, '$version was rolled back earlier, not offered again');
    }
    final artifactUrl = target.artifactUrl(release);
    if (artifactUrl == null) {
      return UpdateResult(UpdateOutcome.checkFailed, 'release $version has no ${target.kind} asset');
    }
    // The checksums, the signature and the artifact are only ever fetched over
    // HTTPS.
    final signatureUrl = release.signatureUrl;
    if (artifactUrl.scheme != 'https' ||
        release.checksumsUrl.scheme != 'https' ||
        (signatureUrl != null && signatureUrl.scheme != 'https')) {
      return const UpdateResult(UpdateOutcome.checkFailed, 'release assets are not served over HTTPS');
    }

    final stage = Directory('${target.stagingDir.path}/$version');
    _cleanStaging();
    try {
      stage.createSync(recursive: true);
    } on FileSystemException catch (e) {
      return UpdateResult(UpdateOutcome.failed, 'cannot prepare a staging directory: $e');
    }
    final artifact = File('${stage.path}/artifact');

    // 1. Authenticate the checksums file: the signature is checked over the
    //    exact bytes, before the (large) artifact is downloaded.
    List<int> checksums;
    String? signature;
    try {
      checksums = await source.fetchBytes(release.checksumsUrl);
      signature = signatureUrl == null ? null : utf8.decode(await source.fetchBytes(signatureUrl), allowMalformed: true);
    } on Object catch (e) {
      return UpdateResult(UpdateOutcome.downloadFailed, 'download failed: $e');
    }
    final check = await verifier.verify(checksums, signature);
    if (check.accepted && check.message.startsWith('WARNING')) log(check.message);
    if (!check.accepted) {
      log('refusing release $version: ${check.message}');
      return UpdateResult(UpdateOutcome.signatureRejected, 'release $version rejected: ${check.message}');
    }

    // 2. Download the artifact and match it against the signed list.
    try {
      await source.download(artifactUrl, artifact);
    } on Object catch (e) {
      return UpdateResult(UpdateOutcome.downloadFailed, 'download failed: $e');
    }
    final name = target.artifactName(version);
    final expected = expectedChecksum(utf8.decode(checksums, allowMalformed: true), name);
    if (expected == null) {
      return UpdateResult(UpdateOutcome.checksumMismatch, '$name is not listed in the release checksums');
    }
    final actual = (await sha256.bind(artifact.openRead()).first).toString();
    if (actual != expected) {
      return UpdateResult(UpdateOutcome.checksumMismatch, 'SHA-256 mismatch for $version');
    }

    try {
      await target.install(
        artifact: artifact,
        stage: stage,
        version: version,
        installed: installed,
        beforeSwap: () => state
            .copyWith(
              pending: PendingUpdate(version: version.toString(), previous: installed.toString(), startedAt: now()),
              lastUpdate: LastUpdate(
                version: version.toString(),
                previous: installed.toString(),
                notes: notes,
                installedAt: now(),
              ),
            )
            .write(target.stateFile),
      );
    } on Object catch (e) {
      return UpdateResult(UpdateOutcome.failed, 'installing $version failed: $e');
    }
    log('installed $version (was $installed)');
    return UpdateResult(UpdateOutcome.updated, 'updated $installed -> $version', version: version);
  }

  void _cleanStaging() {
    try {
      if (target.stagingDir.existsSync()) target.stagingDir.deleteSync(recursive: true);
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
void confirmPendingHealthy(InstallLayout layout, SemVer runningVersion) =>
    confirmPendingHealthyAt(layout.stateFile, runningVersion);

/// [confirmPendingHealthy] for any install kind: all it needs is the state file.
void confirmPendingHealthyAt(File stateFile, SemVer runningVersion) {
  final state = UpdateState.read(stateFile);
  final pending = state.pending;
  if (pending == null || pending.confirmed || pending.version != runningVersion.toString()) return;
  state.copyWith(pending: pending.confirm()).write(stateFile);
}
