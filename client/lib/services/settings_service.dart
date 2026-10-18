import 'package:shared_preferences/shared_preferences.dart';

import '../config/connection_settings.dart';
import '../dimmer.dart';
import '../preload_lead.dart';
import '../update/semver.dart';
import '../update/update_notices.dart';

/// The degraded behavior of a channel whose cached schedule ran out.
enum ExhaustedBehavior {
  /// Replay the last known programming, over and over (the default).
  loopLast,

  /// Hold the local slate until a refresh brings a schedule.
  slate,
}

/// Simple local persistence for the client — no code generation, same style
/// as the rest of the project (see playhead_api.dart). Stores: the last
/// watched channel (so boot can skip the mosaic and go straight to the player
/// when there's history), a short history of recent channels, key binding
/// overrides (set from Settings -> "Remap controls"), and tier 1 stream
/// (audio/subtitle) track overrides (set from the player's track selector —
/// see widgets/track_selector_overlay.dart and
/// services/stream_selection_resolver.dart's TierOneOverride). Tier 1 is
/// explicitly client-local only (issue #3's design) — never sent to or
/// stored by the backend, unlike tiers 2-4 (StreamPreferencesService).
class SettingsService implements UpdateNoticeStore {
  static const _dismissedUpdateKey = 'dismissed_update_version';
  static const _changelogShownKey = 'update_changelog_shown_for';
  static const _lastWatchedKey = 'last_watched_channel_id';
  static const _recentChannelsKey = 'recent_channel_ids';
  static const _maxRecentChannels = 6;
  static const _keyBindingPrefix = 'key_binding_'; // + InputAction name
  static const _audioTrackOverridePrefix = 'track_override_audio_'; // + channel id
  static const _subtitleTrackOverridePrefix = 'track_override_subtitle_'; // + channel id

  static const _exhaustedBehaviorKey = 'cache_exhausted_behavior';
  static const _cacheWindowLimitKey = 'cache_window_limit_hours';
  static const _startFullScreenKey = 'start_fullscreen';
  static const _uiScaleKey = 'ui_scale_override';
  static const _preloadLeadKey = 'preload_lead_seconds';
  static const _dimLevelKey = 'dim_level_percent';
  static const _connectionPrefix = 'connection_'; // + a connection_settings.dart key

  /// Choices offered for the cache window limit (hours).
  static const cacheWindowLimitChoicesHours = [6, 12, 24, 48, 72, 168];

  final SharedPreferences _prefs;

  SettingsService(this._prefs);

  /// The newest release whose "new version" banner the user dismissed: the
  /// banner is not shown again until a newer release appears.
  @override
  SemVer? get dismissedVersion => SemVer.tryParse(_prefs.getString(_dismissedUpdateKey) ?? '');

  @override
  Future<void> setDismissedVersion(SemVer version) => _prefs.setString(_dismissedUpdateKey, version.toString());

  /// The version whose "updated to X" changelog was already shown.
  @override
  String? get changelogShownFor => _prefs.getString(_changelogShownKey);

  @override
  Future<void> setChangelogShownFor(String version) => _prefs.setString(_changelogShownKey, version);

  /// What a channel does once the cached schedule has run out (offline for
  /// longer than the window): the client's own choice, never a backend one.
  ExhaustedBehavior get exhaustedBehavior {
    final stored = _prefs.getString(_exhaustedBehaviorKey);
    return ExhaustedBehavior.values.firstWhere((b) => b.name == stored, orElse: () => ExhaustedBehavior.loopLast);
  }

  Future<void> setExhaustedBehavior(ExhaustedBehavior behavior) => _prefs.setString(_exhaustedBehaviorKey, behavior.name);

  /// The client's cap on how much programming the cache keeps (the effective
  /// window is the smaller of this and the backend's).
  Duration get cacheWindowLimit => Duration(hours: _prefs.getInt(_cacheWindowLimitKey) ?? 48);

  Future<void> setCacheWindowLimit(Duration limit) => _prefs.setInt(_cacheWindowLimitKey, limit.inHours);

  /// Whether the client opens in full screen (desktop only). The user's saved
  /// choice wins; with none saved, [defaultValue] (the installation's
  /// `PLAYHEADTV_FULLSCREEN` / `start_fullscreen`, itself true when unset).
  bool startFullScreen(bool defaultValue) => _prefs.getBool(_startFullScreenKey) ?? defaultValue;

  Future<void> setStartFullScreen(bool value) => _prefs.setBool(_startFullScreenKey, value);

  /// The user's fixed interface size (a factor, see ui/ui_scale.dart), or null
  /// for Automatic.
  double? get uiScaleOverride {
    final v = _prefs.getDouble(_uiScaleKey);
    return v != null && v > 0 ? v : null;
  }

