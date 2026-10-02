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

/// One ordered fallback step of a stream (audio/subtitle) selection cascade
/// — mirrors backend/internal/domain.StreamRuleStep. Audio+subtitle are
/// meant to be resolved together from the same step (see
/// services/stream_selection_resolver.dart).
class StreamRuleStep {
  final String audioLanguage; // wildcard, e.g. "pt*"; "" matches any language
  final String? subtitleLanguage; // null = no subtitle preference at this step
  final List<String> titleBlocklist;
  final List<String> titleAllowlist;

  const StreamRuleStep({
    required this.audioLanguage,
    required this.subtitleLanguage,
    required this.titleBlocklist,
    required this.titleAllowlist,
  });

  factory StreamRuleStep.fromJson(Map<String, dynamic> json) => StreamRuleStep(
        audioLanguage: json['audio_language'] as String? ?? '',
        subtitleLanguage: json['subtitle_language'] as String?,
        titleBlocklist: (json['title_blocklist'] as List<dynamic>? ?? const []).cast<String>(),
        titleAllowlist: (json['title_allowlist'] as List<dynamic>? ?? const []).cast<String>(),
      );
}

/// The ordered cascade itself — mirrors
/// backend/internal/domain.StreamRuleCascade. Returned (wrapped in a
/// `rules` field) by GET /stream-preferences/me and
/// GET /stream-preferences/config.
class StreamRuleCascade {
  final List<StreamRuleStep> steps;

  const StreamRuleCascade({required this.steps});

  static const empty = StreamRuleCascade(steps: []);

  factory StreamRuleCascade.fromJson(Map<String, dynamic> json) {
    final raw = json['steps'] as List<dynamic>?;
    return StreamRuleCascade(
      steps: raw == null ? const [] : raw.map((e) => StreamRuleStep.fromJson(e as Map<String, dynamic>)).toList(),
    );
  }
}

class StreamPreferencesMeta {
  final int revision;
  const StreamPreferencesMeta({required this.revision});

  factory StreamPreferencesMeta.fromJson(Map<String, dynamic> json) =>
      StreamPreferencesMeta(revision: json['revision'] as int? ?? 0);
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

  // Callers fan out one request per EPG entry (a channel of short items can
  // mean hundreds), and every in-flight request holds a socket: unbounded,
  // that exhausts the process's file descriptors ("Too many open files").
  static const maxConcurrentRequests = 6;
  int _inFlight = 0;
  final _waiting = <Completer<void>>[];
  // Catalog items are looked up repeatedly (same item across entries, across
  // channels, across EPG slices); one request per id per session is enough.
  final _catalogItems = <String, Future<CatalogItemInfo>>{};

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

  Future<void> _acquire() async {
    if (_inFlight < maxConcurrentRequests) {
      _inFlight++;
      return;
    }
    final slot = Completer<void>();
    _waiting.add(slot);
    await slot.future;
  }

  void _release() {
    if (_waiting.isEmpty) {
      _inFlight--;
    } else {
      // The slot passes straight to the next waiter.
      _waiting.removeAt(0).complete();
    }
  }

  Future<dynamic> _getJson(Uri uri) async {
    final http.Response response;
    await _acquire();
    try {
      response = await _client.get(uri, headers: _headers).timeout(timeout);
    } catch (e) {
      throw Exception('failed to connect to $uri: $e');
    } finally {
      _release();
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

  Future<CatalogItemInfo> getCatalogItem(String id) {
    final cached = _catalogItems[id];
    if (cached != null) return cached;
    final fetch = _fetchCatalogItem(id);
    _catalogItems[id] = fetch;
    // A failed lookup must not stay cached.
    fetch.catchError((Object _) {
      _catalogItems.remove(id);
      return Future<CatalogItemInfo>.error('unreachable');
    }).ignore();
    return fetch;
  }

  Future<CatalogItemInfo> _fetchCatalogItem(String id) async {
    final data = await _getJson(Uri.parse('$_normalizedBaseUrl/catalog-items/$id'));
    if (data is! Map<String, dynamic>) {
      throw Exception('GET /catalog-items/$id: expected an object, got ${data.runtimeType}');
    }
    return CatalogItemInfo.fromJson(data);
  }

  // --- Stream (audio/subtitle) selection preferences (tiers 2-4) ---
  //
  // These three endpoints are session-authenticated on the backend (see
  // backend/internal/api/stream_preferences.go), same tier as other
  // authoring endpoints (unlike /epg, /channels, /catalog-items/{id}, which
  // also accept a plain device token) — [deviceToken] above must hold a
  // real app_user session token (from POST /sessions) for these three calls
  // to succeed, not just a paired device token. See
  // services/stream_preferences_service.dart for how a fetch failure here
  // (e.g. a deployment only configured with a device token) degrades to an
  // empty cascade instead of disrupting playback.

  Future<StreamRuleCascade> getMyStreamPreference() async {
    final data = await _getJson(Uri.parse('$_normalizedBaseUrl/stream-preferences/me'));
    if (data is! Map<String, dynamic>) {
      throw Exception('GET /stream-preferences/me: expected an object, got ${data.runtimeType}');
    }
    return StreamRuleCascade.fromJson(data['rules'] as Map<String, dynamic>? ?? const {});
  }

  /// scopeType is "global" or "channel"; scopeId is required (and only
  /// meaningful) for "channel".
  Future<StreamRuleCascade> getStreamPreferenceConfig({required String scopeType, String? scopeId}) async {
    final uri = Uri.parse('$_normalizedBaseUrl/stream-preferences/config').replace(queryParameters: {
      'scope_type': scopeType,
      'scope_id': ?scopeId,
    });
    final data = await _getJson(uri);
    if (data is! Map<String, dynamic>) {
      throw Exception('GET /stream-preferences/config: expected an object, got ${data.runtimeType}');
    }
    return StreamRuleCascade.fromJson(data['rules'] as Map<String, dynamic>? ?? const {});
  }

  Future<StreamPreferencesMeta> getStreamPreferencesMeta() async {
    final data = await _getJson(Uri.parse('$_normalizedBaseUrl/stream-preferences/meta'));
    if (data is! Map<String, dynamic>) {
      throw Exception('GET /stream-preferences/meta: expected an object, got ${data.runtimeType}');
    }
    return StreamPreferencesMeta.fromJson(data);
  }
}
