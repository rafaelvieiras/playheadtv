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

// The suites normally run next to a real backend: [BackendFixture.start]
// builds the Go server and spawns it from the test process. On a device
// (integration_test on an emulator or a set-top box) that process is inside
// Android, where there is no `go` and no checkout, so the lab starts the
// backend on the host, maps it into the device with `adb reverse` and passes
// its address here (client/tool/android_lab.sh). The rest of the fixture does
// not change: the app and the helpers still talk to http://127.0.0.1:<port>,
// which on the device is the reverse tunnel.
const _remoteBaseUrl = String.fromEnvironment('PLAYHEADTV_IT_BACKEND_URL');

class BackendFixture {
  Directory? _tempDir;
  Process? _process;
  String? _baseUrl;
  String? _dbPath;
  String? _binaryPath;
  Map<String, String> _extraEnv = const {};
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
  ///
  /// The server is built once into the temp directory and run as that binary
  /// (not through `go run`), so [stopServer] really takes it down and
  /// [restartServer] brings it back on the same address with the same data.
  /// [env] adds environment variables (e.g. `PLAYHEADTV_EPG_WINDOW`).
  ///
  /// With `PLAYHEADTV_IT_BACKEND_URL` set (see [_remoteBaseUrl]) nothing is
  /// built or spawned: the backend already runs on the host and this only
  /// waits for `/healthz` and does the bootstrap. [stopServer] and
  /// [restartServer] throw in that mode, so the suites that need them stay on
  /// the desktop.
  Future<void> start({Map<String, String> env = const {}}) async {
    if (_remoteBaseUrl.isNotEmpty) {
      _extraEnv = env;
      _baseUrl = _remoteBaseUrl;
      await _waitForHealth();
      await _bootstrap();
      return;
    }
    final backendDir = Directory('${Directory.current.path}/../backend');
    if (!await backendDir.exists()) {
      throw StateError(
        'backend/ not found at ${backendDir.path} — run `flutter test` from inside client/, with backend/ next to it.',
      );
    }

    _extraEnv = env;
    _tempDir = await Directory.systemTemp.createTemp('playheadtv_it_backend_');
    _dbPath = '${_tempDir!.path}/playheadtv_it.db';
    final binaryPath = _binaryPath = '${_tempDir!.path}/server';
    final build = await Process.run('go', ['build', '-o', binaryPath, './cmd/server'], workingDirectory: backendDir.path);
    if (build.exitCode != 0) {
      throw StateError('go build of the backend failed (exit ${build.exitCode}): ${build.stderr}');
    }

    // Same bind-port-0-and-release trick as the widget test: small race
    // window, acceptable for local test use only.
    final probeSocket = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
    final port = probeSocket.port;
    await probeSocket.close();
    _baseUrl = 'http://127.0.0.1:$port';

    await _spawn(port);
    await _bootstrap();
  }

  /// The tenant's app_user and the session token every helper uses. A 409 is
  /// not an error: a backend that outlives the fixture (the lab runs one on the
  /// host for the whole suite, see AGENTS.md "Android validation") already has
  /// the user, and the login below is what this needs.
  Future<void> _bootstrap() async {
    try {
      await _postJson('/app-users', {'email': 'e2e-test@example.com', 'password': 'test-password-123'});
    } on StateError catch (e) {
      if (!e.toString().contains('-> 409')) rethrow;
    }
    final login = await _postJson(
      '/sessions',
      {'email': 'e2e-test@example.com', 'password': 'test-password-123'},
    );
    _sessionToken = login['token'] as String;
  }

  Future<void> _spawn(int port) async {
    final process = _process = await Process.start(
      _binaryPath!,
      const [],
      workingDirectory: _tempDir!.path,
      environment: {
        'PLAYHEADTV_DB_PATH': _dbPath!,
        'PLAYHEADTV_ADDR': ':$port',
        ..._extraEnv,
      },
      includeParentEnvironment: true,
    );
    process.stdout.transform(utf8.decoder).listen((l) => stdout.write('[backend] $l'));
    process.stderr.transform(utf8.decoder).listen((l) => stderr.write('[backend] $l'));
    await _waitForHealth();
  }

  Future<void> _waitForHealth() async {
    final deadline = DateTime.now().add(const Duration(seconds: 30));
    while (true) {
      try {
        final resp = await _client.get(Uri.parse('$_baseUrl/healthz')).timeout(const Duration(seconds: 1));
        if (resp.statusCode == 200) break;
      } catch (_) {
        // backend still starting — try again.
      }
      if (DateTime.now().isAfter(deadline)) {
        throw StateError('backend did not answer /healthz at $_baseUrl in time (30s)');
      }
      await Future.delayed(const Duration(milliseconds: 200));
    }
  }

  /// Whether the backend is up (false between [stopServer] and
  /// [restartServer]).
  bool get isRunning => _remoteBaseUrl.isNotEmpty || _process != null;

