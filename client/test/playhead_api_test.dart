// Integration test of playhead_api.dart against the REAL Go backend (not a
// mock) — starts `go run ./cmd/server` on a free port with a temporary
// SQLite, populates channel/media-source/catalog-item/block/schedule-entry
// via HTTP, and checks that PlayheadApi.listChannels()/getEpg()/getCatalogItem()
// parse the real responses correctly.
//
// It doesn't need Jellyfin: the backend never touches video, only metadata
// (see AGENTS.md/docs/ARCHITECTURE.md), so everything here is pure HTTP+SQLite.
//
// The backend requires authentication — this test goes through the real flow
// (app_user bootstrap, login, device creation) and exercises PlayheadApi
// exactly as main.dart uses it: authenticated by device token via
// PLAYHEADTV_DEVICE_TOKEN.
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;

void main() {
  late Directory tempDir;
  late Process serverProcess;
  late String baseUrl;
  final httpClient = http.Client();

  setUpAll(() async {
    final backendDir = Directory('${Directory.current.path}/../backend');
    if (!await backendDir.exists()) {
      fail(
        'backend/ not found at ${backendDir.path} — this test expects to run '
        'with `flutter test` from inside client/, with backend/ next to it.',
      );
    }

    tempDir = await Directory.systemTemp.createTemp('playheadtv_client_test_');
    final dbPath = '${tempDir.path}/playheadtv_test.db';

    // Ask the OS for a free port (bind on port 0), read the assigned port and
    // release it before starting the Go server. Small race window, but
    // acceptable for this local test only.
    final probeSocket = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
    final port = probeSocket.port;
    await probeSocket.close();
    baseUrl = 'http://127.0.0.1:$port';

    serverProcess = await Process.start(
      'go',
      ['run', './cmd/server'],
      workingDirectory: backendDir.path,
      environment: {
        'PLAYHEADTV_DB_PATH': dbPath,
        'PLAYHEADTV_ADDR': ':$port',
      },
      includeParentEnvironment: true,
    );
    serverProcess.stdout.transform(utf8.decoder).listen((l) => stdout.write('[backend] $l'));
    serverProcess.stderr.transform(utf8.decoder).listen((l) => stderr.write('[backend] $l'));

    final deadline = DateTime.now().add(const Duration(seconds: 30));
    while (true) {
      try {
        final resp = await httpClient.get(Uri.parse('$baseUrl/healthz')).timeout(const Duration(seconds: 1));
        if (resp.statusCode == 200) break;
      } catch (_) {
        // backend still starting (or compiling, on the first run) — try again.
      }
      if (DateTime.now().isAfter(deadline)) {
        fail('backend did not answer /healthz at $baseUrl in time (30s)');
      }
      await Future.delayed(const Duration(milliseconds: 200));
    }
  });

  tearDownAll(() async {
    serverProcess.kill(ProcessSignal.sigterm);
    try {
      await serverProcess.exitCode.timeout(const Duration(seconds: 5));
    } on TimeoutException {
      serverProcess.kill(ProcessSignal.sigkill);
    }
    httpClient.close();
    try {
      await tempDir.delete(recursive: true);
    } catch (_) {
      // best-effort — don't let the test fail because of cleanup.
    }
  });

  Future<Map<String, dynamic>> postJson(String path, Map<String, dynamic> body, {String? token}) async {
    final resp = await httpClient.post(
      Uri.parse('$baseUrl$path'),
      headers: {
        'Content-Type': 'application/json',
        if (token != null) 'Authorization': 'Bearer $token',
      },
      body: jsonEncode(body),
    );
    expect(resp.statusCode, anyOf(200, 201), reason: 'POST $path -> ${resp.statusCode}: ${resp.body}');
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  Future<Map<String, dynamic>> putJson(String path, Map<String, dynamic> body, {String? token}) async {
    final resp = await httpClient.put(
      Uri.parse('$baseUrl$path'),
      headers: {
        'Content-Type': 'application/json',
        if (token != null) 'Authorization': 'Bearer $token',
      },
      body: jsonEncode(body),
    );
    expect(resp.statusCode, 200, reason: 'PUT $path -> ${resp.statusCode}: ${resp.body}');
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  test('PlayheadApi reads real channels/epg/catalog-items from the backend', () async {
    // --- bootstrap: create the tenant's single app_user and log in to get a session token ---
    await postJson('/app-users', {'email': 'e2e-test@example.com', 'password': 'test-password-123'});
    final login = await postJson('/sessions', {'email': 'e2e-test@example.com', 'password': 'test-password-123'});
    final sessionToken = login['token'] as String;

    // --- populate channel, media_source (local_folder avoids depending on network/Jellyfin), catalog_item, block, schedule_entry ---
    final channelResp = await postJson(
      '/channels',
      {'number': 1, 'name': 'Test Channel', 'timezone': 'UTC'},
      token: sessionToken,
    );
    final channelId = channelResp['id'] as String;

    final mediaSourceResp = await postJson(
      '/media-sources',
      {'type': 'local_folder', 'name': 'Test Local Source'},
      token: sessionToken,
    );
    final mediaSourceId = mediaSourceResp['id'] as String;

    await postJson(
      '/media-sources/$mediaSourceId/catalog-sync',
      {
        'items': [
          {'external_id': 'jf-item-a', 'kind': 'episode', 'title': 'PROGRAM A', 'duration_ms': 20000},
          {'external_id': 'jf-item-b', 'kind': 'other', 'title': 'COMMERCIAL', 'duration_ms': 10000},
        ],
      },
      token: sessionToken,
    );

    final itemsResp = await httpClient.get(
      Uri.parse('$baseUrl/catalog-items?media_source_id=$mediaSourceId'),
      headers: {'Authorization': 'Bearer $sessionToken'},
    );
    expect(itemsResp.statusCode, 200, reason: itemsResp.body);
    final catalogItems = (jsonDecode(itemsResp.body) as List).cast<Map<String, dynamic>>();
    final itemAId = catalogItems.firstWhere((i) => i['external_id'] == 'jf-item-a')['id'] as String;
    final itemBId = catalogItems.firstWhere((i) => i['external_id'] == 'jf-item-b')['id'] as String;

    final blockResp = await postJson('/blocks', {'name': 'Test Block', 'list_mode': 'once'}, token: sessionToken);
    final blockId = blockResp['id'] as String;
    await postJson('/blocks/$blockId/items', {'catalog_item_id': itemAId}, token: sessionToken);
    await postJson('/blocks/$blockId/items', {'catalog_item_id': itemBId}, token: sessionToken);

    // Started 10s ago: item A (20s) is still "on air" — a real scenario that
    // main.dart has to handle (seek into the middle of the first item).
    //
    // Issue #7's Schedule/Playout split: schedule_entry became Schedule +
    // ScheduleItem, decoupled from Channel (see docs/ARCHITECTURE.md) — a
    // Schedule is created, a ScheduleItem added to it, then assigned to the
    // channel via PUT /channels/{id}, which is a full replace (channels.go),
    // hence repeating the number/name/timezone already used above.
    final now = DateTime.now().toUtc();
    final startAt = now.subtract(const Duration(seconds: 10));
    final scheduleResp = await postJson('/schedules', {'name': 'Test Schedule'}, token: sessionToken);
    final scheduleId = scheduleResp['id'] as String;
    await postJson(
      '/schedules/$scheduleId/items',
      {
        'block_id': blockId,
        'recurrence_type': 'once',
        'start_at': startAt.millisecondsSinceEpoch,
      },
      token: sessionToken,
    );
    await putJson(
      '/channels/$channelId',
      {'number': 1, 'name': 'Test Channel', 'timezone': 'UTC', 'schedule_id': scheduleId},
      token: sessionToken,
    );

    // Device token — the same authentication path that PLAYHEADTV_DEVICE_TOKEN
    // uses in main.dart/playhead_api.dart.
    final deviceResp = await postJson(
      '/devices',
      {'name': 'Test client', 'platform': 'linux'},
      token: sessionToken,
    );
    final deviceToken = deviceResp['token'] as String;

    // --- PlayheadApi sob teste ---
    final api = PlayheadApi(baseUrl: baseUrl, deviceToken: deviceToken);

    final channels = await api.listChannels();
    expect(channels, isNotEmpty);
    final testChannel = channels.firstWhere((c) => c.id == channelId);
    expect(testChannel.name, 'Test Channel');
    expect(testChannel.number, 1);

    // The EPG materializer runs asynchronously (200ms debounce + a background
    // Run) — wait until epg_entry shows up, with a timeout.
    var entries = <EpgEntry>[];
    final epgDeadline = DateTime.now().add(const Duration(seconds: 15));
    while (entries.isEmpty) {
      entries = await api.getEpg(
        channelId,
        now.subtract(const Duration(minutes: 1)),
        now.add(const Duration(hours: 6)),
      );
      if (entries.isNotEmpty) break;
      if (DateTime.now().isAfter(epgDeadline)) {
        fail('GET /epg did not materialize any entry within 15s');
      }
      await Future.delayed(const Duration(milliseconds: 300));
    }

    expect(entries.length, 2);
    expect(entries[0].catalogItemId, itemAId);
    expect(entries[0].kind, 'program');
    expect(entries[0].endAt.difference(entries[0].startAt), const Duration(seconds: 20));
    expect(entries[1].catalogItemId, itemBId);
    expect(entries[1].endAt.difference(entries[1].startAt), const Duration(seconds: 10));
    // Item A started ~10s ago — checks that start_at came through as an
    // instant in the recent past, not zeroed/corrupted in the epoch-millis
    // parsing.
    expect(now.difference(entries[0].startAt).inSeconds, greaterThanOrEqualTo(9));
    expect(now.difference(entries[0].startAt).inSeconds, lessThan(20));

    final catalogA = await api.getCatalogItem(entries[0].catalogItemId);
    expect(catalogA.externalId, 'jf-item-a');
    expect(catalogA.title, 'PROGRAM A');

    final catalogB = await api.getCatalogItem(entries[1].catalogItemId);
    expect(catalogB.externalId, 'jf-item-b');
    expect(catalogB.title, 'COMMERCIAL');
  });

  test('PlayheadApi throws a clear Exception when the request is unauthorized', () async {
    // Without a device/session token: GET /catalog-items/{id} requires
    // authentication. It serves equally well to prove that any status != 200
    // becomes an Exception, not a crash/silent parse.
    final api = PlayheadApi(baseUrl: baseUrl);
    await expectLater(api.getCatalogItem('any-id'), throwsA(isA<Exception>()));
  });
}