  Future<void> setUiScaleOverride(double? value) =>
      value == null ? _prefs.remove(_uiScaleKey) : _prefs.setDouble(_uiScaleKey, value);

  /// The user's saved preload lead in seconds (see preload_lead.dart), or null
  /// when never chosen (the installation default applies).
  int? get preloadLeadSeconds {
    final v = _prefs.getInt(_preloadLeadKey);
    return v == null ? null : sanitizePreloadLeadSeconds(v);
  }

  Future<void> setPreloadLeadSeconds(int seconds) => _prefs.setInt(_preloadLeadKey, seconds);

  /// The user's saved dim level in percent (see dimmer.dart), or null when never
  /// chosen (the installation default applies).
  int? get dimLevelPercent {
    final v = _prefs.getInt(_dimLevelKey);
    return v == null ? null : sanitizeDimLevelPercent(v);
  }

  Future<void> setDimLevelPercent(int percent) => _prefs.setInt(_dimLevelKey, sanitizeDimLevelPercent(percent));

  /// The connection settings typed into the setup screen (empty until then).
  /// They rank below the config file and a build's --dart-define.
  ConnectionSettings get connection => ConnectionSettings.fromStored({
        for (final key in [jellyfinUrlKey, jellyfinApiKeyKey, playheadBaseUrlKey, playheadDeviceTokenKey])
          key: _prefs.getString('$_connectionPrefix$key'),
      });

  Future<void> setConnection(ConnectionSettings value) async {
    for (final e in value.toStored().entries) {
      if (e.value.isEmpty) {
        await _prefs.remove('$_connectionPrefix${e.key}');
      } else {
        await _prefs.setString('$_connectionPrefix${e.key}', e.value);
      }
    }
  }

  static Future<SettingsService> create() async => SettingsService(await SharedPreferences.getInstance());

  String? get lastWatchedChannelId => _prefs.getString(_lastWatchedKey);

  List<String> get recentChannelIds => _prefs.getStringList(_recentChannelsKey) ?? const [];

  /// Called whenever the player successfully tunes a channel (boot or
  /// switch). Updates the last watched channel and the MRU history (no
  /// duplicates, most recent first, truncated at _maxRecentChannels).
  Future<void> recordWatched(String channelId) async {
    await _prefs.setString(_lastWatchedKey, channelId);
    final updated = [channelId, ...recentChannelIds.where((id) => id != channelId)];
    if (updated.length > _maxRecentChannels) {
      updated.removeRange(_maxRecentChannels, updated.length);
    }
    await _prefs.setStringList(_recentChannelsKey, updated);
  }

  /// keyId (LogicalKeyboardKey.keyId) saved for the InputAction named
  /// `actionName`, or null if it was never remapped (uses the default).
  int? getKeyBindingKeyId(String actionName) => _prefs.getInt('$_keyBindingPrefix$actionName');

  Future<void> setKeyBinding(String actionName, int keyId) =>
      _prefs.setInt('$_keyBindingPrefix$actionName', keyId);

  /// Removes every known key binding override (Settings: "Restore
  /// defaults"). `actionNames` is the list of names of every InputAction —
  /// passed by the caller so this service doesn't need to know the enum
  /// (avoids a circular import with input_action.dart).
  Future<void> clearKeyBindings(Iterable<String> actionNames) async {
    for (final name in actionNames) {
      await _prefs.remove('$_keyBindingPrefix$name');
    }
  }

  /// Tier 1 audio track override for `channelId`: the literal index into
  /// `MediaInfo.audio` the user picked (track selector or the
  /// `cycleAudioTrack` accelerator) for what's currently showing on that
  /// channel. Null means "never overridden here" — falls through to tiers
  /// 2-4 (see StreamSelectionResolver.resolve).
  int? getAudioTrackOverride(String channelId) => _prefs.getInt('$_audioTrackOverridePrefix$channelId');

  Future<void> setAudioTrackOverride(String channelId, int trackIndex) =>
      _prefs.setInt('$_audioTrackOverridePrefix$channelId', trackIndex);

  /// Tier 1 subtitle track override for `channelId`: the literal index into
  /// `MediaInfo.subtitle`, or `-1` for the picker's explicit "None" choice
  /// (disables subtitles, distinct from null — "never overridden", which
  /// falls through to tiers 2-4).
  int? getSubtitleTrackOverride(String channelId) => _prefs.getInt('$_subtitleTrackOverridePrefix$channelId');

  Future<void> setSubtitleTrackOverride(String channelId, int trackIndex) =>
      _prefs.setInt('$_subtitleTrackOverridePrefix$channelId', trackIndex);
}
