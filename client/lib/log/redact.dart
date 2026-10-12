// Secret redaction, done by the logger itself so no call site has to remember.
// Two layers: the exact secrets the app knows it holds (the Jellyfin API key,
// the device token), wherever they appear, and patterns for the places a secret
// usually travels (URL query parameters, `Authorization` headers, token
// headers) in case a value the app does not know about shows up.

const redactedMark = '<redacted>';

// Query parameters / key=value pairs whose value is a secret.
final _paramPattern = RegExp(
  r'''((?:api[_-]?key|access[_-]?token|device[_-]?token|auth[_-]?token|x-emby-token|x-mediabrowser-token|token|password|secret)["']?\s*[=:]\s*["']?)[^&\s"'<>,;]+''',
  caseSensitive: false,
);

// `Authorization: Bearer abc`, `Authorization=...`: everything up to the end of
// the line (a scheme and a value, or a MediaBrowser key="value" list).
final _authLinePattern = RegExp(r'(authorization\s*[:=]\s*)[^\r\n]*', caseSensitive: false);

// JSON-ish `"Authorization": "Bearer abc"`.
final _authJsonPattern = RegExp(r'''(authorization["']\s*:\s*["'])[^"']*''', caseSensitive: false);

class Redactor {
  final Set<String> _secrets = {};

  /// A secret shorter than this is not matched literally (it would mangle
  /// ordinary text); the patterns still apply to it.
  static const minSecretLength = 4;

  /// Replaces the known secrets. Empty and null values are ignored.
  void setSecrets(Iterable<String?> secrets) {
    _secrets
      ..clear()
      ..addAll({
        for (final s in secrets)
          if (s != null && s.length >= minSecretLength) ...[s, Uri.encodeComponent(s), Uri.encodeFull(s)],
      });
  }

  String redact(String text) {
    var out = text;
    // Longest first, so a secret containing another is replaced whole.
    final known = _secrets.toList()..sort((a, b) => b.length.compareTo(a.length));
    for (final s in known) {
      out = out.replaceAll(s, redactedMark);
    }
    out = out.replaceAllMapped(_authJsonPattern, (m) => '${m[1]}$redactedMark');
    out = out.replaceAllMapped(_authLinePattern, (m) => '${m[1]}$redactedMark');
    out = out.replaceAllMapped(_paramPattern, (m) => '${m[1]}$redactedMark');
    return out;
  }
}
