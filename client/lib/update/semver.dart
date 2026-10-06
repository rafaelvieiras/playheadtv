// Minimal semantic version: MAJOR.MINOR.PATCH with an optional -prerelease
// (build metadata after `+` is ignored, as the spec says). Pure Dart: the
// updater CLI (tool/updater.dart) is compiled without Flutter.

/// A parsed semantic version, ordered by the semver precedence rules.
class SemVer implements Comparable<SemVer> {
  final int major;
  final int minor;
  final int patch;

  /// Dot-separated prerelease identifiers, empty for a release.
  final List<String> prerelease;

  const SemVer(this.major, this.minor, this.patch, [this.prerelease = const []]);

  static final _pattern = RegExp(r'^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$');

  /// Parses "1.2.3", "v1.2.3", "1.2.3-rc.1" or "1.2.3+5"; null when [text] is
  /// not a version.
  static SemVer? tryParse(String text) {
    final m = _pattern.firstMatch(text.trim());
    if (m == null) return null;
    return SemVer(
      int.parse(m.group(1)!),
      int.parse(m.group(2)!),
      int.parse(m.group(3)!),
      m.group(4) == null ? const [] : m.group(4)!.split('.'),
    );
  }

  bool get isPrerelease => prerelease.isNotEmpty;

  @override
  int compareTo(SemVer other) {
    for (final pair in [(major, other.major), (minor, other.minor), (patch, other.patch)]) {
      if (pair.$1 != pair.$2) return pair.$1.compareTo(pair.$2);
    }
    // A release outranks any of its prereleases.
    if (prerelease.isEmpty && other.prerelease.isEmpty) return 0;
    if (prerelease.isEmpty) return 1;
    if (other.prerelease.isEmpty) return -1;
    final n = prerelease.length < other.prerelease.length ? prerelease.length : other.prerelease.length;
    for (var i = 0; i < n; i++) {
      final a = prerelease[i];
      final b = other.prerelease[i];
      if (a == b) continue;
      final ai = int.tryParse(a);
      final bi = int.tryParse(b);
      if (ai != null && bi != null) return ai.compareTo(bi);
      if (ai != null) return -1; // numeric identifiers rank below textual ones
      if (bi != null) return 1;
      return a.compareTo(b);
    }
    return prerelease.length.compareTo(other.prerelease.length);
  }

  bool operator >(SemVer other) => compareTo(other) > 0;

  @override
  bool operator ==(Object other) => other is SemVer && compareTo(other) == 0;

  @override
  int get hashCode => Object.hash(major, minor, patch, prerelease.join('.'));

  @override
  String toString() => '$major.$minor.$patch${prerelease.isEmpty ? '' : '-${prerelease.join('.')}'}';
}
