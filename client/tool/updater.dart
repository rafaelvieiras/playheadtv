// The headless updater, compiled to a standalone executable (`dart compile
// exe`) and shipped inside every release bundle as `bundle/playheadtv-updater`.
// The launcher (or the AppImage's AppRun) runs `playheadtv-updater prepare`
// before every start of the app: it rolls back an unconfirmed update, then
// installs a newer release if there is one. It prints one line and ALWAYS exits
// 0 on any outcome that should still start the app; the launcher ignores its
// status anyway.
//
// Usage:
//   playheadtv-updater prepare [--root <install root>]
//       The tar.gz user install. --root defaults to $PLAYHEADTV_ROOT.
//   playheadtv-updater prepare --appimage <file> --version <running version> [--state-dir <dir>]
//       An AppImage: swaps <file> itself. The update state lives in --state-dir
//       (default $XDG_STATE_HOME/playheadtv).
//   playheadtv-updater keys
//       Prints how many update keys this build pins; exit status 0 only when
//       at least one is (the packaging scripts gate on it).
// Environment: PLAYHEADTV_AUTO_UPDATE=0 disables the update (config.json
// `auto_update` does the same), PLAYHEADTV_UPDATE_REPO overrides the
// "owner/name" the releases come from.
//
// Every update is authenticated with the public keys pinned in
// lib/update/pinned_keys.dart; a build with none pinned refuses to update.
import 'dart:io';

import 'package:client/config/config_file.dart';
import 'package:client/update/install_layout.dart';
import 'package:client/update/install_target.dart';
import 'package:client/update/pinned_keys.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/semver.dart';
import 'package:client/update/updater.dart';

/// The repository releases are published to.
const defaultUpdateRepo = String.fromEnvironment('PLAYHEADTV_UPDATE_REPO', defaultValue: 'rafaelvieiras/playheadtv');

String? _option(List<String> args, String name) {
  final i = args.indexOf(name);
  return i >= 0 && i + 1 < args.length ? args[i + 1] : null;
}

Never _usage(String message) {
  stderr.writeln('playheadtv-updater: $message');
  stderr.writeln('usage: playheadtv-updater prepare [--root <install root>]');
  stderr.writeln('       playheadtv-updater prepare --appimage <file> --version <X.Y.Z> [--state-dir <dir>]');
  exit(2);
}

Future<void> main(List<String> args) async {
  if (args.isNotEmpty && args.first == 'keys') {
    final keys = effectivePinnedKeys();
    stdout.writeln('playheadtv-updater: ${keys.length} update key(s) pinned'
        '${allowUnsignedUpdatesBuild ? ' (development build: unsigned updates allowed when no key is pinned)' : ''}');
    exit(keys.isEmpty ? 1 : 0);
  }
  if (args.isEmpty || args.first != 'prepare') _usage('expected the "prepare" or "keys" command');
  final env = Platform.environment;

  final UpdateTarget target;
  final File? configFile;
  final appImagePath = _option(args, '--appimage');
  if (appImagePath != null) {
    final running = SemVer.tryParse(_option(args, '--version') ?? '');
    if (running == null) _usage('--appimage needs --version <X.Y.Z>');
    final stateDir = _option(args, '--state-dir') != null ? Directory(_option(args, '--state-dir')!) : appImageStateDir(env);
    if (stateDir == null) _usage('no state directory (set XDG_STATE_HOME or HOME, or pass --state-dir)');
    target = AppImageTarget(appImage: File(appImagePath), running: running, stateFile: File('${stateDir.path}/state.json'));
    configFile = configFileLocation(env);
  } else {
    final root = _option(args, '--root') ?? env['PLAYHEADTV_ROOT'] ?? '';
    if (root.isEmpty) _usage('no install root (set PLAYHEADTV_ROOT or pass --root)');
    final layout = InstallLayout(Directory(root));
    target = TarballTarget(layout);
    configFile = layout.configFile;
  }

  final config = readConfigFile(configFile);
  final settings = UpdateSettings.fromConfig(config, env: env);
  final repo = env['PLAYHEADTV_UPDATE_REPO'] ?? (config['update_repo'] is String ? config['update_repo'] as String : defaultUpdateRepo);

  final updater = Updater(
    target: target,
    source: GithubReleaseSource(repo),
    verifier: defaultUpdateVerifier(),
    settings: settings,
    log: (m) => stderr.writeln('playheadtv-updater: $m'),
  );
  final result = await updater.prepare();
  stdout.writeln('playheadtv-updater: ${result.outcome.name}: ${result.message}'
      '${result.rolledBackFrom == null ? '' : ' (rolled back ${result.rolledBackFrom})'}');
  exit(0);
}
