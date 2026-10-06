// Backend compatibility: the client declares the range of backend API versions
// it supports and compares it with what the backend reports on GET /version.
// The API version is an integer bumped only for a breaking change of the REST
// contract (backend/internal/buildinfo), independent of the release versions.
import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

/// The oldest and the newest backend API version this client works with.
/// Raise [maxSupportedApiVersion] when a client release is verified against a
/// newer backend; raise [minSupportedApiVersion] when it drops an old one.
const minSupportedApiVersion = 1;
const maxSupportedApiVersion = 1;

enum CompatStatus {
  /// The backend's API version is inside the supported range.
  compatible,

  /// The backend is older than this client supports.
  backendTooOld,

  /// The backend is newer than this client supports.
  backendTooNew,

  /// The backend did not tell (unreachable, or a build without GET /version):
  /// never a reason to block, the offline cache keeps working.
  unknown,
}

class CompatResult {
  final CompatStatus status;
  final int? backendApiVersion;
  final String? backendVersion;

  const CompatResult(this.status, {this.backendApiVersion, this.backendVersion});

  bool get isIncompatible => status == CompatStatus.backendTooOld || status == CompatStatus.backendTooNew;

  /// A clear sentence for the user, or null when there is nothing to say.
  String? get message {
    switch (status) {
      case CompatStatus.backendTooOld:
        return 'This Playhead TV backend (API version $backendApiVersion) is older than this app supports '
            '(API versions $minSupportedApiVersion to $maxSupportedApiVersion). '
            'Update the backend, or install an older app version.';
      case CompatStatus.backendTooNew:
        return 'This Playhead TV backend (API version $backendApiVersion) is newer than this app supports '
            '(API versions $minSupportedApiVersion to $maxSupportedApiVersion). '
            'Update the app to the latest release.';
      case CompatStatus.compatible:
      case CompatStatus.unknown:
        return null;
    }
  }
}

/// Classifies a backend API version against the supported range.
CompatResult classifyApiVersion(int apiVersion, {String? backendVersion}) {
  final status = apiVersion < minSupportedApiVersion
      ? CompatStatus.backendTooOld
      : apiVersion > maxSupportedApiVersion
          ? CompatStatus.backendTooNew
          : CompatStatus.compatible;
  return CompatResult(status, backendApiVersion: apiVersion, backendVersion: backendVersion);
}

/// Parses a GET /version body; [CompatStatus.unknown] for anything that does
/// not carry an integer `api_version`.
CompatResult parseVersionBody(String body) {
  try {
    final json = jsonDecode(body);
    if (json is Map && json['api_version'] is int) {
      return classifyApiVersion(json['api_version'] as int,
          backendVersion: json['version'] is String ? json['version'] as String : null);
    }
  } on FormatException {
    // fall through
  }
  return const CompatResult(CompatStatus.unknown);
}

/// Asks the backend at [baseUrl] for its API version. Never throws: a backend
/// that cannot be reached, or does not know /version, is `unknown`.
Future<CompatResult> checkBackendCompat(
  String baseUrl, {
  http.Client? client,
  Duration timeout = const Duration(seconds: 5),
}) async {
  if (baseUrl.isEmpty) return const CompatResult(CompatStatus.unknown);
  final owned = client == null;
  final c = client ?? http.Client();
  try {
    final uri = Uri.parse(baseUrl.endsWith('/') ? '${baseUrl}version' : '$baseUrl/version');
    final response = await c.get(uri).timeout(timeout);
    if (response.statusCode != 200) return const CompatResult(CompatStatus.unknown);
    return parseVersionBody(response.body);
  } on Object {
    return const CompatResult(CompatStatus.unknown);
  } finally {
    if (owned) c.close();
  }
}
