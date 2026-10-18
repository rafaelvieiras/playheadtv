// "Test connection" for the setup screen: one cheap request per server, each
// answered with a short sentence a person can act on. Pure Dart. Never throws,
// and no message repeats a key or token.
import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

enum ProbeStatus { ok, unreachable, unauthorized, unexpected }

class ProbeResult {
  final ProbeStatus status;
  final String message;
  const ProbeResult(this.status, this.message);

  bool get isOk => status == ProbeStatus.ok;
}

/// The media server: `GET /System/Info` with the API key in a header (an
/// invalid key is 401, which is what tells "wrong key" from "wrong address").
Future<ProbeResult> probeMediaServer(
  String baseUrl,
  String apiKey, {
  http.Client? client,
  Duration timeout = const Duration(seconds: 6),
}) {
  return _probe(
    Uri.parse('$baseUrl/System/Info'),
    headers: {'X-Emby-Token': apiKey},
    client: client,
    timeout: timeout,
    unauthorized: 'The media server rejected the API key.',
    describe: (body) {
      final json = _tryJson(body);
      final name = json is Map && json['ServerName'] is String ? json['ServerName'] as String : null;
      return name == null ? 'Media server reachable.' : 'Media server "$name" reachable.';
    },
  );
}

/// The Playhead TV backend: `GET /channels` with the device token when there is
/// one. Counting the channels tells an empty install from a wrong address.
Future<ProbeResult> probeBackend(
  String baseUrl,
  String deviceToken, {
  http.Client? client,
  Duration timeout = const Duration(seconds: 6),
}) {
  return _probe(
    Uri.parse('$baseUrl/channels'),
    headers: deviceToken.isEmpty ? const {} : {'Authorization': 'Bearer $deviceToken'},
    client: client,
    timeout: timeout,
    unauthorized: deviceToken.isEmpty
        ? 'The backend requires a device token.'
        : 'The backend rejected the device token.',
    describe: (body) {
      final json = _tryJson(body);
      final list = json is List ? json : (json is Map && json['channels'] is List ? json['channels'] as List : null);
      if (list == null) return 'Backend reachable.';
      return list.length == 1 ? 'Backend reachable, 1 channel.' : 'Backend reachable, ${list.length} channels.';
    },
  );
}

Object? _tryJson(String body) {
  try {
    return jsonDecode(body);
  } on FormatException {
    return null;
  }
}

Future<ProbeResult> _probe(
  Uri uri, {
  required Map<String, String> headers,
  required http.Client? client,
  required Duration timeout,
  required String unauthorized,
  required String Function(String body) describe,
}) async {
  final owned = client == null;
  final c = client ?? http.Client();
  try {
    final response = await c.get(uri, headers: headers).timeout(timeout);
    final code = response.statusCode;
    if (code == 200) return ProbeResult(ProbeStatus.ok, describe(response.body));
    if (code == 401 || code == 403) return ProbeResult(ProbeStatus.unauthorized, unauthorized);
    if (code == 404) {
      return const ProbeResult(ProbeStatus.unexpected, 'Reached a server, but it does not look like the right one (404).');
    }
    return ProbeResult(ProbeStatus.unexpected, 'The server answered with HTTP $code.');
  } on TimeoutException {
    return const ProbeResult(ProbeStatus.unreachable, 'No answer: check the address and that the device is on the same network.');
  } on Object {
    return const ProbeResult(ProbeStatus.unreachable, 'Could not connect: check the address and the network.');
  } finally {
    if (owned) c.close();
  }
}
