// Public, unauthenticated client for the temporary-PIN pairing API:
//   POST /pair       starts a pairing, returns the PIN and the poll secret;
//   POST /pair/poll  reports pending / approved / expired.
//
// Pure HTTP (package:http) with an injectable client, so the whole flow is
// unit-tested with MockClient and no backend. The server is untrusted: URLs
// must be http(s) and bounded in length, and no secret (poll secret, device
// token, Jellyfin API key) is ever logged or included in an error message or a
// toString().
import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import 'pairing_boot.dart' show ServerUrlResolver;

/// Outcome of POST /pair.
sealed class PairingStartResult {
  const PairingStartResult();
}

/// The backend accepted the request: a PIN to show and a secret to poll with.
class PairingStarted extends PairingStartResult {
  final String pin;
  final String pollSecret;
  final Duration expiresIn;
  final Duration pollInterval;

  const PairingStarted({
    required this.pin,
    required this.pollSecret,
    required this.expiresIn,
    required this.pollInterval,
  });

  /// Redacted: the poll secret must never reach a log line.
  @override
  String toString() => 'PairingStarted(pin: $pin, expiresIn: $expiresIn, pollInterval: $pollInterval)';
}

/// The request was rejected or the response was unusable. [message] is safe to
/// show and never contains a secret.
class PairingStartFailure extends PairingStartResult {
  final String message;
  final int? statusCode;

  const PairingStartFailure(this.message, {this.statusCode});

  @override
  String toString() => 'PairingStartFailure($message${statusCode == null ? '' : ', status: $statusCode'})';
}

/// Outcome of POST /pair/poll.
sealed class PairingPollResult {
  const PairingPollResult();
}

/// The user has not approved yet; poll again after [retryAfter].
class PairingPending extends PairingPollResult {
  final Duration retryAfter;

  const PairingPending(this.retryAfter);

  @override
  String toString() => 'PairingPending(retryAfter: $retryAfter)';
}

/// The user approved: the credentials the client must store.
class PairingApproved extends PairingPollResult {
  final String deviceToken;
  final String jellyfinUrl;
  final String jellyfinApiKey;

  const PairingApproved({
    required this.deviceToken,
    required this.jellyfinUrl,
    required this.jellyfinApiKey,
  });

  /// Redacted: the device token and the Jellyfin API key must never reach a log.
  @override
  String toString() => 'PairingApproved(jellyfinUrl: $jellyfinUrl)';
}

/// The pairing is unknown, expired or already delivered (HTTP 404).
class PairingExpired extends PairingPollResult {
  final String message;

  const PairingExpired([this.message = 'This pairing code has expired.']);

  @override
  String toString() => 'PairingExpired($message)';
}

/// A transport failure or a malformed/hostile response. [message] is safe to
/// show and never contains a secret.
class PairingPollFailure extends PairingPollResult {
  final String message;
  final int? statusCode;

  const PairingPollFailure(this.message, {this.statusCode});

  @override
  String toString() => 'PairingPollFailure($message${statusCode == null ? '' : ', status: $statusCode'})';
}

/// Talks to the public pairing endpoints of one backend. Unauthenticated: it
/// carries no device token and sends none.
class PairingApi {
  /// Largest response body the client will read; a hostile server cannot make
  /// it allocate an unbounded string.
  static const int maxResponseBytes = 64 * 1024;

  /// Field limits. Anything longer is treated as malformed, never truncated
  /// and forwarded (truncating a secret would silently corrupt it).
  static const int maxNameLength = 64;
  static const int maxPlatformLength = 32;
  static const int maxPinLength = 16;
  static const int maxPollSecretLength = 128;
  static const int maxTokenLength = 4096;
  static const int maxApiKeyLength = 4096;

  /// Upper bounds on the server-reported durations; a hostile server cannot
  /// make the client wait for years or poll in a tight loop.
  static const int maxExpiresInSeconds = 24 * 60 * 60;
  static const int maxPollIntervalSeconds = 300;

