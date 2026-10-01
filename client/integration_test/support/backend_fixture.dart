// Starts a REAL Go backend (`go run ./cmd/server`) on a free port with a
// temporary SQLite, and exposes high-level helpers to populate
// channel/media_source/catalog_item/block/schedule_entry via HTTP — extracted
// from the bootstrap flow already proven in `test/playhead_api_test.dart`
// (read that file first: this is the same flow, just repackaged into a class
// reusable by the integration tests in `integration_test/`, which need the
// SAME real base, with no mocks, but repeated across several scenarios).
//
// Each instance uses its own tempDir + port — it never touches the shared
// docker compose container (port 9394) nor any other process that is already
// running (see AGENTS.md).
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:http/http.dart' as http;

class BackendFixture {
  Directory? _tempDir;
  Process? _process;
  String? _baseUrl;
  String? _sessionToken;
  final http.Client _client = http.Client();

  String get baseUrl {
    final url = _baseUrl;
    if (url == null) throw StateError('BackendFixture.start() has not run yet');
    return url;
  }

  /// Starts `go run ./cmd/server` with a temporary DB on a free port, waits
  /// for `/healthz` to answer and does the one-time bootstrap of the tenant's
  /// app_user (POST /app-users + POST /sessions) to keep a session token used
  /// by all the other helpers of this class.
  Future<void> start() async {
    final backendDir = Directory('${Directory.current.path}/../backend');
    if (!await backendDir.exists()) {
      throw StateError(
        'backend/ not found at ${backendDir.path} — run `flutter test` from inside client/, with backend/ next to it.',
      );
    }

    _tempDir = await Directory.systemTemp.createTemp('playheadtv_it_backend_');
    final dbPath = '${_tempDir!.path}/playheadtv_it.db';

    // Same bind-port-0-and-release trick as the widget test: small race
    // window, acceptable for local test use only.
    final probeSocket = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
    final port = probeSocket.port;
    await probeSocket.close();
    _baseUrl = 'http://127.0.0.1:$port';

    _process = await Process.start(
      'go',
      ['run', './cmd/server'],
      workingDirectory: backendDir.path,
      environment: {
        'PLAYHEADTV_DB_PATH': dbPath,
        'PLAYHEADTV_ADDR': ':$port',
      },
      includeParentEnvironment: true,
    );
    _process!.stdout.transform(utf8.decoder).listen((l) => stdout.write('[backend] $l'));
    _process!.stderr.transform(utf8.decoder).listen((l) => stderr.write('[backend] $l'));

    final deadline = DateTime.now().add(const Duration(seconds: 30));
    while (true) {
      try {
        final resp = await _client.get(Uri.parse('$_baseUrl/healthz')).timeout(const Duration(seconds: 1));
        if (resp.statusCode == 200) break;
      } catch (_) {
        // backend still starting (or compiling, on the first run) — try again.
      }
      if (DateTime.now().isAfter(deadline)) {
        throw StateError('backend did not answer /healthz at $_baseUrl in time (30s)');
      }
      await Future.delayed(const Duration(milliseconds: 200));
    }

    await _postJson('/app-users', {'email': 'e2e-test@example.com', 'password': 'test-password-123'});
    final login = await _postJson(
      '/sessions',
      {'email': 'e2e-test@example.com', 'password': 'test-password-123'},
    );
    _sessionToken = login['token'] as String;
  }

  /// Kills the backend process (SIGTERM, falling back to SIGKILL if it doesn't
  /// exit within 5s) and deletes the SQLite temp directory. Idempotent/
  /// best-effort — safe to call in tearDown even if start() failed midway.
  Future<void> stop() async {
    final process = _process;
    if (process != null) {
      process.kill(ProcessSignal.sigterm);
      try {
        await process.exitCode.timeout(const Duration(seconds: 5));
      } on TimeoutException {
        process.kill(ProcessSignal.sigkill);
      }
    }
    _client.close();
    final dir = _tempDir;
    if (dir != null) {
      try {
        await dir.delete(recursive: true);
      } catch (_) {
        // best-effort — don't let the test fail because of cleanup.
      }
    }
  }

