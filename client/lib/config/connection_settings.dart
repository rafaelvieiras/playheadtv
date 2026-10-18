// The connection settings a person types into the setup screen: where the media
// server and the Playhead TV backend are. Pure Dart (no Flutter), so the
// normalisation and validation are unit-tested.
//
// They are stored on the device (SettingsService) under the same keys the config
// file uses, and ranked below it and below a build's --dart-define (see
// AppConfig): a packaged install or a developer build keeps winning.

/// Config-file keys, shared with `AppConfig` and the stored copy.
const jellyfinUrlKey = 'jellyfin_url';
const jellyfinApiKeyKey = 'jellyfin_api_key';
const playheadBaseUrlKey = 'playhead_base_url';
const playheadDeviceTokenKey = 'playhead_device_token';

class ConnectionSettings {
  final String jellyfinUrl;
  final String jellyfinApiKey;
  final String playheadBaseUrl;

  /// Optional: empty means the backend runs without auth.
  final String playheadDeviceToken;

  const ConnectionSettings({
    this.jellyfinUrl = '',
    this.jellyfinApiKey = '',
    this.playheadBaseUrl = '',
    this.playheadDeviceToken = '',
  });

  /// What the player needs to start: the media server, its key and the backend.
  bool get isComplete => jellyfinUrl.isNotEmpty && jellyfinApiKey.isNotEmpty && playheadBaseUrl.isNotEmpty;

  /// Trimmed, with a scheme and no trailing slash on the URLs.
  ConnectionSettings normalized() => ConnectionSettings(
        jellyfinUrl: normalizeUrl(jellyfinUrl),
        jellyfinApiKey: jellyfinApiKey.trim(),
        playheadBaseUrl: normalizeUrl(playheadBaseUrl),
        playheadDeviceToken: playheadDeviceToken.trim(),
      );

  /// The first problem a person must fix before saving, or null. Messages name
  /// the field and never repeat what was typed (a key must not reach a screen
  /// capture or a log).
  String? validate() {
    final n = normalized();
    final jellyfin = validateUrl(n.jellyfinUrl);
    if (jellyfin != null) return 'Media server URL: $jellyfin';
    if (n.jellyfinApiKey.isEmpty) return 'Media server API key is required.';
    final backend = validateUrl(n.playheadBaseUrl);
    if (backend != null) return 'Playhead TV URL: $backend';
    return null;
  }

  Map<String, String> toStored() => {
        jellyfinUrlKey: jellyfinUrl,
        jellyfinApiKeyKey: jellyfinApiKey,
        playheadBaseUrlKey: playheadBaseUrl,
        playheadDeviceTokenKey: playheadDeviceToken,
      };

  factory ConnectionSettings.fromStored(Map<String, Object?> map) {
    String s(String key) => map[key] is String ? map[key]! as String : '';
    return ConnectionSettings(
      jellyfinUrl: s(jellyfinUrlKey),
      jellyfinApiKey: s(jellyfinApiKeyKey),
      playheadBaseUrl: s(playheadBaseUrlKey),
      playheadDeviceToken: s(playheadDeviceTokenKey),
    );
  }
}

/// Trims, adds `http://` when no scheme was typed and drops trailing slashes
/// (the app appends paths to these URLs). Empty stays empty.
String normalizeUrl(String raw) {
  var url = raw.trim();
  if (url.isEmpty) return '';
  if (!url.contains('://')) url = 'http://$url';
  while (url.endsWith('/')) {
    url = url.substring(0, url.length - 1);
  }
  return url;
}

/// A problem with an already normalised URL, or null when it is usable.
String? validateUrl(String url) {
  if (url.isEmpty) return 'is required.';
  final uri = Uri.tryParse(url);
  if (uri == null || uri.host.isEmpty) return 'is not a valid address.';
  if (uri.scheme != 'http' && uri.scheme != 'https') return 'must start with http:// or https://.';
  return null;
}
