// Unit tests for the in-memory plugin stream cache: the position bucket that
// lets a resolve started at boot be the one playback uses, the reuse window,
// the position each answer was cut at, and the drop after a failed open.
import 'package:client/cache/stream_cache.dart';
import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';

PluginStream _stream(String mode) => PluginStream(uri: Uri.parse('http://host/s/$mode'), mode: mode);

void main() {
  final at = DateTime.utc(2030, 1, 1, 12);

  test('a stream resolved for a position is found from the same position', () {
    final cache = StreamCache();
    cache.store('item', const Duration(seconds: 10), _stream('pair'), now: at);

    final hit = cache.lookup('item', const Duration(seconds: 10), now: at);
    expect(hit, isNotNull);
    expect(hit!.stream.mode, 'pair');
    expect(hit.requestedAt, const Duration(seconds: 10));
  });

  test('a position inside the same bucket shares one answer', () {
    final cache = StreamCache();
    cache.store('item', const Duration(seconds: 10), _stream('pair'), now: at);

    // 10.0 and 11.9 are the same 2 s bucket; 12.0 is the next one.
    expect(cache.lookup('item', const Duration(seconds: 11, milliseconds: 900), now: at), isNotNull);
    expect(cache.lookup('item', const Duration(seconds: 12), now: at), isNull);
  });

  test('a reused answer carries the position IT was cut at, not the one asked now', () {
    final cache = StreamCache();
    cache.store('item', const Duration(seconds: 10), _stream('remux'), now: at);

    final hit = cache.lookup('item', const Duration(seconds: 11), now: at);
    // A remux begins where it was asked to: playback adds this back to every
    // position it reads, so it has to be the stream's own start.
    expect(hit!.requestedAt, const Duration(seconds: 10));
    expect(hit.stream.startsAtRequestedPosition, isTrue);
  });

  test('an answer older than the reuse window is not handed out', () {
    final cache = StreamCache();
    cache.store('item', Duration.zero, _stream('pair'), now: at);
    final later = at.add(pluginStreamReuse);

    expect(cache.lookup('item', Duration.zero, now: later), isNull);
    expect(cache.length, 0, reason: 'dropped on lookup, not kept around');
  });

  test('storing drops everything that expired', () {
    final cache = StreamCache();
    cache.store('a', Duration.zero, _stream('pair'), now: at);
    cache.store('b', Duration.zero, _stream('pair'), now: at.add(pluginStreamReuse));

    expect(cache.length, 1);
    expect(cache.lookup('a', Duration.zero, now: at.add(pluginStreamReuse)), isNull);
    expect(cache.lookup('b', Duration.zero, now: at.add(pluginStreamReuse)), isNotNull);
  });

  test('one item never answers for another, even in the same bucket', () {
    final cache = StreamCache();
    cache.store('a', Duration.zero, _stream('pair'), now: at);
    expect(cache.lookup('b', Duration.zero, now: at), isNull);
  });

  test('forget drops the answer for exactly that position', () {
    final cache = StreamCache();
    cache.store('item', const Duration(seconds: 4), _stream('pair'), now: at);

    cache.forget('item', const Duration(seconds: 4));
    expect(cache.lookup('item', const Duration(seconds: 4), now: at), isNull);
  });

  test('the bucket is the same before and after a store of the same position', () {
    expect(StreamCache.bucketOf(const Duration(seconds: 7, milliseconds: 999)), const Duration(seconds: 6));
    expect(StreamCache.bucketOf(const Duration(seconds: 8)), const Duration(seconds: 8));
    expect(streamPositionBucket, const Duration(seconds: 2));
  });
}
