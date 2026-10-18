// First-run pairing boot helpers: which server URL the app already knows and
// where an unconfigured client should go next. Pure Dart (no Flutter, no
// storage) so the routing matrix is unit-tested directly.
//
// Precedence for the "known" backend URL mirrors the rest of the client: what
// a previous session stored on the device wins, else the URL injected into the
// build with --dart-define=PLAYHEADTV_OFFICIAL_URL. A build's official URL is
// never committed to the repository (see AGENTS.md).

/// Where a first-run client goes next.
enum BootRoute {
  /// The connection is complete: straight to the home page.
  home,

  /// No complete connection, but a server URL is known: quick-connect pairing.
  pairing,

  /// No complete connection and no known server URL: ask the user to type one.
  serverUrl,
}

/// The pure routing decision. [serverUrl] may be a raw, untrusted value; it is
/// normalised first and counts as "known" only when it is a usable http(s) URL.
BootRoute decideBootRoute({required bool configured, required String serverUrl}) {
  if (configured) return BootRoute.home;
  return ServerUrlResolver.normalize(serverUrl).isNotEmpty ? BootRoute.pairing : BootRoute.serverUrl;
}

/// Resolves and sanitises the Playhead TV backend URL the client talks to.
class ServerUrlResolver {
  /// The build-time define carrying the official URL. No real hostname is ever
  /// committed; the value exists only in a packaged/developer build.
  static const String officialUrlDefine = 'PLAYHEADTV_OFFICIAL_URL';

  /// Longest URL accepted from any source (build define, device storage or a
  /// server response).
  static const int maxUrlLength = 2048;

  const ServerUrlResolver._();

  /// The official server URL injected into the build, or empty when the build
  /// did not set the define.
  static String officialUrl({
    String define = const String.fromEnvironment(officialUrlDefine),
  }) =>
      normalize(define);

  /// The server URL the app already knows: what a previous session stored wins,
  /// else the build-time official URL. Empty when neither is usable.
  static String knownServerUrl({
    required String storedBackendUrl,
    String officialDefine = const String.fromEnvironment(officialUrlDefine),
  }) {
    final stored = normalize(storedBackendUrl);
    if (stored.isNotEmpty) return stored;
    return normalize(officialDefine);
  }

  /// Trims, adds `http://` when no scheme was typed, drops trailing slashes and
  /// rejects anything that is not http(s), has no host or is too long. A value
  /// like `javascript:...` or `file:///...` is rejected, never coerced.
  static String normalize(String raw) {
    var url = raw.trim();
    if (url.isEmpty || url.length > maxUrlLength) return '';
    final schemeEnd = url.indexOf('://');
    if (schemeEnd == -1) {
      // No scheme: only accept a plain host[:port][/path]; a stray ':' before
      // the first '/' is a scheme we do not support.
      final firstColon = url.indexOf(':');
      final firstSlash = url.indexOf('/');
      if (firstColon != -1 && (firstSlash == -1 || firstColon < firstSlash)) return '';
      url = 'http://$url';
    } else {
      final scheme = url.substring(0, schemeEnd).toLowerCase();
      if (scheme != 'http' && scheme != 'https') return '';
    }
    final uri = Uri.tryParse(url);
    if (uri == null || uri.host.isEmpty) return '';
    while (url.endsWith('/')) {
      url = url.substring(0, url.length - 1);
    }
    return url;
  }
}
