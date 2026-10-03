// A tiny reverse proxy in front of the real backend, used by the integration
// tests that care about HOW the client talks to it:
//
//  - it records every request (method, path, query), so a test can assert how
//    many requests a channel load costs and which endpoints were hit;
//  - in `legacy` mode it behaves like a backend that predates the enriched
//    GET /epg: the `group_tail` parameter is dropped before forwarding and
//    every entry of the response is cut back to the original, required
//    fields — no titles, source ids, art references or tail groups.
//
// The video traffic goes to the fixture video server, never through here.
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:http/http.dart' as http;

class RecordedRequest {
  final String method;
  final String path;
  final Map<String, String> query;
  const RecordedRequest(this.method, this.path, this.query);

  @override
  String toString() => '$method $path${query.isEmpty ? '' : '?${Uri(queryParameters: query).query}'}';
}

class RequestProxy {
  final String backendBaseUrl;
  final bool legacy;
  final List<RecordedRequest> requests = [];

  HttpServer? _server;
  final http.Client _client = http.Client();

  // The fields GET /epg had before it was enriched.
  static const _legacyEpgFields = {
    'id',
    'tenant_id',
    'channel_id',
    'kind',
    'catalog_item_id',
    'start_at',
    'end_at',
    'source_schedule_item_id',
    'generated_at',
  };

  RequestProxy({required this.backendBaseUrl, this.legacy = false});

  String get baseUrl => 'http://127.0.0.1:${_server!.port}';

  Future<void> start() async {
    _server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    _server!.listen((req) => unawaited(_forward(req)));
  }

  Future<void> stop() async {
    await _server?.close(force: true);
    _client.close();
  }

  void clear() => requests.clear();

  /// Requests whose path starts with [pathPrefix] (any method).
  List<RecordedRequest> to(String pathPrefix) => requests.where((r) => r.path.startsWith(pathPrefix)).toList();

  Future<void> _forward(HttpRequest req) async {
    var query = Map<String, String>.from(req.uri.queryParameters);
    final isEpg = req.method == 'GET' && req.uri.path == '/epg';
    if (legacy && isEpg) query.remove('group_tail');
    requests.add(RecordedRequest(req.method, req.uri.path, Map.of(req.uri.queryParameters)));

    final target = Uri.parse(backendBaseUrl).replace(path: req.uri.path, queryParameters: query.isEmpty ? null : query);
    final body = await req.fold<List<int>>(<int>[], (acc, chunk) => acc..addAll(chunk));
    final outgoing = http.Request(req.method, target)..bodyBytes = body;
    for (final name in const ['authorization', 'content-type', 'if-none-match']) {
      final v = req.headers.value(name);
      if (v != null) outgoing.headers[name] = v;
    }
    try {
      final resp = await http.Response.fromStream(await _client.send(outgoing));
      var bytes = resp.bodyBytes;
      if (legacy && isEpg && resp.statusCode == 200) {
        final entries = jsonDecode(utf8.decode(bytes)) as List<dynamic>;
        bytes = utf8.encode(jsonEncode([
          for (final e in entries.cast<Map<String, dynamic>>())
            {for (final k in _legacyEpgFields) if (e.containsKey(k)) k: e[k]},
        ]));
      }
      req.response.statusCode = resp.statusCode;
      final ct = resp.headers['content-type'];
      if (ct != null) req.response.headers.set(HttpHeaders.contentTypeHeader, ct);
      req.response.add(bytes);
    } catch (e) {
      req.response.statusCode = HttpStatus.badGateway;
    }
    await req.response.close();
  }
}