  final String baseUrl;
  final http.Client _client;
  final Duration _timeout;
  final bool _ownsClient;

  PairingApi({
    required String baseUrl,
    http.Client? client,
    Duration timeout = const Duration(seconds: 15),
  })  : baseUrl = ServerUrlResolver.normalize(baseUrl),
        _client = client ?? http.Client(),
        // ignore: prefer_initializing_formals
        _timeout = timeout,
        _ownsClient = client == null;

  /// Closes the underlying client only when this instance created it.
  void close() {
    if (_ownsClient) _client.close();
  }

  /// Starts a pairing. [name] and [platform] are the device identity the
  /// approval page shows; both are trimmed and bounded.
  Future<PairingStartResult> start({required String name, required String platform}) async {
    if (baseUrl.isEmpty) return const PairingStartFailure('The server address is not valid.');
    final safeName = _trimmed(name, maxNameLength);
    final safePlatform = _trimmed(platform, maxPlatformLength);
    if (safeName.isEmpty) return const PairingStartFailure('A device name is required.');
    if (safePlatform.isEmpty) return const PairingStartFailure('A device platform is required.');

    final http.Response response;
    try {
      response = await _client
          .post(
            Uri.parse('$baseUrl/pair'),
            headers: const {'content-type': 'application/json'},
            body: jsonEncode({'name': safeName, 'platform': safePlatform}),
          )
          .timeout(_timeout);
    } on TimeoutException {
      return const PairingStartFailure('The server did not respond. Check the connection and try again.');
    } on http.ClientException {
      return const PairingStartFailure('Could not reach the server. Check the address and try again.');
    }

    if (response.statusCode != 200) {
      // A server without the pairing feature answers 401 (its auth layer sits
      // in front of unknown routes), 404 or 405: it is reachable, just too old.
      if (const {401, 403, 404, 405}.contains(response.statusCode)) {
        return PairingStartFailure(
          'This server does not support device pairing. Update the Playhead TV backend and try again.',
          statusCode: response.statusCode,
        );
      }
      return PairingStartFailure(_refusedMessage(response.statusCode), statusCode: response.statusCode);
    }
    final json = _decodeObject(response);
    if (json == null) return const PairingStartFailure('The server sent an unexpected response.');

    final pin = _stringField(json, 'pin', maxPinLength);
    final pollSecret = _stringField(json, 'poll_secret', maxPollSecretLength);
    final expiresIn = _positiveIntField(json, 'expires_in', maxExpiresInSeconds);
    final pollInterval = _positiveIntField(json, 'poll_interval', maxPollIntervalSeconds);
    if (!_looksLikePin(pin) || pollSecret.isEmpty || expiresIn == null || pollInterval == null) {
      return const PairingStartFailure('The server sent an unexpected response.');
    }

    return PairingStarted(
      pin: pin,
      pollSecret: pollSecret,
      expiresIn: Duration(seconds: expiresIn),
      pollInterval: Duration(seconds: pollInterval),
    );
  }

