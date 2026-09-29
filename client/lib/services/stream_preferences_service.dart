// Caching/sync service for the stream (audio/subtitle) selection cascade
// (tiers 2-4 — see AGENTS.md "Client app structure" and issue #3). Mirrors
// SettingsService's shared_preferences persistence pattern: fetches tiers
// 2 (user profile) and 4 (global admin) once at boot/login, caches the
// decoded cascades + the revision they were fetched at, and polls
// GET /stream-preferences/meta periodically to decide whether to refetch.
// Tier 3 (channel admin) is fetched lazily per channel (there's no bulk
// "every channel's config" endpoint) and cached per channel id, invalidated
// wholesale whenever the tenant-wide revision changes.
//
// No network call ever happens from the playback hot path
// (_createController in main.dart) — every getter below reads whatever is
// already in shared_preferences, synchronously. Fetching happens here, off
// that path: refreshAll() at boot/login, the periodic poll, and
// ensureChannelRules() from the channel-load path (already async / already
// doing network I/O — see _loadAndPlayChannel in main.dart) ahead of the
// point where _createController needs the answer.
import 'dart:async';
import 'dart:convert';

import 'package:shared_preferences/shared_preferences.dart';

import '../playhead_api.dart';

class StreamPreferencesService {
  static const _userRulesKey = 'stream_pref_user_rules';
  static const _globalRulesKey = 'stream_pref_global_rules';
  static const _revisionKey = 'stream_pref_revision';
  static const _channelRulesPrefix = 'stream_pref_channel_rules_'; // + channelId
  static const _channelRulesIndexKey = 'stream_pref_channel_rules_index'; // list of cached channel ids

  // Stream preferences change rarely — an admin editing the global/channel
  // config, or the user editing their own profile from another device —
  // not every few seconds like live EPG data. Polling every 5 minutes keeps
  // the staleness window small enough that a change is picked up well
  // within a single viewing session, while staying "minutes, not seconds"
  // (battery/resource-light, per the issue's stated goal) — this is a
  // single lightweight GET (same shape as GET /epg/meta), on a timer, not a
  // meaningful resource cost either way.
  static const pollInterval = Duration(minutes: 5);

  final SharedPreferences _prefs;
  final PlayheadApi _api;
  Timer? _pollTimer;

  StreamPreferencesService(this._prefs, this._api);

  static Future<StreamPreferencesService> create(PlayheadApi api) async =>
      StreamPreferencesService(await SharedPreferences.getInstance(), api);

  StreamRuleCascade get userRules => _decode(_prefs.getString(_userRulesKey));
  StreamRuleCascade get globalRules => _decode(_prefs.getString(_globalRulesKey));

  StreamRuleCascade? cachedChannelRules(String channelId) {
    final raw = _prefs.getString('$_channelRulesPrefix$channelId');
    return raw == null ? null : _decode(raw);
  }

  int get cachedRevision => _prefs.getInt(_revisionKey) ?? 0;

  /// Initial fetch at boot/login: populates tiers 2 + 4 so the very first
  /// playback decision already has a cache to read, without waiting for the
  /// first poll tick. A failure here (e.g. this deployment's configured
  /// bearer token is a device token, not a session token — see the doc
  /// comment on PlayheadApi's stream-preference methods) is swallowed: an
  /// empty cache resolves to "no preference configured", which is exactly
  /// today's pre-cascade behavior (player default audio, no subtitle) —
  /// never something that should block or crash playback.
  Future<void> refreshAll() async {
    try {
      final results = await Future.wait([
        _api.getMyStreamPreference(),
        _api.getStreamPreferenceConfig(scopeType: 'global'),
        _api.getStreamPreferencesMeta(),
      ]);
      final userRules = results[0] as StreamRuleCascade;
      final globalRules = results[1] as StreamRuleCascade;
      final meta = results[2] as StreamPreferencesMeta;
      await _prefs.setString(_userRulesKey, _encode(userRules));
      await _prefs.setString(_globalRulesKey, _encode(globalRules));
      await _prefs.setInt(_revisionKey, meta.revision);
    } catch (_) {
      // Keep whatever was cached before (or the empty default) — see doc
      // comment above.
    }
  }