  /// Takes the backend process down but keeps its data and address: from now
  /// on every request to [baseUrl] is refused, which is how a test simulates
  /// the backend (or the internet) being out. [restartServer] brings it back.
  Future<void> stopServer() async {
    if (_remoteBaseUrl.isNotEmpty) {
      throw UnsupportedError(
        'stopServer() needs the backend next to the test, and on a device the backend is on the host: '
        'this suite runs on the desktop (client/tool/run_gui_suites.sh), not on an Android target yet.',
      );
    }
    final process = _process;
    _process = null;
    if (process == null) return;
    process.kill(ProcessSignal.sigterm);
    try {
      await process.exitCode.timeout(const Duration(seconds: 5));
    } on TimeoutException {
      process.kill(ProcessSignal.sigkill);
      await process.exitCode;
    }
  }

  /// Starts the backend again on the same address with the same database (the
  /// schedule is re-materialized at boot) and logs in again.
  Future<void> restartServer({Map<String, String>? env}) async {
    if (_remoteBaseUrl.isNotEmpty) {
      throw UnsupportedError(
        'restartServer() needs the backend next to the test, and on a device the backend is on the host: '
        'this suite runs on the desktop (client/tool/run_gui_suites.sh), not on an Android target yet.',
      );
    }
    if (env != null) _extraEnv = env;
    await _spawn(Uri.parse(_baseUrl!).port);
    final login = await _postJson('/sessions', {'email': 'e2e-test@example.com', 'password': 'test-password-123'}, auth: false);
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

  /// How far this process's clock is from the backend's, learned from the `Date`
  /// header of every response.
  ///
  /// A device clock drifts — an emulator under load ran 49 s behind the host
  /// after eleven minutes — and the fixture computes epoch millis that the
  /// backend interprets with *its* clock while materialising with *its* clock.
  /// With a skewed client, "start two seconds ago" lands 49 s in the past, the
  /// items that came and went never air, and a suite waiting for twelve entries
  /// waits for eight that will never exist. Every epoch this fixture sends is
  /// therefore anchored on the server's clock.
  Duration _serverOffset = Duration.zero;

  DateTime get serverNow => DateTime.now().add(_serverOffset);

  void _learnClock(http.Response resp) {
    final date = resp.headers['date'];
    if (date == null) return;
    try {
      _serverOffset = HttpDate.parse(date).difference(DateTime.now());
    } catch (_) {
      // A server without a parseable Date: keep the local clock.
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
    _learnClock(resp);
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
    _learnClock(resp);
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
    _learnClock(resp);
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
    // list_mode once: the items play a single time (the server default repeats the list to the end of the window).
    final blockResp = await _postJson('/blocks', {'name': 'Block ${DateTime.now().microsecondsSinceEpoch}', 'list_mode': 'once'});
    final blockId = blockResp['id'] as String;
    for (final catalogItemId in catalogItemIds) {
      await _postJson('/blocks/$blockId/items', {'catalog_item_id': catalogItemId});
    }
    return blockId;
  }

  /// Creates a Schedule + a single `once` ScheduleItem inside it, then
  /// assigns it to the channel. A thin wrapper over createScheduleWithItems
  /// below for the common single-occurrence case most suites need.
  Future<String> createScheduleOnce({
    required String channelId,
    required String blockId,
    required DateTime startAt,
  }) async =>
      (await createScheduleWithItems(channelId: channelId, items: [(blockId: blockId, startAt: startAt)])).single;

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
  ///
  /// With [asBase] the schedule is assigned as the channel's base schedule
  /// (`base_schedule_id`) instead of its pinned one; the other of the two is
  /// kept as it is.
  Future<List<String>> createScheduleWithItems({
    required String channelId,
    required List<({String blockId, DateTime startAt})> items,
    bool asBase = false,
  }) async {
    final scheduleResp = await _postJson('/schedules', {
      'name': 'Schedule ${DateTime.now().microsecondsSinceEpoch}',
    });
    final scheduleId = scheduleResp['id'] as String;
    final created = <String>[];
    for (final item in items) {
      final resp = await _postJson('/schedules/$scheduleId/items', {
        'block_id': item.blockId,
        'recurrence_type': 'once',
        'start_at': item.startAt.toUtc().millisecondsSinceEpoch,
      });
      created.add(resp['id'] as String);
    }
    final ch = await _getJson('/channels/$channelId') as Map<String, dynamic>;
    await _putJson('/channels/$channelId', {
      'number': ch['number'],
      'name': ch['name'],
      'guide_art_url': ch['guide_art_url'],
      'timezone': ch['timezone'],
      'schedule_id': asBase ? ch['schedule_id'] : scheduleId,
      'base_schedule_id': asBase ? scheduleId : ch['base_schedule_id'],
    });
    return created;
  }

  Future<Map<String, dynamic>> scheduleItem(String id) async =>
      await _getJson('/schedule-items/$id') as Map<String, dynamic>;

  /// Moves a one-off item. Used by [seedChannel] to recover a grid that landed
  /// in the past because the write itself took the time.
  Future<void> moveScheduleItem(String id, DateTime startAt) async {
    final item = await scheduleItem(id);
    await _putJson('/schedule-items/$id', {
      'block_id': item['block_id'],
      'recurrence_type': item['recurrence_type'],
      'start_at': startAt.toUtc().millisecondsSinceEpoch,
    });
  }

  /// A channel-scoped TAIL filler rule drawing sequentially from a new pool
  /// of [catalogItemIds]: after a program, the gap up to the next scheduled
  /// occurrence is padded with these clips, one `tail` epg_entry each.
  Future<void> createTailRule({required String channelId, required List<String> catalogItemIds}) =>
      createFillerRule(channelId: channelId, kind: 'tail', catalogItemIds: catalogItemIds);

  /// A channel-scoped filler rule of [kind] (`preroll`, `midroll`, `postroll`,
  /// `tail` or `fallback`) drawing sequentially from a new pool of
  /// [catalogItemIds].
  Future<void> createFillerRule({
    required String channelId,
    required String kind,
    required List<String> catalogItemIds,
  }) async {
    final pool = await _postJson('/filler-pools', {'name': '$kind pool ${DateTime.now().microsecondsSinceEpoch}'});
    final poolId = pool['id'] as String;
    for (final id in catalogItemIds) {
      await _postJson('/filler-pools/$poolId/items', {'catalog_item_id': id, 'weight': 1});
    }
    await _postJson('/filler-rules', {
      'scope_type': 'channel',
      'scope_id': channelId,
      'kind': kind,
      'filler_pool_id': poolId,
      'selection_strategy': 'sequential',
      'position': 0,
    });
  }

  /// A channel-scoped cut rule set that cuts every item at [offsetsMs] (the
  /// guards are shrunk to 1 ms so a short fixture clip is cut at all), with the
  /// channel's midroll between the segments.
  Future<void> createCutOffsetsRule({required String channelId, required List<int> offsetsMs}) async {
    await _postJson('/cut-rule-sets', {
      'scope_type': 'channel',
      'scope_id': channelId,
      'rules': [
        {
          'strategy': {'type': 'offsets', 'offsets_ms': offsetsMs},
        },
      ],
      'guards': {'min_segment_ms': 1, 'min_from_start_ms': 1, 'min_from_end_ms': 1},
    });
  }

  /// Polls GET /epg until the channel has at least [minEntries] materialized
  /// entries (materialization is asynchronous after a write), so a test boots
  /// the app against a settled guide.
  Future<void> waitForEpgEntries(String channelId, int minEntries, {Duration timeout = const Duration(seconds: 60)}) async {
    final deadline = DateTime.now().add(timeout);
    var count = 0;
    var stableSince = DateTime.now();
    while (DateTime.now().isBefore(deadline)) {
      final now = serverNow.toUtc().millisecondsSinceEpoch;
      final data = await _getJson('/epg?channel_id=$channelId&start=${now - 60000}&end=${now + 7200000}') as List;
      if (data.length != count) {
        count = data.length;
        stableSince = DateTime.now();
      }
      // Wait for the count to stop moving, not just to cross the threshold.
      if (count >= minEntries && DateTime.now().difference(stableSince) > const Duration(seconds: 1)) return;
      await Future.delayed(const Duration(milliseconds: 300));
    }
    throw StateError('channel $channelId never reached $minEntries epg entries (last: $count)');
  }

  /// GET /epg for one channel, the raw enriched entries (JSON objects) from
  /// [fromMs] on.
  Future<List<Map<String, dynamic>>> getEpg(String channelId, {required int fromMs}) async {
    final data = await _getJson('/epg?channel_id=$channelId&start=$fromMs&end=${fromMs + 7200000}') as List;
    return data.cast<Map<String, dynamic>>();
  }

  /// PUT /{channels|blocks}/{id}/watermark: [ownerType] is `channel` or
  /// `block`, [mode] `inherit`, `disable` or `override`; [png] (base64 inside
  /// the body) is the image, kept as it is when null. Returns the stored
  /// settings (`image_id` among them).
  Future<Map<String, dynamic>> setWatermark({
    required String ownerType,
    required String ownerId,
    required String mode,
    List<int>? png,
    String? position,
    int? opacityPct,
    int? sizePct,
  }) =>
      _putJson('/${ownerType}s/$ownerId/watermark', {
        'mode': mode,
        if (png != null) 'image_base64': base64Encode(png),
        'position': ?position,
        'opacity_pct': ?opacityPct,
        'size_pct': ?sizePct,
      });

  /// PUT /channels/{id}/logo: uploads the channel's logo (base64 in the body,
  /// same shape as the watermark). Returns the stored metadata (`image_id`).
  Future<Map<String, dynamic>> setChannelLogo({required String channelId, required List<int> png}) =>
      _putJson('/channels/$channelId/logo', {'image_base64': base64Encode(png)});

  /// POST /channels/{id}/reset-epg: the admin's grid reset of one channel.
  Future<void> resetChannelEpg(String channelId) async {
    await _postJson('/channels/$channelId/reset-epg', {});
  }

  Future<String> createDeviceToken({String name = 'E2E test client', String platform = 'linux'}) async {
    final resp = await _postJson('/devices', {'name': name, 'platform': platform});
    return resp['token'] as String;
  }
}
