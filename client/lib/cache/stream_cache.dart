// Resolved plugin stream addresses, in memory for the life of the process.
//
// A plugin-backed item has no url until somebody asks the backend for one
// (`GET /catalog-items/{id}/play`), and that answer is expensive on the host:
// an extractor run, 250-350 ms when its own cache is warm and seconds when it is
// not. The same answer used to live in the player page, which meant a resolve
// started before the page existed (the boot prefetch) could not be handed to
// it. It lives here so both sides share one answer.
//
// Not part of the offline cache: nothing here survives the process, and nothing
// here is written to disk. A url carries the plugin's token and a `remux`
// stream lives only while its process does, so a stale one is worse than none.
//
// See docs/ARCHITECTURE.md "Plugin playback cache".
import '../playhead_api.dart';

/// How long a resolved url may be reused. The player drops a url the moment an
/// open with it fails, whatever this says.
const pluginStreamReuse = Duration(seconds: 45);

/// How far apart two requests for the same item may be and still share one
/// answer.
///
/// A `pair` starts at the item's own zero and the player seeks, so any of them
/// plays the exact position asked for — sharing across the bucket costs
/// nothing. A `remux` is cut where it was asked to start, so a shared one puts
/// the picture up to one bucket behind the wall clock: 2 s is the same slop
/// `epgSyncTolerance` already allows between the guide and what is playing, and
/// it is what lets a resolve started at boot (before the first frame) be the
/// one playback uses, instead of paying the extractor twice.
const streamPositionBucket = Duration(seconds: 2);

/// One answered `/play` request: the stream, and the position it was answered
/// for.
class CachedStream {
  final PluginStream stream;

  /// The position within the item that was asked for. For a `remux` this is
  /// where the stream itself begins, which is what the player adds back to
  /// every position it reads (`_playbackOffset`).
  final Duration requestedAt;

  final DateTime at;

  const CachedStream({required this.stream, required this.requestedAt, required this.at});
}

/// The process-wide cache (one player page at a time, one boot prefetch).
class StreamCache {
  StreamCache();

  static final StreamCache instance = StreamCache();

  final Map<String, CachedStream> _entries = {};

  /// The bucket a [position] falls in: what the cache is keyed by.
  static Duration bucketOf(Duration position) =>
      Duration(milliseconds: (position.inMilliseconds ~/ streamPositionBucket.inMilliseconds) * streamPositionBucket.inMilliseconds);

  static String _key(String itemId, Duration position) => '$itemId|${bucketOf(position).inMilliseconds}';

  /// The stream already resolved for [itemId] near [position], or null when
  /// there is none, it is older than [pluginStreamReuse], or it was resolved
  /// for a different position bucket.
  CachedStream? lookup(String itemId, Duration position, {DateTime? now}) {
    final key = _key(itemId, position);
    final entry = _entries[key];
    if (entry == null) return null;
    final at = now ?? DateTime.now();
    if (at.difference(entry.at) >= pluginStreamReuse) {
      _entries.remove(key);
      return null;
    }
    return entry;
  }

  /// Keeps [stream], resolved for [requestedAt], and forgets everything older
  /// than [pluginStreamReuse] (a long session that hops channels never grows
  /// without bound).
  void store(String itemId, Duration requestedAt, PluginStream stream, {DateTime? now}) {
    final at = now ?? DateTime.now();
    _entries.removeWhere((_, v) => at.difference(v.at) >= pluginStreamReuse);
    _entries[_key(itemId, requestedAt)] = CachedStream(stream: stream, requestedAt: requestedAt, at: at);
  }

  /// Drops the answer for [position] after an open with it failed: the url may
  /// be the reason, so it is never tried twice.
  void forget(String itemId, Duration position) => _entries.remove(_key(itemId, position));

  int get length => _entries.length;

  void clear() => _entries.clear();
}
