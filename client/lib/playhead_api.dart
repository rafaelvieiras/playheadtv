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

/// One stored tail clip inside a grouped tail [EpgEntry] — everything a
/// player needs to play it with no further request. Mirrors the `clips` of
/// `GET /epg?group_tail=1` (backend/internal/api/epg_view.go).
class EpgClip {
  final String id; // the epg_entry id
  final String catalogItemId;
  final String? externalId; // optional: absent on a backend that predates the enriched /epg
  final String? title;
  final DateTime startAt;
  final DateTime endAt;
  final int? durationMs;

  const EpgClip({
    required this.id,
    required this.catalogItemId,
    required this.startAt,
    required this.endAt,
    this.externalId,
    this.title,
    this.durationMs,
  });

  factory EpgClip.fromJson(Map<String, dynamic> json) => EpgClip(
        id: json['id'] as String,
        catalogItemId: json['catalog_item_id'] as String,
        externalId: json['external_id'] as String?,
        title: json['title'] as String?,
        startAt: DateTime.fromMillisecondsSinceEpoch(json['start_at'] as int, isUtc: true),
        endAt: DateTime.fromMillisecondsSinceEpoch(json['end_at'] as int, isUtc: true),
        durationMs: json['duration_ms'] as int?,
      );

  EpgClip withCatalog(CatalogItemInfo info) => EpgClip(
        id: id,
        catalogItemId: catalogItemId,
        startAt: startAt,
        endAt: endAt,
        externalId: info.externalId,
        title: info.title,
        durationMs: durationMs,
      );
}

/// One program of a channel's guide. Programs and fillers are the same
/// concept: [kind] says which it is (program, preroll, midroll, postroll,
/// tail, fallback), nothing else about handling differs.
///
/// Required fields are the ones the client has always used to play
/// (id/kind/catalog_item_id/start_at/end_at). Every other field is optional:
/// it is only there when the backend sends the enriched `GET /epg`, and a
/// missing one makes the client fall back to a per-item catalog lookup
/// ([PlayheadApi.resolveCatalog]).
class EpgEntry {
  final String id;
  final String kind;
  final String catalogItemId;
  final DateTime startAt; // UTC, from epoch millis
  final DateTime endAt; // UTC, from epoch millis

  // Optional enrichment (catalog metadata). [externalId] is the media
  // server's own item id, what the stream and image URLs are built from.
  final String? externalId;
  final String? title;
  final String? seriesTitle;
  final String? seriesExternalId;
  final String? itemKind; // catalog kind (movie, episode, ...), not [kind]
  final int? seasonNumber;
  final int? episodeNumber;
  final int? episodeNumberEnd;
  final int? durationMs; // the item's own length; [endAt] - [startAt] is what is scheduled
  final String? imagePrimaryTag;
  final String? imageBackdropTag;
  final String? imageThumbTag;
  final String? seriesImagePrimaryTag;

  /// Only for a grouped tail (`kind == 'tail'` with clips): the individual
  /// clips, in playing order. [catalogItemId] is then the first clip's.
  final List<EpgClip> clips;

  const EpgEntry({
    required this.id,
    required this.kind,
    required this.catalogItemId,
    required this.startAt,
    required this.endAt,
    this.externalId,
    this.title,
    this.seriesTitle,
    this.seriesExternalId,
    this.itemKind,
    this.seasonNumber,
    this.episodeNumber,
    this.episodeNumberEnd,
    this.durationMs,
    this.imagePrimaryTag,
    this.imageBackdropTag,
    this.imageThumbTag,
    this.seriesImagePrimaryTag,
    this.clips = const [],
  });

  factory EpgEntry.fromJson(Map<String, dynamic> json) {
    final rawClips = json['clips'] as List<dynamic>?;
    final clips = rawClips == null ? const <EpgClip>[] : [for (final c in rawClips) EpgClip.fromJson(c as Map<String, dynamic>)];
    return EpgEntry(
      id: json['id'] as String,
      kind: json['kind'] as String,
      // A grouped tail has no catalog_item_id of its own.
      catalogItemId: (json['catalog_item_id'] as String?) ?? clips.first.catalogItemId,
      startAt: DateTime.fromMillisecondsSinceEpoch(json['start_at'] as int, isUtc: true),
      endAt: DateTime.fromMillisecondsSinceEpoch(json['end_at'] as int, isUtc: true),
      externalId: json['external_id'] as String?,
      title: json['title'] as String?,
      seriesTitle: json['series_title'] as String?,
      seriesExternalId: json['series_external_id'] as String?,
      itemKind: json['item_kind'] as String?,
      seasonNumber: json['season_number'] as int?,
      episodeNumber: json['episode_number'] as int?,
      episodeNumberEnd: json['episode_number_end'] as int?,
      durationMs: json['duration_ms'] as int?,
      imagePrimaryTag: json['image_primary_tag'] as String?,
      imageBackdropTag: json['image_backdrop_tag'] as String?,
      imageThumbTag: json['image_thumb_tag'] as String?,
      seriesImagePrimaryTag: json['series_image_primary_tag'] as String?,
      clips: clips,
    );
  }

