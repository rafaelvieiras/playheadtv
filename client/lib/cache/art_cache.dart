// On-disk cache of the images the client shows (program posters, channel
// logos): one file per URL in a folder under the app's cache directory, with a
// size cap and least-recently-used eviction. The art lives on the media server
// (or wherever a channel logo points), never on the Playhead TV backend, so
// this keeps working with the backend down, and keeps the posters when the
// media server itself is momentarily out of reach.
//
// A file's modification time is its "last used" stamp: [cached] touches it, and
// eviction deletes the oldest first until the folder fits [maxBytes]. File
// names are a hash of the URL, so the media server key in a URL never lands on
// disk as a name.
import 'dart:async';
import 'dart:io';

import 'package:crypto/crypto.dart';
import 'package:http/http.dart' as http;

/// Default cap of the art folder.
const defaultArtCacheMaxBytes = 96 * 1024 * 1024;

class ArtCache {
  final Directory dir;
  final int maxBytes;
  final http.Client _client;
  final Duration timeout;
  final DateTime Function() _clock;

  // Running estimate of the folder's size, so eviction only scans when the cap
  // may actually have been crossed.
  int? _approxBytes;
  final _inFlight = <String, Future<File?>>{};
  final _failedAt = <String, DateTime>{};

  /// How long a URL that failed to download is left alone.
  static const retryAfterFailure = Duration(minutes: 1);

  ArtCache({
    required this.dir,
    this.maxBytes = defaultArtCacheMaxBytes,
    http.Client? client,
    this.timeout = const Duration(seconds: 10),
    DateTime Function()? clock,
  })  : _client = client ?? http.Client(),
        _clock = clock ?? DateTime.now;

  static String keyFor(String url) => sha256.convert(url.codeUnits).toString();

  File _fileFor(String url) => File('${dir.path}/${keyFor(url)}.img');

  /// The cached file for [url], or null when it is not cached. Marks it as
  /// just used.
  File? cached(String url) {
    final file = _fileFor(url);
    try {
      if (!file.existsSync()) return null;
      final now = _clock();
      // Touching on every build would be needless disk traffic.
      if (now.difference(file.lastModifiedSync()) > const Duration(minutes: 1)) file.setLastModifiedSync(now);
      return file;
    } on FileSystemException {
      return null;
    }
  }

  /// Downloads [url] into the cache (or returns the cached file). Null when it
  /// cannot be fetched. Concurrent calls for one URL share one download.
  Future<File?> fetch(String url) {
    final hit = cached(url);
    if (hit != null) return Future.value(hit);
    // A URL that just failed is not retried on every rebuild of a widget that
    // asks for it.
    final failed = _failedAt[url];
    if (failed != null && _clock().difference(failed) < retryAfterFailure) return Future.value(null);
    return _inFlight.putIfAbsent(url, () => _download(url).whenComplete(() {
      _inFlight.remove(url);
    }));
  }

  Future<File?> _download(String url) async {
    final file = await _tryDownload(url);
    if (file == null) {
      _failedAt[url] = _clock();
    } else {
      _failedAt.remove(url);
    }
    return file;
  }

  Future<File?> _tryDownload(String url) async {
    try {
      final response = await _client.get(Uri.parse(url)).timeout(timeout);
      if (response.statusCode != 200 || response.bodyBytes.isEmpty) return null;
      dir.createSync(recursive: true);
      final file = _fileFor(url);
      final part = File('${file.path}.part');
      await part.writeAsBytes(response.bodyBytes, flush: true);
      await part.rename(file.path);
      _approxBytes = (_approxBytes ?? await _scanBytes()) + response.bodyBytes.length;
      if (_approxBytes! > maxBytes) await evict();
      return file;
    } catch (_) {
      return null;
    }
  }

  Future<int> _scanBytes() async {
    var total = 0;
    if (!dir.existsSync()) return 0;
    await for (final f in dir.list()) {
      if (f is File) total += await f.length();
    }
    return total;
  }

  /// Deletes the least recently used files until the folder fits [maxBytes];
  /// returns how many were deleted. Also clears leftovers of interrupted
  /// downloads.
  Future<int> evict() async {
    if (!dir.existsSync()) return 0;
    final files = <({File file, int bytes, DateTime used})>[];
    await for (final f in dir.list()) {
      if (f is! File) continue;
      if (f.path.endsWith('.part')) {
        try {
          await f.delete();
        } catch (_) {}
        continue;
      }
      final stat = await f.stat();
      files.add((file: f, bytes: stat.size, used: stat.modified));
    }
    var total = files.fold<int>(0, (sum, f) => sum + f.bytes);
    files.sort((a, b) => a.used.compareTo(b.used));
    var deleted = 0;
    for (final f in files) {
      if (total <= maxBytes) break;
      try {
        await f.file.delete();
        total -= f.bytes;
        deleted++;
      } catch (_) {}
    }
    _approxBytes = total;
    return deleted;
  }

  /// Deletes every cached image (an explicit user action).
  Future<void> clear() async {
    if (dir.existsSync()) await dir.delete(recursive: true);
    _approxBytes = 0;
  }

  void close() => _client.close();
}
