// Unit test (mock HTTP client standing in for the backend, in-memory cache)
// for the background syncer: the conditional `If-None-Match` refresh (200 once,
// then 304), a refresh that finds a different schedule, and the rule that no
// failure (backend down, rejected token, unexpected status) ever touches what
// is cached.
import 'dart:convert';

import 'package:client/cache/schedule_cache.dart';
import 'package:client/cache/schedule_sync.dart';
import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

final _now = DateTime.utc(2026, 10, 3, 12, 20);

Map<String, dynamic> _json(String channel, String id, DateTime start, Duration length, {String title = 'T'}) => {
      'id': id,
      'tenant_id': 'default',
      'channel_id': channel,
      'kind': 'program',
      'catalog_item_id': 'ci-$id',
      'start_at': start.millisecondsSinceEpoch,
      'end_at': start.add(length).millisecondsSinceEpoch,
      'external_id': 'jf-$id',
      'title': '$title $id',
    };

/// A scripted backend: serves `/channels`, `/epg/meta` and `/epg` with an ETag
/// that changes whenever [schedule] does, honoring `If-None-Match`.
class _FakeBackend {
  List<Map<String, dynamic>> schedule;
  int revision = 1;
  Duration? window = const Duration(hours: 48);
  bool down = false;
  int? failWith; // status for every request
  final requests = <http.Request>[];

  _FakeBackend(this.schedule);

  String get etag => '"$revision-${jsonEncode(schedule).hashCode}"';

  http.Client get client => MockClient((request) async {
        requests.add(request);
        if (down) throw http.ClientException('connection refused');
        if (failWith != null) return http.Response('no', failWith!);
        switch (request.url.path) {
          case '/channels':
            return http.Response(
              jsonEncode([
                {'id': 'c1', 'number': 1, 'name': 'One'},
                {'id': 'c2', 'number': 2, 'name': 'Two'},
              ]),
              200,
            );
          case '/epg/meta':
            return http.Response(
              jsonEncode({'revision': revision, 'valid_until': 0, 'freeze_margin_ms': 0, if (window != null) 'window_ms': window!.inMilliseconds}),
              200,
            );
          case '/epg':
            final headers = {'etag': etag, 'x-epg-revision': '$revision', 'content-type': 'application/json'};
            if (request.headers['If-None-Match'] == etag) return http.Response('', 304, headers: headers);
            return http.Response(jsonEncode(schedule), 200, headers: headers);
        }
        return http.Response('not found', 404);
      });
}