  bool get isTailGroup => clips.isNotEmpty;

  /// The media server item and image tag to build this entry's poster URL
  /// from: its own primary image, else (an episode without one) its series'.
  /// Null when the backend sent no art references.
  ({String itemId, String tag})? get primaryArt {
    final ext = externalId;
    final own = imagePrimaryTag;
    if (ext != null && own != null) return (itemId: ext, tag: own);
    final series = seriesExternalId;
    final seriesTag = seriesImagePrimaryTag;
    if (series != null && seriesTag != null) return (itemId: series, tag: seriesTag);
    return null;
  }

  /// True once everything needed to play and label this entry is known
  /// without asking the backend again.
  bool get isResolved => isTailGroup ? clips.every((c) => c.externalId != null) : externalId != null;

  /// What to show for it: "Series · S1E2 · Title" for an episode that carries
  /// series data, the plain title otherwise.
  String get displayTitle {
    final t = title ?? '';
    final series = seriesTitle;
    if (isTailGroup || series == null || series.isEmpty) return t;
    final season = seasonNumber;
    final ep = episodeNumber;
    final epEnd = episodeNumberEnd;
    final code = StringBuffer();
    if (season != null) code.write('S$season');
    if (ep != null) {
      code.write('E$ep');
      if (epEnd != null && epEnd != ep) code.write('-E$epEnd');
    }
    return [series, if (code.isNotEmpty) code.toString(), if (t.isNotEmpty) t].join(' · ');
  }

  EpgEntry _copy({String? externalId, String? title, List<EpgClip>? clips}) => EpgEntry(
        id: id,
        kind: kind,
        catalogItemId: catalogItemId,
        startAt: startAt,
        endAt: endAt,
        externalId: externalId ?? this.externalId,
        title: title ?? this.title,
        seriesTitle: seriesTitle,
        seriesExternalId: seriesExternalId,
        itemKind: itemKind,
        seasonNumber: seasonNumber,
        episodeNumber: episodeNumber,
        episodeNumberEnd: episodeNumberEnd,
        durationMs: durationMs,
        imagePrimaryTag: imagePrimaryTag,
        imageBackdropTag: imageBackdropTag,
        imageThumbTag: imageThumbTag,
        seriesImagePrimaryTag: seriesImagePrimaryTag,
        clips: clips ?? this.clips,
      );

  EpgEntry withCatalog(CatalogItemInfo info) => _copy(externalId: info.externalId, title: info.title);

  EpgEntry withClips(List<EpgClip> newClips) => _copy(clips: newClips);
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

  // Only the fallback against a backend without the enriched GET /epg fans
  // out one request per EPG entry (a channel of short items can mean
  // hundreds), and every in-flight request holds a socket: unbounded, that
  // exhausts the process's file descriptors ("Too many open files"). Kept so
  // that fallback stays safe.
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

  /// One request returns the window already enriched with titles, source ids
  /// and art references. [groupTail] asks the backend to group consecutive
  /// tail clips into one entry carrying them (`clips`); a backend that does
  /// not know the parameter simply returns one flat entry per clip, which
  /// callers handle like any other program.
  Future<List<EpgEntry>> getEpg(String channelId, DateTime start, DateTime end, {bool groupTail = false}) async {
    final uri = Uri.parse('$_normalizedBaseUrl/epg').replace(queryParameters: {
      'channel_id': channelId,
      'start': start.toUtc().millisecondsSinceEpoch.toString(),
      'end': end.toUtc().millisecondsSinceEpoch.toString(),
      if (groupTail) 'group_tail': '1',
    });
    final data = await _getJson(uri);
    if (data is! List) {
      throw Exception('GET /epg: expected a list, got ${data.runtimeType}');
    }
    final entries = data.map((e) => EpgEntry.fromJson(e as Map<String, dynamic>)).toList();
    entries.sort((a, b) => a.startAt.compareTo(b.startAt));
    return entries;
  }

  /// Makes [entry] playable and labelled. With the enriched `GET /epg` this
  /// is a no-op (nothing is requested); against an older backend the missing
  /// source id / title is fetched per catalog item (bounded concurrency,
  /// cached per id), exactly as before the enrichment existed.
  Future<EpgEntry> resolveCatalog(EpgEntry entry) async {
    if (entry.isResolved) return entry;
    if (entry.isTailGroup) {
      final clips = await Future.wait(entry.clips.map((c) async {
        if (c.externalId != null) return c;
        return c.withCatalog(await getCatalogItem(c.catalogItemId));
      }));
      return entry.withClips(clips);
    }
    return entry.withCatalog(await getCatalogItem(entry.catalogItemId));
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
