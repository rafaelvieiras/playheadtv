// What the updater needs from the outside world, behind interfaces so the
// whole update flow runs against a fake release server in unit tests: the
// release listing and the file downloads (ReleaseSource), and unpacking an
// archive (ArchiveExtractor). The real implementations are GithubReleaseSource
// (GitHub Releases over HTTPS) and TarExtractor (the system `tar`).
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:http/http.dart' as http;

import 'semver.dart';

/// Tag prefix of client releases; backend releases are tagged `vX.Y.Z` in the
/// same repository, and the two must never be mixed up.
const clientTagPrefix = 'client-v';

/// Platform part of the package file name; the only build published today.
const clientPlatform = 'linux-x64';

/// `playheadtv-client-1.2.3-linux-x64.tar.gz`
String clientPackageName(SemVer version) => 'playheadtv-client-$version-$clientPlatform.tar.gz';

/// `playheadtv-client-1.2.3-linux-x64.AppImage`
String clientAppImageName(SemVer version) => 'playheadtv-client-$version-$clientPlatform.AppImage';

/// `playheadtv-client-1.2.3-checksums.txt` (sha256sum format). It lists every
/// downloadable artifact of the release and is the one file that is signed.
String clientChecksumsName(SemVer version) => 'playheadtv-client-$version-checksums.txt';

/// `playheadtv-client-1.2.3-checksums.txt.sig`: the detached Ed25519 signature
/// of the checksums file (base64 text).
String clientSignatureName(SemVer version) => '${clientChecksumsName(version)}.sig';

/// One published client release.
class ReleaseInfo {
  final SemVer version;

  /// Download URL of the package, and of the SHA-256 checksums file.
  final Uri packageUrl;
  final Uri checksumsUrl;

  /// The detached signature of the checksums file; null when the release has
  /// none (the updater refuses such a release, it is never a warning).
  final Uri? signatureUrl;

  /// The AppImage of this release, null when it was published without one.
  final Uri? appImageUrl;

  /// The body of the GitHub Release (the changelog), as published. Untrusted
  /// text: it is only ever shown as plain text, see `update_notes.dart`.
  final String notes;

  const ReleaseInfo({
    required this.version,
    required this.packageUrl,
    required this.checksumsUrl,
    this.signatureUrl,
    this.appImageUrl,
    this.notes = '',
  });
}

/// Source of client releases.
abstract class ReleaseSource {
  /// The newest published, non-draft, non-prerelease client release, or null
  /// when there is none. Throws when the source cannot be reached or answers
  /// something unusable (the updater treats any throw as "check failed").
  Future<ReleaseInfo?> latestStable();

  /// The bytes of [url] (the checksums file and its signature): the signature
  /// is checked over exactly these bytes.
  Future<List<int>> fetchBytes(Uri url);

  /// Downloads [url] into [dest], replacing it.
  Future<void> download(Uri url, File dest);
}

/// Unpacks a release package.
abstract class ArchiveExtractor {
  /// Extracts the gzip-compressed tar [archive] into the existing directory
  /// [destination]; throws on failure.
  Future<void> extract(File archive, Directory destination);
}

/// Extracts with the system `tar` (always present on a Linux desktop; GNU tar
/// already refuses absolute and `..` member paths).
class TarExtractor implements ArchiveExtractor {
  const TarExtractor();

  @override
  Future<void> extract(File archive, Directory destination) async {
    final result = await Process.run('tar', ['-xzf', archive.path, '-C', destination.path]);
    if (result.exitCode != 0) {
      throw FileSystemException('tar failed (${result.exitCode}): ${result.stderr}', archive.path);
    }
  }
}

/// GitHub Releases of one repository ("owner/name"), over HTTPS.
class GithubReleaseSource implements ReleaseSource {
  final String repo;
  final http.Client _client;
  final Duration timeout;

  GithubReleaseSource(this.repo, {http.Client? client, this.timeout = const Duration(seconds: 10)})
      : _client = client ?? http.Client();

  @override
  Future<ReleaseInfo?> latestStable() async {
    // Not /releases/latest: that returns the newest release of the whole
    // repository, which may be a backend one.
    final uri = Uri.https('api.github.com', '/repos/$repo/releases', {'per_page': '30'});
    final response = await _client.get(uri, headers: {'Accept': 'application/vnd.github+json'}).timeout(timeout);
    if (response.statusCode != 200) {
      throw HttpException('releases listing answered ${response.statusCode}', uri: uri);
    }
    return pickLatestClientRelease(jsonDecode(response.body));
  }

  @override
  Future<List<int>> fetchBytes(Uri url) async {
    final response = await _client.get(url).timeout(timeout);
    if (response.statusCode != 200) {
      throw HttpException('$url answered ${response.statusCode}', uri: url);
    }
    return response.bodyBytes;
  }

  @override
  Future<void> download(Uri url, File dest) async {
    final request = http.Request('GET', url);
    final response = await _client.send(request).timeout(timeout);
    if (response.statusCode != 200) {
      throw HttpException('$url answered ${response.statusCode}', uri: url);
    }
    if (dest.existsSync()) dest.deleteSync();
    final sink = dest.openWrite();
    try {
      // The per-chunk timeout turns a stalled connection into a failure
      // instead of an update that never ends.
      await sink.addStream(response.stream.timeout(timeout));
    } finally {
      await sink.close();
    }
  }
}

/// Picks the newest client release out of a decoded GitHub `releases` listing
/// (a JSON list): tag `client-vX.Y.Z`, not a draft, not a prerelease, with both
/// the package and the checksums asset for its own version (the signature and
/// the AppImage are optional here: a release without a signature is picked and
/// then refused by the updater, so the refusal is logged instead of the release
/// silently not existing). Anything malformed is skipped. Exposed for tests.
ReleaseInfo? pickLatestClientRelease(Object? listing) {
  if (listing is! List) return null;
  ReleaseInfo? best;
  for (final release in listing) {
    if (release is! Map) continue;
    if (release['draft'] == true || release['prerelease'] == true) continue;
    final tag = release['tag_name'];
    if (tag is! String || !tag.startsWith(clientTagPrefix)) continue;
    final version = SemVer.tryParse(tag.substring(clientTagPrefix.length));
    if (version == null || version.isPrerelease) continue;
    final assets = release['assets'];
    if (assets is! List) continue;
    Uri? packageUrl;
    Uri? checksumsUrl;
    Uri? signatureUrl;
    Uri? appImageUrl;
    for (final asset in assets) {
      if (asset is! Map) continue;
      final name = asset['name'];
      final url = asset['browser_download_url'];
      if (name is! String || url is! String) continue;
      if (name == clientPackageName(version)) packageUrl = Uri.tryParse(url);
      if (name == clientChecksumsName(version)) checksumsUrl = Uri.tryParse(url);
      if (name == clientSignatureName(version)) signatureUrl = Uri.tryParse(url);
      if (name == clientAppImageName(version)) appImageUrl = Uri.tryParse(url);
    }
    if (packageUrl == null || checksumsUrl == null) continue;
    if (best == null || version > best.version) {
      final body = release['body'];
      best = ReleaseInfo(
        version: version,
        packageUrl: packageUrl,
        checksumsUrl: checksumsUrl,
        signatureUrl: signatureUrl,
        appImageUrl: appImageUrl,
        notes: body is String ? body : '',
      );
    }
  }
  return best;
}