  /// One poll. [retryAfter] is echoed on a pending result so the caller can
  /// honour the server's interval; [pollSecret] is the secret from [start].
  Future<PairingPollResult> poll(String pollSecret, {Duration? retryAfter}) async {
    final secret = pollSecret.trim();
    if (baseUrl.isEmpty || secret.isEmpty || secret.length > maxPollSecretLength) {
      return const PairingPollFailure('The pairing could not be checked.');
    }

    final http.Response response;
    try {
      response = await _client
          .post(
            Uri.parse('$baseUrl/pair/poll'),
            headers: const {'content-type': 'application/json'},
            body: jsonEncode({'poll_secret': secret}),
          )
          .timeout(_timeout);
    } on TimeoutException {
      return const PairingPollFailure('The server did not respond.');
    } on http.ClientException {
      return const PairingPollFailure('Could not reach the server.');
    }

    if (response.statusCode == 404) return const PairingExpired();
    if (response.statusCode != 200) {
      return PairingPollFailure(_refusedMessage(response.statusCode), statusCode: response.statusCode);
    }

    final json = _decodeObject(response);
    if (json == null) return const PairingPollFailure('The server sent an unexpected response.');

    final status = _stringField(json, 'status', 32);
    if (status == 'pending') return PairingPending(retryAfter ?? const Duration(seconds: 3));
    if (status == 'approved') {
      final token = _stringField(json, 'device_token', maxTokenLength);
      final key = _stringField(json, 'jellyfin_api_key', maxApiKeyLength);
      // The Jellyfin URL is a server value used to build request URLs later:
      // http(s) only, bounded, never a javascript:/file: scheme.
      final jellyfinUrl = ServerUrlResolver.normalize(_stringField(json, 'jellyfin_url', ServerUrlResolver.maxUrlLength));
      // Both end up in HTTP headers: a control character (CR/LF) from a hostile
      // server must never get that far. Tokens are URL-safe text; keys are
      // printable ASCII without spaces.
      if (!_headerSafe.hasMatch(token) || !_headerSafe.hasMatch(key)) {
        return const PairingPollFailure('The server sent an unexpected response.');
      }
      if (token.isEmpty || key.isEmpty || jellyfinUrl.isEmpty) {
        return const PairingPollFailure('The server sent an unexpected response.');
      }
      return PairingApproved(deviceToken: token, jellyfinUrl: jellyfinUrl, jellyfinApiKey: key);
    }
    return const PairingPollFailure('The server sent an unexpected response.');
  }

  static final RegExp _headerSafe = RegExp(r'^[\x21-\x7E]+$');

  /// Bounded, trimmed copy of a request field.
  static String _trimmed(String value, int max) {
    final trimmed = value.trim();
    return trimmed.length > max ? trimmed.substring(0, max) : trimmed;
  }

  /// A field that must be a bounded non-empty string, else empty.
  static String _stringField(Map<String, Object?> json, String key, int max) {
    final value = json[key];
    if (value is! String) return '';
    final trimmed = value.trim();
    if (trimmed.isEmpty || trimmed.length > max) return '';
    return trimmed;
  }

  /// A field that must be a positive integer, else null. A JSON number encoded
  /// as a string is deliberately rejected (hostile or buggy server).
  static int? _positiveIntField(Map<String, Object?> json, String key, int max) {
    final value = json[key];
    return value is int && value > 0 && value <= max ? value : null;
  }

  /// A PIN in the contract's XXXX-XXXX shape. Case-insensitive, the dash is
  /// optional, and only the allowed alphabet (A-Z, 2-9) is accepted.
  static final RegExp _pinPattern = RegExp(r'^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{4}-[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{4}$');

  static bool _looksLikePin(String raw) {
    // The server's canonical start payload always shows the PIN as XXXX-XXXX;
    // a response without the dash is treated as malformed. The permissive
    // "with or without dash" rule applies to user input on the approval page,
    // not to an untrusted server response.
    final pin = raw.toUpperCase();
    return pin.length == 9 && pin[4] == '-' && _pinPattern.hasMatch(pin);
  }

  /// Decodes a JSON object, refusing a body larger than [maxResponseBytes].
  static Map<String, Object?>? _decodeObject(http.Response response) {
    if (response.bodyBytes.length > maxResponseBytes) return null;
    try {
      final decoded = jsonDecode(utf8.decode(response.bodyBytes, allowMalformed: true));
      if (decoded is Map) return {for (final e in decoded.entries) '${e.key}': e.value};
    } on FormatException {
      // fall through
    }
    return null;
  }

  /// A generic, safe message. The server's own error text is deliberately not
  /// echoed back (it is untrusted and could carry anything).
  static String _refusedMessage(int statusCode) => 'The server refused the request ($statusCode).';
}