void main() {
  late _FakeBackend backend;
  late ScheduleCache cache;
  late ScheduleSyncer syncer;

  List<Map<String, dynamic>> initial() => [
        _json('c1', 'a', _now.subtract(const Duration(minutes: 10)), const Duration(hours: 1)),
        _json('c1', 'b', _now.add(const Duration(minutes: 50)), const Duration(hours: 1)),
        _json('c2', 'x', _now, const Duration(hours: 2)),
      ];

  setUp(() {
    backend = _FakeBackend(initial());
    cache = ScheduleCache.inMemory(clock: () => _now);
    syncer = ScheduleSyncer(
      api: PlayheadApi(baseUrl: 'http://backend.test', deviceToken: 'tok', client: backend.client),
      cache: cache,
      interval: const Duration(minutes: 15),
    );
  });
  tearDown(() {
    syncer.stop();
    cache.close();
  });

  List<http.Request> epgRequests() => backend.requests.where((r) => r.url.path == '/epg').toList();

  test('the first refresh downloads everything, a second one is a 304 that downloads nothing', () async {
    expect(await syncer.syncNow(), SyncOutcome.updated);
    expect(cache.channels().map((c) => c.id), ['c1', 'c2']);
    expect(cache.entries('c1', _now, _now.add(const Duration(hours: 3))).map((e) => e.id), ['a', 'b']);
    expect(cache.revision, 1);
    expect(cache.backendWindow, const Duration(hours: 48));
    expect(cache.lastSyncAt, _now);
    expect(epgRequests().single.headers.containsKey('If-None-Match'), isFalse);
    expect(epgRequests().single.url.queryParameters['group_tail'], '1');
    // Every request carries the device token.
    expect(backend.requests.every((r) => r.headers['Authorization'] == 'Bearer tok'), isTrue);

    expect(await syncer.syncNow(), SyncOutcome.unchanged);
    final second = epgRequests().last;
    expect(second.headers['If-None-Match'], backend.etag, reason: 'the validator of the copy held is sent back');
    expect(cache.entries('c1', _now, _now.add(const Duration(hours: 3))).map((e) => e.id), ['a', 'b'], reason: '304 keeps the cache as is');
  });

  test('a changed schedule is downloaded and replaces what changed, announcing the channel', () async {
    await syncer.syncNow();
    final changes = <CacheChange>[];
    cache.changes.listen(changes.add);

    backend.revision = 2;
    backend.schedule = [
      _json('c1', 'a', _now.subtract(const Duration(minutes: 10)), const Duration(hours: 1)),
      _json('c1', 'n', _now.add(const Duration(minutes: 50)), const Duration(hours: 1), title: 'New'),
      _json('c2', 'x', _now, const Duration(hours: 2)),
    ];
    expect(await syncer.syncNow(), SyncOutcome.updated);
    expect(epgRequests().last.headers['If-None-Match'], isNotNull, reason: 'still conditional; the answer was a 200 because it changed');
    expect(cache.entries('c1', _now, _now.add(const Duration(hours: 3))).map((e) => e.id), ['a', 'n']);
    expect(cache.revision, 2);
    await Future<void>.delayed(Duration.zero);
    expect(changes.single.channelIds, {'c1'});
  });

  test('the validator is not sent for another client window limit', () async {
    await syncer.syncNow();
    cache.clientLimit = const Duration(hours: 12);
    await syncer.syncNow();
    expect(epgRequests().last.headers.containsKey('If-None-Match'), isFalse);
  });

  test('a backend that is down leaves the cache untouched and reports offline', () async {
    await syncer.syncNow();
    final before = cache.entries('c1', _now, _now.add(const Duration(hours: 3))).map((e) => e.id).toList();
    final syncedAt = cache.lastSyncAt;

    backend.down = true;
    expect(await syncer.syncNow(), SyncOutcome.offline);
    expect(syncer.status.value.lastOutcome, SyncOutcome.offline);
    expect(syncer.status.value.lastSucceeded, isFalse);
    expect(cache.entries('c1', _now, _now.add(const Duration(hours: 3))).map((e) => e.id), before);
    expect(cache.channels(), hasLength(2));
    expect(cache.lastSyncAt, syncedAt, reason: 'a failed refresh is not a refresh');

    // It comes back: the next refresh succeeds with no re-download needed.
    backend.down = false;
    expect(await syncer.syncNow(), SyncOutcome.unchanged);
  });

  test('a rejected token keeps the cache, is reported as such and recovers on the next refresh', () async {
    await syncer.syncNow();
    final before = cache.allEntries('c1').map((e) => e.id).toList();

    backend.failWith = 401;
    expect(await syncer.syncNow(), SyncOutcome.authRejected);
    backend.failWith = 403;
    expect(await syncer.syncNow(), SyncOutcome.authRejected);
    expect(cache.allEntries('c1').map((e) => e.id), before, reason: 'never discarded on an auth error');
    expect(cache.channels(), hasLength(2));

    backend.failWith = null; // the backend accepts the credentials again
    backend.revision = 5;
    backend.schedule = [_json('c1', 'z', _now, const Duration(hours: 1)), _json('c2', 'x', _now, const Duration(hours: 2))];
    expect(await syncer.syncNow(), SyncOutcome.updated);
    expect(cache.entries('c1', _now, _now.add(const Duration(hours: 1))).map((e) => e.id), ['z']);
  });

  test('an unexpected status is a failure that keeps the cache', () async {
    await syncer.syncNow();
    backend.failWith = 500;
    expect(await syncer.syncNow(), SyncOutcome.failed);
    expect(cache.allEntries('c1'), isNotEmpty);
  });

  test('the client limit caps what is stored even when the backend sends more', () async {
    backend.window = const Duration(hours: 72);
    backend.schedule = [for (var h = 0; h < 60; h++) _json('c1', 'p$h', _now.add(Duration(hours: h)), const Duration(hours: 1))];
    cache.clientLimit = const Duration(hours: 24);
    await syncer.syncNow();
    expect(cache.window, const Duration(hours: 24));
    expect(cache.allEntries('c1').length, 24);
  });

  test('the request starts at the top of the previous hour so the validator holds for the whole hour', () {
    expect(ScheduleSyncer.downloadStart(DateTime.utc(2026, 10, 3, 12, 20)), DateTime.utc(2026, 10, 3, 11));
    expect(ScheduleSyncer.downloadStart(DateTime.utc(2026, 10, 3, 12, 59, 59)), DateTime.utc(2026, 10, 3, 11));
    expect(ScheduleSyncer.downloadStart(DateTime.utc(2026, 10, 3, 13)), DateTime.utc(2026, 10, 3, 12));
  });

  test('an entry a backend left unresolved is completed once, before it is stored', () async {
    backend.schedule = [
      {
        'id': 'e1',
        'channel_id': 'c1',
        'kind': 'program',
        'catalog_item_id': 'ci-9',
        'start_at': _now.millisecondsSinceEpoch,
        'end_at': _now.add(const Duration(hours: 1)).millisecondsSinceEpoch,
      },
    ];
    final lookups = <String>[];
    final client = MockClient((request) async {
      if (request.url.path.startsWith('/catalog-items/')) {
        lookups.add(request.url.path);
        return http.Response(jsonEncode({'id': 'ci-9', 'external_id': 'jf-9', 'title': 'Legacy nine', 'duration_ms': 3600000}), 200);
      }
      return backend.client.get(request.url, headers: request.headers);
    });
    final legacy = ScheduleSyncer(
      api: PlayheadApi(baseUrl: 'http://backend.test', client: client),
      cache: cache,
      interval: const Duration(minutes: 15),
    );
    await legacy.syncNow();
    final stored = cache.entries('c1', _now, _now.add(const Duration(hours: 1))).single;
    expect(stored.externalId, 'jf-9');
    expect(stored.title, 'Legacy nine');
    expect(lookups, hasLength(1));
    legacy.stop();
  });
}
