import 'package:shared_preferences/shared_preferences.dart';

/// Simple local persistence for the client — no code generation, same style
/// as the rest of the project (see playhead_api.dart). Stores: the last
/// watched channel (so boot can skip the mosaic and go straight to the player
/// when there's history), a short history of recent channels, and key binding
/// overrides (set from Settings -> "Remap controls").
class SettingsService {
  static const _lastWatchedKey = 'last_watched_channel_id';
  static const _recentChannelsKey = 'recent_channel_ids';
  static const _maxRecentChannels = 6;
  static const _keyBindingPrefix = 'key_binding_'; // + InputAction name

  final SharedPreferences _prefs;

  SettingsService(this._prefs);

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
}
