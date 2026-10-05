// Unit test (temp directory, mock HTTP client) for the on-disk art cache: one
// file per URL, a size cap, least-recently-used eviction, and no hammering of a
// URL that just failed.
import 'dart:io';
import 'dart:typed_data';

import 'package:client/cache/art_cache.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

void main() {
  late Directory dir;
  setUp(() => dir = Directory.systemTemp.createTempSync('art_cache_test_'));
  tearDown(() => dir.deleteSync(recursive: true));

  Uint8List bytes(int n) => Uint8List.fromList(List.filled(n, 7));

  ArtCache cache({int maxBytes = 1000, required Map<String, int> sizes, List<String>? requested, DateTime Function()? clock}) {
    final client = MockClient((request) async {
      requested?.add(request.url.toString());
      final size = sizes[request.url.toString()];
      if (size == null) return http.Response('missing', 404);
      return http.Response.bytes(bytes(size), 200);
    });
    return ArtCache(dir: Directory('${dir.path}/art'), maxBytes: maxBytes, client: client, clock: clock);
  }

  void stamp(ArtCache c, String url, DateTime when) => File('${dir.path}/art/${ArtCache.keyFor(url)}.img').setLastModifiedSync(when);

  test('downloads once, then serves the file from disk', () async {
    final requested = <String>[];
    final art = cache(sizes: {'http://media/a': 100}, requested: requested);
    expect(art.cached('http://media/a'), isNull);
    final file = await art.fetch('http://media/a');
    expect(file, isNotNull);
    expect(file!.lengthSync(), 100);
    expect(art.cached('http://media/a')!.path, file.path);
    await art.fetch('http://media/a');
    expect(requested, hasLength(1));
    // The URL (it carries the media server key) is never a file name.
    expect(file.path, isNot(contains('media')));
  });

  test('concurrent fetches of one URL share one download', () async {
    final requested = <String>[];
    final art = cache(sizes: {'http://media/a': 10}, requested: requested);
    await Future.wait([art.fetch('http://media/a'), art.fetch('http://media/a'), art.fetch('http://media/a')]);
    expect(requested, hasLength(1));
  });

  test('evicts the least recently used files once over the cap, keeping the recent ones', () async {
    final urls = [for (var i = 0; i < 5; i++) 'http://media/$i'];
    final art = cache(maxBytes: 450, sizes: {for (final u in urls) u: 100});
    final base = DateTime.now().subtract(const Duration(hours: 1));
    // Four files fit under the cap (400 <= 450).
    for (var i = 0; i < 4; i++) {
      await art.fetch(urls[i]);
      stamp(art, urls[i], base.add(Duration(minutes: i)));
    }
    // Using the oldest one makes it the most recent.
    stamp(art, urls[0], base.add(const Duration(minutes: 30)));
    expect(art.cached(urls[0]), isNotNull);

    // The fifth pushes the total to 500: the least recently used (urls[1]) goes.
    await art.fetch(urls[4]);
    expect(art.cached(urls[1]), isNull, reason: 'least recently used');
    expect(art.cached(urls[0]), isNotNull, reason: 'used recently');
    expect(art.cached(urls[2]), isNotNull);
    expect(art.cached(urls[3]), isNotNull);
    expect(art.cached(urls[4]), isNotNull, reason: 'just downloaded');
    final total = Directory('${dir.path}/art').listSync().whereType<File>().fold<int>(0, (s, f) => s + f.lengthSync());
    expect(total, lessThanOrEqualTo(450));
  });

  test('evict removes as many as needed and reports how many', () async {
    final urls = [for (var i = 0; i < 6; i++) 'http://media/$i'];
    final big = cache(maxBytes: 10000, sizes: {for (final u in urls) u: 100});
    final base = DateTime.now().subtract(const Duration(hours: 1));
    for (var i = 0; i < 6; i++) {
      await big.fetch(urls[i]);
      stamp(big, urls[i], base.add(Duration(minutes: i)));
    }
    final small = ArtCache(dir: Directory('${dir.path}/art'), maxBytes: 250);
    expect(await small.evict(), 4);
    expect(small.cached(urls[0]), isNull);
    expect(small.cached(urls[3]), isNull);
    expect(small.cached(urls[4]), isNotNull);
    expect(small.cached(urls[5]), isNotNull);
    expect(await small.evict(), 0);
  });

  test('a failed download is not retried at once and leaves nothing behind', () async {
    final requested = <String>[];
    var now = DateTime(2026, 10, 3, 12);
    final art = cache(sizes: {}, requested: requested, clock: () => now);
    expect(await art.fetch('http://media/gone'), isNull);
    expect(await art.fetch('http://media/gone'), isNull);
    expect(requested, hasLength(1));
    now = now.add(ArtCache.retryAfterFailure + const Duration(seconds: 1));
    expect(await art.fetch('http://media/gone'), isNull);
    expect(requested, hasLength(2));
    expect(Directory('${dir.path}/art').existsSync() ? Directory('${dir.path}/art').listSync() : const [], isEmpty);
  });

  test('clear deletes every cached image', () async {
    final art = cache(sizes: {'http://media/a': 10});
    await art.fetch('http://media/a');
    await art.clear();
    expect(art.cached('http://media/a'), isNull);
  });
}
