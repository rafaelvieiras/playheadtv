// The boot prefetch, end to end against a fake backend: the work it starts
// before the first frame must be the very request playback would make, and the
// answer must be in the cache the player reads.
//
// No widgets, no video engine plugin registration: only the real
// `prefetchFirstChannel`, the real offline runtime, the real schedule cache and
// a local HTTP server standing in for the backend.
import 'dart:convert';
import 'dart:io';

import 'package:client/cache/offline_runtime.dart';
import 'package:client/cache/stream_cache.dart';
import 'package:client/main.dart' show AppConfig, prefetchFirstChannel;
import 'package:client/services/settings_service.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

const _channelId = 'chan-1';
const _itemId = 'item-1';

void main() {
  late HttpServer server;
  late Directory cacheDir;
  final playRequests = <Uri>[];
  final epgRequests = <Uri>[];
  var nowMs = DateTime.now().toUtc().millisecondsSinceEpoch;

  setUp(() async {
    playRequests.clear();
    epgRequests.clear();
    SharedPreferences.setMockInitialValues({});
    cacheDir = Directory.systemTemp.createTempSync('playheadtv_prefetch_');
    server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    server.listen((request) async {
      final uri = request.uri;
      final response = request.response;
      Map<String, Object?> json(Object? body) {
        response.headers.contentType = ContentType.json;
        response.write(jsonEncode(body));
        return const {};
      }

      if (uri.path == '/healthz') {
        json({'status': 'ok'});
      } else if (uri.path == '/plugins') {
        json([
          {'id': 'youtube', 'name': 'YouTube', 'installed': true, 'enabled': true}
        ]);
      } else if (uri.path == '/plugin-sources') {
        json(const []);
      } else if (uri.path == '/channels') {
        json([
          {'id': _channelId, 'number': 11, 'name': 'History'}
        ]);
      } else if (uri.path == '/epg/meta') {
        json({'revision': 7, 'window_ms': 48 * 3600 * 1000});
      } else if (uri.path == '/epg') {
        epgRequests.add(uri);
        // One program on air that needs a plugin, one later program.
        json([
          {
            'id': 'e-1',
            'channel_id': _channelId,
            'kind': 'program',
            'catalog_item_id': _itemId,
            'external_id': 'video-1',
            'title': 'Ep',
            'start_at': nowMs - 600000, // started 10 minutes ago
            'end_at': nowMs + 1200000,
            'requires': [
              {'kind': 'plugin', 'id': 'youtube'}
            ],
          }
        ]);
      } else if (uri.path == '/catalog-items/$_itemId/play') {
        playRequests.add(uri);
        json({'mode': 'pair', 'url': 'http://127.0.0.1:1/v', 'audio_url': 'http://127.0.0.1:1/a'});
      } else {
        response.statusCode = 404;
        json({'error': 'not found'});
      }
      await response.close();
    });

    AppConfig.overrideForTest(
      jellyfinUrl: 'http://127.0.0.1:1',
      jellyfinApiKey: 'k',
      playheadBaseUrl: 'http://127.0.0.1:${server.port}',
      playheadDeviceToken: 'tok',
      cacheDir: cacheDir.path,
      cacheSyncInterval: const Duration(hours: 1),
    );
    OfflineRuntime.reset();
    StreamCache.instance.clear();
    final settings = await SettingsService.create();
    await settings.recordWatched(_channelId);
  });

  tearDown(() async {
    OfflineRuntime.reset();
    StreamCache.instance.clear();
    await server.close(force: true);
    if (cacheDir.existsSync()) cacheDir.deleteSync(recursive: true);
  });

  test('resolves the item on air for the channel the app boots into', () async {
    final runtime = await OfflineRuntime.ensure();
    await runtime.syncer.syncNow(); // the guide is on disk, as after any earlier run

    await prefetchFirstChannel();

    expect(playRequests, hasLength(1), reason: 'one resolve, for the head unit only');
    final play = playRequests.single;
    expect(play.path, '/catalog-items/$_itemId/play');
    expect(play.queryParameters['modes'], contains('pair'), reason: 'never opens a remux for nothing');
    final askedMs = int.parse(play.queryParameters['start_ms']!);
    expect(askedMs, closeTo(600000, 3000), reason: 'the position the program is at now, not zero');

    final cached = StreamCache.instance.lookup(_itemId, Duration(milliseconds: askedMs));
    expect(cached, isNotNull, reason: 'the player reads this cache, so the work is reused');
    expect(cached!.stream.mode, 'pair');
    // The answer records the position it was cut at, not a bucket: a remux
    // reused later is at most one bucket off, and its clock is read from here.
    expect(cached.requestedAt.inMilliseconds, closeTo(askedMs, 1));
  });

  test('resolving again inside the reuse window does not ask the backend twice', () async {
    final runtime = await OfflineRuntime.ensure();
    await runtime.syncer.syncNow();

    await prefetchFirstChannel();
    await prefetchFirstChannel();

    expect(playRequests, hasLength(1));
  });

  test('a channel the cache knows nothing about is skipped, not guessed', () async {
    final settings = await SettingsService.create();
    await settings.recordWatched('chan-unknown');

    await prefetchFirstChannel();

    expect(playRequests, isEmpty);
    expect(epgRequests, isEmpty, reason: 'no network at all: the prefetch never syncs');
  });
}
