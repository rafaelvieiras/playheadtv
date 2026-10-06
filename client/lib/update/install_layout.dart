// The on-disk layout of an installed client and its update state. Everything
// lives under ONE user-writable root directory, so nothing needs privileges:
//
//   <root>/playheadtv            launcher script (never swapped by an update)
//   <root>/config.json           settings (backend URL, device token, ...)
//   <root>/state.json            update state (pending marker, failed versions)
//   <root>/current  -> versions/<ver>    the version the launcher starts
//   <root>/previous -> versions/<ver>    the version before the last update
//   <root>/versions/<ver>/       one extracted release package
//   <root>/staging/              download and extraction scratch space
//
// `current` is a symlink and is replaced with rename(2), which is atomic: at
// every instant it names a complete version.
import 'dart:convert';
import 'dart:io';

import 'semver.dart';

class InstallLayout {
  final Directory root;
  const InstallLayout(this.root);

  String get _p => root.path;

  Link get currentLink => Link('$_p/current');
  Link get previousLink => Link('$_p/previous');
  Directory get versionsDir => Directory('$_p/versions');
  Directory get stagingDir => Directory('$_p/staging');
  File get stateFile => File('$_p/state.json');
  File get configFile => File('$_p/config.json');

  Directory versionDir(SemVer v) => Directory('$_p/versions/$v');

  /// The executable of version [v]: the Flutter bundle's binary.
  File executableOf(SemVer v) => File('$_p/versions/$v/bundle/client');

  /// The version `current` points at, or null when there is no (valid) link.
  SemVer? get installedVersion => _linkedVersion(currentLink);

  /// The version `previous` points at, or null.
  SemVer? get previousVersion => _linkedVersion(previousLink);

  SemVer? _linkedVersion(Link link) {
    try {
      final target = link.targetSync();
      return SemVer.tryParse(target.split('/').where((s) => s.isNotEmpty).last);
    } on FileSystemException {
      return null;
    }
  }

  /// Atomically points [link] at the version directory of [v] (a relative
  /// target, so the whole root can be moved): a temporary link is created and
  /// renamed over [link].
  void pointAt(Link link, SemVer v) {
    final tmp = Link('${link.path}.tmp');
    if (tmp.existsSync()) tmp.deleteSync();
    tmp.createSync('versions/$v');
    tmp.renameSync(link.path);
  }

  /// Removes every version directory except [keep] (current and previous).
  void prune(Set<String> keep) {
    if (!versionsDir.existsSync()) return;
    for (final entity in versionsDir.listSync(followLinks: false)) {
      final name = entity.path.split('/').last;
      if (keep.contains(name)) continue;
      try {
        entity.deleteSync(recursive: true);
      } on FileSystemException {
        // Best effort: a leftover directory is harmless and retried next time.
      }
    }
  }
}

/// The marker written when an update has been swapped in and is not yet known
/// to work.
class PendingUpdate {
  final String version;
  final String previous;
  final DateTime startedAt;
  final bool confirmed;

  const PendingUpdate({
    required this.version,
    required this.previous,
    required this.startedAt,
    this.confirmed = false,
  });

  PendingUpdate confirm() =>
      PendingUpdate(version: version, previous: previous, startedAt: startedAt, confirmed: true);

  Map<String, Object?> toJson() => {
        'version': version,
        'previous': previous,
        'started_at': startedAt.toUtc().toIso8601String(),
        'confirmed': confirmed,
      };

  static PendingUpdate? fromJson(Object? json) {
    if (json is! Map) return null;
    final version = json['version'];
    final previous = json['previous'];
    final startedAt = DateTime.tryParse('${json['started_at']}');
    if (version is! String || previous is! String || startedAt == null) return null;
    return PendingUpdate(
      version: version,
      previous: previous,
      startedAt: startedAt,
      confirmed: json['confirmed'] == true,
    );
  }
}

/// state.json: the pending marker and the versions that were rolled back (never
/// offered again; a newer release is tried normally).
class UpdateState {
  final PendingUpdate? pending;
  final Set<String> failed;
  const UpdateState({this.pending, this.failed = const {}});

  UpdateState copyWith({PendingUpdate? pending, bool clearPending = false, Set<String>? failed}) => UpdateState(
        pending: clearPending ? null : (pending ?? this.pending),
        failed: failed ?? this.failed,
      );

  /// Reads the state; a missing or corrupt file is an empty state (never an
  /// error: the update machinery must not stop the app from starting).
  static UpdateState read(File file) {
    try {
      final json = jsonDecode(file.readAsStringSync());
      if (json is! Map) return const UpdateState();
      final failed = json['failed'];
      return UpdateState(
        pending: PendingUpdate.fromJson(json['pending']),
        failed: failed is List ? failed.whereType<String>().toSet() : const {},
      );
    } on Object {
      return const UpdateState();
    }
  }

  /// Writes atomically (temp file + rename).
  void write(File file) {
    final tmp = File('${file.path}.tmp');
    tmp.writeAsStringSync(jsonEncode({
      'pending': pending?.toJson(),
      'failed': failed.toList()..sort(),
    }));
    tmp.renameSync(file.path);
  }
}
