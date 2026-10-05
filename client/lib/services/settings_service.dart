import 'package:shared_preferences/shared_preferences.dart';

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
class SettingsService {
  static const _lastWatchedKey = 'last_watched_channel_id';
  static const _recentChannelsKey = 'recent_channel_ids';
  static const _maxRecentChannels = 6;
  static const _keyBindingPrefix = 'key_binding_'; // + InputAction name
  static const _audioTrackOverridePrefix = 'track_override_audio_'; // + channel id
  static const _subtitleTrackOverridePrefix = 'track_override_subtitle_'; // + channel id

  static const _exhaustedBehaviorKey = 'cache_exhausted_behavior';
  static const _cacheWindowLimitKey = 'cache_window_limit_hours';

  /// Choices offered for the cache window limit (hours).
  static const cacheWindowLimitChoicesHours = [6, 12, 24, 48, 72, 168];

  final SharedPreferences _prefs;

  SettingsService(this._prefs);

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
