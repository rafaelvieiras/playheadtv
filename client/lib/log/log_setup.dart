// Turns the environment, the build and the config file into a configured Log.
// Pure Dart: the app's main() and the updater CLI both call it.
import 'dart:io';

import 'log.dart';
import 'log_location.dart';

/// The build's `--dart-define=PLAYHEADTV_LOG_LEVEL=`; empty when not set. A
/// local build can set it (for example from a developer's own build script) to
/// log at a high level without editing any file.
const logLevelDefine = String.fromEnvironment('PLAYHEADTV_LOG_LEVEL');

/// The level this process starts at; see [resolveLogLevel] for the precedence.
LogLevel startupLogLevel({
  required Map<String, String> env,
  required Map<String, Object?> file,
  required String appVersion,
  String define = logLevelDefine,
}) =>
    resolveLogLevel(
      env: env['PLAYHEADTV_LOG_LEVEL'],
      define: define,
      file: file,
      fallback: defaultLogLevelFor(appVersion),
    );

/// Configures [Log] for a real run: level from [startupLogLevel], file in the
/// install's state directory ([logDirectory]). Never throws.
void initLogging({
  required Map<String, String> env,
  required Map<String, Object?> file,
  required String appVersion,
  Iterable<String?> secrets = const [],
  Directory? directory,
}) {
  try {
    final level = startupLogLevel(env: env, file: file, appVersion: appVersion);
    Log.configure(level: level, directory: directory ?? logDirectory(env), secrets: secrets);
    Log.info('app', 'log started: version=$appVersion level=${level.label} os=${Platform.operatingSystem}');
  } catch (_) {}
}
