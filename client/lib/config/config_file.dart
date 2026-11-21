// The config file: settings a packaged install changes without a rebuild
// (backend URL, device token, ...). A flat JSON object, e.g.
//
//   {
//     "playhead_base_url": "https://tv.example.com",
//     "playhead_device_token": "<token>",
//     "jellyfin_url": "https://media.example.com",
//     "jellyfin_api_key": "<key>",
//     "playhead_channel_id": "",
//     "working_set_size": 5,
//     "preload_lead_seconds": 20,
//     "dim_level_percent": 50,
//     "epg_sync_interval_minutes": 15,
//     "start_fullscreen": true,
//     "mosaic_safe_margin_percent": 2,
//     "auto_update": true,
//     "loudness_enabled": true,
//     "loudness_target_lufs": -24,
//     "update_channel": "stable"
//   }
//
// Pure Dart (no Flutter): the updater CLI reads the same file.
import 'dart:convert';
import 'dart:io';

/// Where the config file is looked up, first match wins:
///  1. the PLAYHEADTV_CONFIG environment variable (the launcher sets it to
///     `<install root>/config.json`),
///  2. `$XDG_CONFIG_HOME/playheadtv/config.json`,
///  3. `$HOME/.config/playheadtv/config.json`.
/// Returns null when none can be derived. The file need not exist.
File? configFileLocation(Map<String, String> env) {
  final explicit = env['PLAYHEADTV_CONFIG'];
  if (explicit != null && explicit.isNotEmpty) return File(explicit);
  final xdg = env['XDG_CONFIG_HOME'];
  if (xdg != null && xdg.isNotEmpty) return File('$xdg/playheadtv/config.json');
  final home = env['HOME'];
  if (home != null && home.isNotEmpty) return File('$home/.config/playheadtv/config.json');
  return null;
}

/// Parses config file text. Anything that is not a JSON object yields an empty
/// config (an unusable file must not stop the app; the missing-settings screen
/// tells the user what is wrong).
Map<String, Object?> parseConfig(String text) {
  try {
    final json = jsonDecode(text);
    if (json is Map) return {for (final e in json.entries) '${e.key}': e.value};
  } on FormatException {
    // fall through
  }
  return const {};
}

/// Reads [file]; an absent or unreadable file is an empty config.
Map<String, Object?> readConfigFile(File? file) {
  if (file == null) return const {};
  try {
    return parseConfig(file.readAsStringSync());
  } on FileSystemException {
    return const {};
  }
}

/// One text setting. Precedence: a test override, then a non-empty
/// `--dart-define` (a developer's build wins over a leftover file), then the
/// config file [key], then empty.
String resolveStringSetting({
  required String? override,
  required String define,
  required Map<String, Object?> file,
  required String key,
}) {
  if (override != null) return override;
  if (define.isNotEmpty) return define;
  final value = file[key];
  return value is String ? value : '';
}

/// One integer setting with the same precedence ([defined] says whether the
/// build set the `--dart-define` at all); [fallback] when nothing sets it or the
/// file value is not a positive integer.
int resolveIntSetting({
  required int? override,
  required bool defined,
  required int define,
  required Map<String, Object?> file,
  required String key,
  required int fallback,
}) {
  if (override != null) return override;
  if (defined) return define;
  final value = file[key];
  return value is int && value > 0 ? value : fallback;
}

/// Reads a boolean from text an operator typed (an environment variable):
/// `1/true/yes/on` and `0/false/no/off`, case-insensitive; anything else
/// (including empty) is null, "not set".
bool? parseBoolText(String? text) {
  switch (text?.trim().toLowerCase()) {
    case '1' || 'true' || 'yes' || 'on':
      return true;
    case '0' || 'false' || 'no' || 'off':
      return false;
  }
  return null;
}

/// One boolean setting. Precedence: a test override, then the environment
/// variable [envValue] (see [parseBoolText]), then the config file [key] (a JSON
/// boolean), then [fallback].
bool resolveBoolSetting({
  required bool? override,
  required String? envValue,
  required Map<String, Object?> file,
  required String key,
  required bool fallback,
}) {
  if (override != null) return override;
  final fromEnv = parseBoolText(envValue);
  if (fromEnv != null) return fromEnv;
  final value = file[key];
  return value is bool ? value : fallback;
}