  Future<Map<String, dynamic>> _postJson(String path, Map<String, dynamic> body, {bool auth = true}) async {
    final resp = await _client.post(
      Uri.parse('$_baseUrl$path'),
      headers: {
        'Content-Type': 'application/json',
        if (auth && _sessionToken != null) 'Authorization': 'Bearer $_sessionToken',
      },
      body: jsonEncode(body),
    );
    if (resp.statusCode != 200 && resp.statusCode != 201) {
      throw StateError('POST $path -> ${resp.statusCode}: ${resp.body}');
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  Future<dynamic> _getJson(String path) async {
    final resp = await _client.get(
      Uri.parse('$_baseUrl$path'),
      headers: {'Authorization': 'Bearer $_sessionToken'},
    );
    if (resp.statusCode != 200) {
      throw StateError('GET $path -> ${resp.statusCode}: ${resp.body}');
    }
    return jsonDecode(resp.body);
  }

  Future<Map<String, dynamic>> _putJson(String path, Map<String, dynamic> body) async {
    final resp = await _client.put(
      Uri.parse('$_baseUrl$path'),
      headers: {
        'Content-Type': 'application/json',
        if (_sessionToken != null) 'Authorization': 'Bearer $_sessionToken',
      },
      body: jsonEncode(body),
    );
    if (resp.statusCode != 200) {
      throw StateError('PUT $path -> ${resp.statusCode}: ${resp.body}');
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  Future<String> createChannel({required int number, required String name}) async {
    final resp = await _postJson('/channels', {'number': number, 'name': name, 'timezone': 'UTC'});
    return resp['id'] as String;
  }

  Future<String> createLocalFolderMediaSource({required String name}) async {
    final resp = await _postJson('/media-sources', {'type': 'local_folder', 'name': name});
    return resp['id'] as String;
  }

  /// Sends `items` (each a Map with at least `external_id`/`kind`/`title`/
  /// `duration_ms`) via `POST /media-sources/{id}/catalog-sync` and returns the
  /// resulting `catalog_item` ids IN THE SAME ORDER as the `external_id`s sent
  /// (catalog-sync doesn't return the created ids, so it fetches via GET
  /// /catalog-items?media_source_id=... and matches by external_id).
  Future<List<String>> syncCatalogItems(String mediaSourceId, List<Map<String, Object?>> items) async {
    await _postJson('/media-sources/$mediaSourceId/catalog-sync', {'items': items});
    final data = await _getJson('/catalog-items?media_source_id=$mediaSourceId&limit=200') as List;
    final byExternalId = <String, String>{
      for (final raw in data.cast<Map<String, dynamic>>()) raw['external_id'] as String: raw['id'] as String,
    };
    return [
      for (final item in items) byExternalId[item['external_id'] as String]!,
    ];
  }

  Future<String> createBlockWithItems(List<String> catalogItemIds) async {
    final blockResp = await _postJson('/blocks', {'name': 'Block ${DateTime.now().microsecondsSinceEpoch}'});
    final blockId = blockResp['id'] as String;
    for (final catalogItemId in catalogItemIds) {
      await _postJson('/blocks/$blockId/items', {'catalog_item_id': catalogItemId});
    }
    return blockId;
  }

  /// Creates a Schedule + a single `once` ScheduleItem inside it, then
  /// assigns it to the channel. A thin wrapper over createScheduleWithItems
  /// below for the common single-occurrence case most suites need.
  Future<void> createScheduleOnce({
    required String channelId,
    required String blockId,
    required DateTime startAt,
  }) =>
      createScheduleWithItems(channelId: channelId, items: [(blockId: blockId, startAt: startAt)]);

  /// Creates a Schedule with one or more `once` ScheduleItems (each its own
  /// block + explicit start_at), then assigns it to the channel (issue #7's
  /// Schedule/Playout split: schedule_entry became Schedule + ScheduleItem,
  /// decoupled from Channel — see `docs/ARCHITECTURE.md`). `PUT /channels/{id}`
  /// is a full replace (channels.go), so the channel's current fields are
  /// fetched first — otherwise this would wipe out its number/name/timezone.
  ///
  /// Several items with a start_at gap between one's nominal content end and
  /// the next one's start_at materialize a real interior EPG gap (no filler
  /// rule configured by these tests to fill it) — see epg_gap_test.dart
  /// (issue #9).
  Future<void> createScheduleWithItems({
    required String channelId,
    required List<({String blockId, DateTime startAt})> items,
  }) async {
    final scheduleResp = await _postJson('/schedules', {
      'name': 'Schedule ${DateTime.now().microsecondsSinceEpoch}',
    });
    final scheduleId = scheduleResp['id'] as String;
    for (final item in items) {
      await _postJson('/schedules/$scheduleId/items', {
        'block_id': item.blockId,
        'recurrence_type': 'once',
        'start_at': item.startAt.toUtc().millisecondsSinceEpoch,
      });
    }
    final ch = await _getJson('/channels/$channelId') as Map<String, dynamic>;
    await _putJson('/channels/$channelId', {
      'number': ch['number'],
      'name': ch['name'],
      'guide_art_url': ch['guide_art_url'],
      'timezone': ch['timezone'],
      'schedule_id': scheduleId,
    });
  }

  Future<String> createDeviceToken({String name = 'E2E test client', String platform = 'linux'}) async {
    final resp = await _postJson('/devices', {'name': name, 'platform': platform});
    return resp['token'] as String;
  }
}
