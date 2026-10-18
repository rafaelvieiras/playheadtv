// The device identity sent with POST /pair: a display name the approval page
// shows and one of the backend's known platforms. Pure-ish (dart:io only for
// the defaults) so the mapping is unit-tested with injected values.
import 'dart:io';

import 'package:flutter/services.dart';

/// Platforms the backend's device registration accepts (internal/domain/tenant.go).
/// A value outside this list is rejected there, so the client must map to one
/// of them before sending.
const List<String> backendPlatforms = <String>[
  'linux',
  'windows',
  'android',
  'android_tv',
  'ios',
  'tvos',
];

/// Maps the running platform and a display name to the pairing request.
class DeviceIdentity {
  const DeviceIdentity._();

  static const MethodChannel _channel = MethodChannel('playheadtv/device');

  /// Whether this is an Android TV and the hardware model name, asked of the
  /// Android side. Anything else (other platforms, a failing channel) yields a
  /// plain device with no model, so pairing never depends on it.
  static Future<({bool isTv, String? model})> detect() async {
    if (!Platform.isAndroid) return (isTv: false, model: null);
    try {
      final info = await _channel.invokeMapMethod<String, Object?>('info');
      final model = info?['model'];
      return (isTv: info?['tv'] == true, model: model is String && model.isNotEmpty ? model : null);
    } catch (_) {
      return (isTv: false, model: null);
    }
  }

  /// The default display name when the hostname is empty or unusable.
  static const String defaultName = 'Playhead TV client';

  /// Longest device name accepted (matches [PairingApi.maxNameLength]).
  static const int maxNameLength = 64;

  /// Maps a Dart operating system string (see `Platform.operatingSystem`) to a
  /// backend platform. [isAndroidTv] upgrades android when the build knows it
  /// runs on a TV; the client cannot detect that reliably in Dart today, so it
  /// defaults to plain android.
  static String platform({String? operatingSystem, bool isAndroidTv = false}) {
    final os = (operatingSystem ?? Platform.operatingSystem).toLowerCase();
    switch (os) {
      case 'linux':
        return 'linux';
      case 'windows':
        return 'windows';
      case 'android':
        return isAndroidTv ? 'android_tv' : 'android';
      case 'ios':
        return 'ios';
      case 'macos':
        // No macOS entry on the backend; the closest Apple target is ios.
        return 'ios';
      default:
        return 'linux';
    }
  }

  /// A human-readable name for the approval page. The hostname is only a label
  /// and is never sent anywhere else; a hostile or empty value falls back to
  /// [defaultName]. Bounded to [maxNameLength] and stripped of anything that is
  /// not a plain name character.
  static String name({String? hostname}) {
    final raw = (hostname ?? _safeHostname()).trim();
    final cleaned = raw.replaceAll(RegExp(r'[^A-Za-z0-9._\- ]'), '').trim();
    if (cleaned.isEmpty) return defaultName;
    return cleaned.length > maxNameLength ? cleaned.substring(0, maxNameLength) : cleaned;
  }

  static String _safeHostname() {
    try {
      return Platform.localHostname;
    } catch (_) {
      return '';
    }
  }
}