  /// Starts the periodic /meta poll. Call once, after an initial
  /// [refreshAll], for the life of the app.
  void startPolling() {
    _pollTimer?.cancel();
    _pollTimer = Timer.periodic(pollInterval, (_) => _pollIfStale());
  }

  void dispose() {
    _pollTimer?.cancel();
  }

  Future<void> _pollIfStale() async {
    try {
      final meta = await _api.getStreamPreferencesMeta();
      if (meta.revision == cachedRevision) return; // nothing changed since the last fetch
      await refreshAll();
      // The tenant-wide revision covers every tier-3/4 row, not just the
      // one(s) that actually changed (there's no per-channel revision) —
      // clear the whole per-channel cache so a stale channel's config isn't
      // kept indefinitely; ensureChannelRules() below repopulates it lazily,
      // next time that channel is loaded.
      await _clearChannelRulesCache();
    } catch (_) {
      // A background poll failure must not disrupt playback in progress —
      // same "silently retry next tick" reasoning as EPG's rolling-window
      // extension fetch in main.dart.
    }
  }

  /// Ensures tier-3 (channel admin) rules for [channelId] are cached,
  /// fetching them if missing. Called from the channel-load path
  /// (_loadAndPlayChannel in main.dart, itself already awaiting GET /epg +
  /// GET /catalog-items/{id} before playback starts) — never from
  /// _createController itself, which only ever reads [cachedChannelRules]
  /// synchronously.
  Future<StreamRuleCascade> ensureChannelRules(String channelId) async {
    final cached = cachedChannelRules(channelId);
    if (cached != null) return cached;
    // A failed fetch (e.g. this deployment's configured bearer token is a
    // device token, not a session token — see the doc comment on
    // PlayheadApi's stream-preference methods) is cached as an empty
    // cascade too, same as a success: without this, a channel switch on a
    // deployment that can never authenticate this call would retry a
    // doomed network request on every single switch — needless latency on
    // the channel-load path for no benefit (the periodic poll's
    // revision-triggered cache clear, see _pollIfStale, is what eventually
    // retries once the underlying condition might have changed).
    StreamRuleCascade result;
    try {
      result = await _api.getStreamPreferenceConfig(scopeType: 'channel', scopeId: channelId);
    } catch (_) {
      result = StreamRuleCascade.empty;
    }
    await _prefs.setString('$_channelRulesPrefix$channelId', _encode(result));
    final index = _prefs.getStringList(_channelRulesIndexKey) ?? const [];
    if (!index.contains(channelId)) {
      await _prefs.setStringList(_channelRulesIndexKey, [...index, channelId]);
    }
    return result;
  }

  Future<void> _clearChannelRulesCache() async {
    final index = _prefs.getStringList(_channelRulesIndexKey) ?? const [];
    for (final channelId in index) {
      await _prefs.remove('$_channelRulesPrefix$channelId');
    }
    await _prefs.remove(_channelRulesIndexKey);
  }

  StreamRuleCascade _decode(String? raw) {
    if (raw == null) return StreamRuleCascade.empty;
    try {
      return StreamRuleCascade.fromJson(jsonDecode(raw) as Map<String, dynamic>);
    } catch (_) {
      return StreamRuleCascade.empty;
    }
  }

  String _encode(StreamRuleCascade cascade) => jsonEncode({
        'steps': [
          for (final step in cascade.steps)
            {
              'audio_language': step.audioLanguage,
              'subtitle_language': step.subtitleLanguage,
              'title_blocklist': step.titleBlocklist,
              'title_allowlist': step.titleAllowlist,
            },
        ],
      });
}
