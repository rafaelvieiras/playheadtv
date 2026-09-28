// Simple HTTP client for the Playhead TV backend (Go). No code generation
// (json_serializable/freezed) on purpose — manual parsing of
// Map<String, dynamic> is enough for the size of the contract today and keeps
// the lean style of the rest of the project (see AGENTS.md).
//
// Network/HTTP/parsing errors become plain Exceptions: the caller (main.dart)
// decides how that turns into a status message for the user.
import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

class PlayheadChannel {
  final String id;
  final int number;
  final String name;
  final String? guideArtUrl;

  PlayheadChannel({required this.id, required this.number, required this.name, this.guideArtUrl});

  factory PlayheadChannel.fromJson(Map<String, dynamic> json) => PlayheadChannel(
        id: json['id'] as String,
        number: json['number'] as int,
        name: json['name'] as String,
        guideArtUrl: json['guide_art_url'] as String?,
      );
}

class EpgEntry {
  final String id;
  final String kind;
  final String catalogItemId;
  final DateTime startAt; // UTC, from epoch millis
  final DateTime endAt; // UTC, from epoch millis

  EpgEntry({
    required this.id,
    required this.kind,
    required this.catalogItemId,
    required this.startAt,
    required this.endAt,
  });

  factory EpgEntry.fromJson(Map<String, dynamic> json) => EpgEntry(
        id: json['id'] as String,
        kind: json['kind'] as String,
        catalogItemId: json['catalog_item_id'] as String,
        startAt: DateTime.fromMillisecondsSinceEpoch(json['start_at'] as int, isUtc: true),
        endAt: DateTime.fromMillisecondsSinceEpoch(json['end_at'] as int, isUtc: true),
      );
}

class CatalogItemInfo {
  final String id;
  final String externalId; // Native ID of the item in the source (currently always a Jellyfin item)
  final String title;

  CatalogItemInfo({required this.id, required this.externalId, required this.title});

  factory CatalogItemInfo.fromJson(Map<String, dynamic> json) => CatalogItemInfo(
        id: json['id'] as String,
        externalId: json['external_id'] as String,
        title: json['title'] as String,
      );
}

/// Client for the Playhead TV backend. Authentication is optional — see
/// [deviceToken] for the hook that sends a device token as a Bearer
/// Authorization header when the backend requires one.
class PlayheadApi {
  final String baseUrl;
  final String? deviceToken;
  final http.Client _client;
  final Duration timeout;

  PlayheadApi({
    required this.baseUrl,
    this.deviceToken,
    http.Client? client,
    this.timeout = const Duration(seconds: 10),
  }) : _client = client ?? http.Client();

  String get _normalizedBaseUrl => baseUrl.endsWith('/') ? baseUrl.substring(0, baseUrl.length - 1) : baseUrl;

  Map<String, String> get _headers {
    final token = deviceToken;
    if (token == null || token.isEmpty) return const {};
    return {'Authorization': 'Bearer $token'};
  }

  Future<dynamic> _getJson(Uri uri) async {
    final http.Response response;
    try {
      response = await _client.get(uri, headers: _headers).timeout(timeout);
    } catch (e) {
      throw Exception('failed to connect to $uri: $e');
    }
    if (response.statusCode != 200) {
      throw Exception('$uri responded ${response.statusCode}: ${response.body}');
    }
    try {
      return jsonDecode(response.body);
    } catch (e) {
      throw Exception('invalid response (malformed JSON) from $uri: $e');
    }
  }

  Future<List<PlayheadChannel>> listChannels() async {
    final data = await _getJson(Uri.parse('$_normalizedBaseUrl/channels'));
    if (data is! List) {
      throw Exception('GET /channels: expected a list, got ${data.runtimeType}');
    }
    return data.map((e) => PlayheadChannel.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<List<EpgEntry>> getEpg(String channelId, DateTime start, DateTime end) async {
    final uri = Uri.parse('$_normalizedBaseUrl/epg').replace(queryParameters: {
      'channel_id': channelId,
      'start': start.toUtc().millisecondsSinceEpoch.toString(),
      'end': end.toUtc().millisecondsSinceEpoch.toString(),
    });
    final data = await _getJson(uri);
    if (data is! List) {
      throw Exception('GET /epg: expected a list, got ${data.runtimeType}');
    }
    final entries = data.map((e) => EpgEntry.fromJson(e as Map<String, dynamic>)).toList();
    entries.sort((a, b) => a.startAt.compareTo(b.startAt));
    return entries;
  }

  Future<CatalogItemInfo> getCatalogItem(String id) async {
    final data = await _getJson(Uri.parse('$_normalizedBaseUrl/catalog-items/$id'));
    if (data is! Map<String, dynamic>) {
      throw Exception('GET /catalog-items/$id: expected an object, got ${data.runtimeType}');
    }
    return CatalogItemInfo.fromJson(data);
  }
}
