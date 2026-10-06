// How this copy of the client was installed, which decides what an "update
// available" message can honestly say: the in-app updater can only replace what
// it manages (its own user install, an AppImage file); a copy owned by a package
// manager or a sandbox must be updated the way it was installed.
//
// A pure function over environment markers and the path of the running binary,
// so every kind is unit-tested. Pure Dart (the app and the CLI both use it).
import 'dart:io';

enum InstallKind {
  /// The tar.gz user install laid out by `install.sh` and updated by the
  /// headless updater (the launcher exports PLAYHEADTV_ROOT).
  tarball,

  /// An AppImage; the updater swaps the file (the runtime exports APPIMAGE).
  appImage,

  /// A system package or another path owned by the system (`/usr`, `/opt`):
  /// updated with the package manager, never by the app.
  systemPackage,

  /// A sandbox such as Flatpak (FLATPAK_ID) or Snap (SNAP): updated through the
  /// sandbox's own tooling.
  sandbox,

  /// A source build, a manual copy, anything else: nothing is assumed.
  unknown,
}

/// True for the kinds the in-app updater replaces itself.
extension InstallKindManaged on InstallKind {
  bool get managedByUpdater => this == InstallKind.tarball || this == InstallKind.appImage;
}

String _env(Map<String, String> env, String key) => env[key] ?? '';

bool _under(String path, String dir) {
  final d = dir.endsWith('/') ? dir : '$dir/';
  return path.startsWith(d);
}

/// Classifies the install from [env] and [executablePath] (the resolved path of
/// the running binary), checking the most specific marker first: a sandbox, then
/// an AppImage, then the launcher's install root, then system paths.
InstallKind detectInstallKind({required Map<String, String> env, required String executablePath}) {
  if (_env(env, 'FLATPAK_ID').isNotEmpty || _env(env, 'SNAP').isNotEmpty) return InstallKind.sandbox;
  if (_env(env, 'APPIMAGE').isNotEmpty) return InstallKind.appImage;
  final root = _env(env, 'PLAYHEADTV_ROOT');
  if (root.isNotEmpty && _under(executablePath, root)) return InstallKind.tarball;
  if (_under(executablePath, '/usr') || _under(executablePath, '/opt') || _under(executablePath, '/snap')) {
    return InstallKind.systemPackage;
  }
  return InstallKind.unknown;
}

/// [detectInstallKind] for the running process. The install root is resolved
/// through symlinks first, because the running binary's path already is.
InstallKind detectInstallKindHere() {
  final env = Map<String, String>.from(Platform.environment);
  final root = env['PLAYHEADTV_ROOT'];
  if (root != null && root.isNotEmpty) {
    try {
      env['PLAYHEADTV_ROOT'] = Directory(root).resolveSymbolicLinksSync();
    } on FileSystemException {
      // Keep the path as given.
    }
  }
  return detectInstallKind(env: env, executablePath: Platform.resolvedExecutable);
}
