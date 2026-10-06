// The headless updater, compiled to a standalone executable (`dart compile
// exe`) and shipped inside every release bundle as `bundle/playheadtv-updater`.
// The launcher runs `playheadtv-updater prepare` before every start of the app:
// it rolls back an unconfirmed update, then installs a newer release if there
// is one. It prints one line and ALWAYS exits 0 on any outcome that should
// still start the app; the launcher ignores its status anyway.
//
// Usage: playheadtv-updater prepare [--root <install root>]
//   --root defaults to $PLAYHEADTV_ROOT.
// Environment: PLAYHEADTV_AUTO_UPDATE=0 disables the update (config.json
// `auto_update` does the same), PLAYHEADTV_UPDATE_REPO overrides the
// "owner/name" the releases come from.
import 'dart:io';

import 'package:client/config/config_file.dart';
import 'package:client/update/install_layout.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/updater.dart';

/// The repository releases are published to.
const defaultUpdateRepo = String.fromEnvironment('PLAYHEADTV_UPDATE_REPO', defaultValue: 'rafaelvieiras/playheadtv');

Future<void> main(List<String> args) async {
  if (args.isEmpty || args.first != 'prepare') {
    stderr.writeln('usage: playheadtv-updater prepare [--root <install root>]');
    exit(2);
  }
  final env = Platform.environment;
  var root = env['PLAYHEADTV_ROOT'] ?? '';
  final i = args.indexOf('--root');
  if (i >= 0 && i + 1 < args.length) root = args[i + 1];
  if (root.isEmpty) {
    stderr.writeln('playheadtv-updater: no install root (set PLAYHEADTV_ROOT or pass --root)');
    exit(2);
  }

  final layout = InstallLayout(Directory(root));
  final config = readConfigFile(layout.configFile);
  final settings = UpdateSettings.fromConfig(config, env: env);
  final repo = env['PLAYHEADTV_UPDATE_REPO'] ?? (config['update_repo'] is String ? config['update_repo'] as String : defaultUpdateRepo);

  final updater = Updater(
    layout: layout,
    source: GithubReleaseSource(repo),
    settings: settings,
    log: (m) => stderr.writeln('playheadtv-updater: $m'),
  );
  final result = await updater.prepare();
  stdout.writeln('playheadtv-updater: ${result.outcome.name}: ${result.message}'
      '${result.rolledBackFrom == null ? '' : ' (rolled back ${result.rolledBackFrom})'}');
  exit(0);
}
