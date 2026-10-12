// Where the log file lives: the install's state directory, next to update.log
// and state.json. Pure Dart.
import 'dart:io';

import '../update/install_layout.dart';
import '../update/install_target.dart';

/// The directory for the log, first match wins:
///  1. `PLAYHEADTV_ROOT` (the tar.gz install's launcher sets it): the install
///     root, where update.log and state.json are,
///  2. the AppImage's (and any other run's) state directory,
///     `${XDG_STATE_HOME:-~/.local/state}/playheadtv`,
///  3. `%LOCALAPPDATA%\playheadtv` on Windows.
/// Null when none can be derived (the log then stays in memory only).
Directory? logDirectory(Map<String, String> env) {
  final root = env['PLAYHEADTV_ROOT'];
  if (root != null && root.isNotEmpty) return InstallLayout(Directory(root)).root;
  final state = appImageStateDir(env);
  if (state != null) return state;
  final local = env['LOCALAPPDATA'];
  if (local != null && local.isNotEmpty) return Directory('$local/playheadtv');
  return null;
}
