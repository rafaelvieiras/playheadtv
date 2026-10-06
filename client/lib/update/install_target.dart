// What kind of installation the updater swaps. The update flow (check, verify
// the signature, download, verify the hash, mark pending, roll back an
// unconfirmed one) is the same for every kind; only "what to download" and "how
// to put it in place and take it back" differ, and that is this interface:
//
//  - TarballTarget: the user-install layout of `install.sh` (InstallLayout):
//    extract the package into versions/<ver> and flip the `current` symlink.
//  - AppImageTarget: the AppImage file the app runs from: swap that one file,
//    keeping the previous one beside it.
import 'dart:io';

import 'install_layout.dart';
import 'release_source.dart';
import 'semver.dart';

abstract class UpdateTarget {
  /// Human name used in log lines.
  String get kind;

  File get stateFile;

  /// Download and extraction scratch space; wiped each run. It must be on the
  /// same filesystem as what is swapped (the swap is a rename).
  Directory get stagingDir;

  /// The installed (running) version, or null when there is no installation.
  SemVer? get installedVersion;

  /// The asset of [release] this kind installs, or null when it has none.
  Uri? artifactUrl(ReleaseInfo release);

  /// The name the artifact has in the checksums file.
  String artifactName(SemVer version);

  /// Puts the downloaded, hash-verified [artifact] in place as [version]
  /// (replacing [installed]). [beforeSwap] is called once the new version is
  /// ready and immediately before it becomes the installed one (the pending
  /// marker is written there). Throws on failure, leaving the installed version
  /// untouched.
  Future<void> install({
    required File artifact,
    required Directory stage,
    required SemVer version,
    required SemVer installed,
    required void Function() beforeSwap,
  });

  /// Takes the unconfirmed [bad] version back: restores [previous]. Returns
  /// false when there is nothing to go back to.
  bool rollback({required SemVer previous, required SemVer bad});

  /// Housekeeping after an update was confirmed healthy.
  void afterConfirmed(PendingUpdate pending);
}

/// The tar.gz user install (InstallLayout).
class TarballTarget implements UpdateTarget {
  final InstallLayout layout;
  final ArchiveExtractor extractor;

  const TarballTarget(this.layout, {this.extractor = const TarExtractor()});

  @override
  String get kind => 'package';

  @override
  File get stateFile => layout.stateFile;

  @override
  Directory get stagingDir => layout.stagingDir;

  @override
  SemVer? get installedVersion => layout.installedVersion;

  @override
  Uri? artifactUrl(ReleaseInfo release) => release.packageUrl;

  @override
  String artifactName(SemVer version) => clientPackageName(version);

  @override
  Future<void> install({
    required File artifact,
    required Directory stage,
    required SemVer version,
    required SemVer installed,
    required void Function() beforeSwap,
  }) async {
    final unpacked = Directory('${stage.path}/unpacked')..createSync();
    await extractor.extract(artifact, unpacked);
    final top = Directory('${unpacked.path}/playheadtv-client-$version-$clientPlatform');
    if (!File('${top.path}/bundle/client').existsSync()) {
      throw FileSystemException('package of $version has no bundle/client', top.path);
    }
    layout.versionsDir.createSync(recursive: true);
    final target = layout.versionDir(version);
    if (target.existsSync()) target.deleteSync(recursive: true);
    top.renameSync(target.path); // same filesystem: staging is inside the root
    final executable = layout.executableOf(version);
    if (!executable.existsSync()) throw FileSystemException('installed bundle is incomplete', target.path);
    // chmod +x is preserved by tar; make sure anyway.
    Process.runSync('chmod', ['+x', executable.path]);

    // Marker first: if the process dies between the marker and the swap, the
    // stale marker is recognised (installed != pending.version) and dropped.
    beforeSwap();
    layout.pointAt(layout.previousLink, installed);
    layout.pointAt(layout.currentLink, version);
  }

  @override
  bool rollback({required SemVer previous, required SemVer bad}) {
    if (!layout.executableOf(previous).existsSync()) return false;
    layout.pointAt(layout.currentLink, previous);
    layout.prune({previous.toString()});
    return true;
  }

  @override
  void afterConfirmed(PendingUpdate pending) => layout.prune({pending.version, pending.previous});
}

/// The AppImage file the app runs from. [appImage] is the path of the running
/// file (the runtime exports it as `$APPIMAGE`), [running] its version (the
/// AppImage carries a VERSION file). The update replaces the file in place:
///
///  1. the new file is downloaded and verified in a staging directory NEXT TO
///     the AppImage (same filesystem, so the swap is one atomic rename),
///  2. the current file is hard-linked (copied when that is impossible) to
///     `<name>.previous`, which is what a rollback restores,
///  3. the new file is renamed over the AppImage path: at every instant that
///     path names a complete file, and the running process keeps its own inode.
///
/// [stateFile] lives in a user state directory, not beside the AppImage, which
/// may sit in a folder the user only reads from.
class AppImageTarget implements UpdateTarget {
  final File appImage;
  final SemVer running;
  @override
  final File stateFile;

  const AppImageTarget({required this.appImage, required this.running, required this.stateFile});

  File get previousFile => File('${appImage.path}.previous');

  @override
  String get kind => 'AppImage';

  @override
  Directory get stagingDir => Directory('${appImage.path}.update');

  @override
  SemVer? get installedVersion => appImage.existsSync() ? running : null;

  @override
  Uri? artifactUrl(ReleaseInfo release) => release.appImageUrl;

  @override
  String artifactName(SemVer version) => clientAppImageName(version);

  @override
  Future<void> install({
    required File artifact,
    required Directory stage,
    required SemVer version,
    required SemVer installed,
    required void Function() beforeSwap,
  }) async {
    // A cheap sanity check on top of the signed hash: an AppImage is an ELF
    // executable (the runtime) followed by a squashfs image.
    final head = artifact.openSync()..setPositionSync(0);
    final magic = head.readSync(4);
    head.closeSync();
    if (magic.length < 4 || magic[0] != 0x7f || magic[1] != 0x45 || magic[2] != 0x4c || magic[3] != 0x46) {
      throw FileSystemException('the downloaded file is not an executable AppImage', artifact.path);
    }
    Process.runSync('chmod', ['755', artifact.path]);

    // Keep the current file as the rollback copy.
    final tmpPrevious = File('${previousFile.path}.tmp');
    if (tmpPrevious.existsSync()) tmpPrevious.deleteSync();
    final linked = Process.runSync('ln', [appImage.path, tmpPrevious.path]);
    if (linked.exitCode != 0) appImage.copySync(tmpPrevious.path);
    tmpPrevious.renameSync(previousFile.path);

    beforeSwap();
    artifact.renameSync(appImage.path);
  }

  @override
  bool rollback({required SemVer previous, required SemVer bad}) {
    if (!previousFile.existsSync()) return false;
    previousFile.renameSync(appImage.path);
    return true;
  }

  @override
  void afterConfirmed(PendingUpdate pending) {
    // The previous file stays: it is the rollback copy of the last update.
  }
}

/// Where the update state of an AppImage lives: `$XDG_STATE_HOME/playheadtv/`
/// (default `~/.local/state/playheadtv/`), `state.json` and `update.log`.
/// Shared by the updater and the app (which confirms a healthy start there).
Directory? appImageStateDir(Map<String, String> env) {
  final xdg = env['XDG_STATE_HOME'];
  if (xdg != null && xdg.isNotEmpty) return Directory('$xdg/playheadtv');
  final home = env['HOME'];
  if (home != null && home.isNotEmpty) return Directory('$home/.local/state/playheadtv');
  return null;
}
