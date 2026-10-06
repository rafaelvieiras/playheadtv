// What the "new version" banner says. Pure: given how the client is installed,
// whether auto-update is on, the running version, the newest release and what
// the user already dismissed, it returns the message to show or null. The
// network, persistence and UI around it live in update_notices.dart and
// widgets/update_overlay.dart.
import 'install_kind.dart';
import 'semver.dart';

class UpdateBanner {
  /// The release this banner is about (what a dismissal is remembered for).
  final SemVer version;
  final String message;
  const UpdateBanner(this.version, this.message);
}

const _releasesPage = "the project's GitHub releases page";

/// The banner for [latest] given the running [running] version, or null when
/// there is nothing to say: no newer release, or the user dismissed [latest]
/// (or a newer one) before. [pending] is the version the updater already
/// swapped in but that is not the running one yet (a long-running session): the
/// banner then only says to restart.
UpdateBanner? bannerFor({
  required InstallKind kind,
  required bool autoUpdate,
  required SemVer running,
  required SemVer latest,
  SemVer? dismissed,
  SemVer? pending,
}) {
  if (!(latest > running)) return null;
  if (dismissed != null && !(latest > dismissed)) return null;

  if (kind.managedByUpdater && pending != null && pending > running && !(latest > pending)) {
    return UpdateBanner(latest, 'Version $pending is installed. Restart Playhead TV to use it.');
  }

  final String how;
  switch (kind) {
    case InstallKind.tarball:
    case InstallKind.appImage:
      if (autoUpdate) {
        how = 'It installs automatically the next time Playhead TV starts.';
      } else if (kind == InstallKind.tarball) {
        how = 'Automatic updates are off: set auto_update to true in config.json, or download the new package from '
            '$_releasesPage and run its install.sh.';
      } else {
        how = 'Automatic updates are off: set auto_update to true in config.json, or download the new AppImage from '
            '$_releasesPage and replace this file.';
      }
    case InstallKind.systemPackage:
      how = 'This copy is managed by your system: update it with your package manager, or download the new package '
          'from $_releasesPage.';
    case InstallKind.sandbox:
      how = 'This copy runs in a sandbox: update it with the tool that installed it (for example "flatpak update").';
    case InstallKind.unknown:
      how = 'Download it from $_releasesPage, or build it from source.';
  }
  return UpdateBanner(latest, 'Version $latest is available. $how');
}
