// Log levels and the rules that pick the active one. Pure Dart (no Flutter):
// the updater CLI uses the same names and precedence.

/// Severity, most severe first. A message is written when its level is at or
/// above (numerically at or below) the active level.
enum LogLevel {
  error,
  warn,
  info,
  debug,
  trace;

  /// The lowercase name used in config files, environment variables and the log
  /// itself.
  String get label => name;

  /// The name shown on screen.
  String get title => name[0].toUpperCase() + name.substring(1);
}

/// Parses a level name (case-insensitive, surrounding spaces ignored). Anything
/// else, including empty and null, is null: "not set".
LogLevel? parseLogLevel(Object? text) {
  if (text is! String) return null;
  final t = text.trim().toLowerCase();
  for (final level in LogLevel.values) {
    if (level.name == t) return level;
  }
  return null;
}

/// The active log level at startup. Precedence, first one that holds a valid
/// level wins (an unknown name is ignored, never an error):
///  1. the `PLAYHEADTV_LOG_LEVEL` environment variable ([env]),
///  2. the `--dart-define=PLAYHEADTV_LOG_LEVEL=` of the build ([define]),
///  3. the `log_level` key of the config file ([file]),
///  4. [fallback]: `error` for a packaged build, `debug` for a local one.
/// The Settings "Log level" choice is applied on top of this at run time and
/// is never persisted.
LogLevel resolveLogLevel({
  String? env,
  String define = '',
  Map<String, Object?> file = const {},
  required LogLevel fallback,
}) =>
    parseLogLevel(env) ?? parseLogLevel(define) ?? parseLogLevel(file['log_level']) ?? fallback;

/// The level a build uses when nothing sets one: `error` for a packaged build
/// (it carries a release version), a high level for a local one.
LogLevel defaultLogLevelFor(String appVersion) => appVersion == '0.0.0-dev' ? LogLevel.debug : LogLevel.error;

/// The levels the Settings "Log level" choice steps through.
const logLevelChoices = [LogLevel.error, LogLevel.warn, LogLevel.info, LogLevel.debug];

/// The choice after [current], wrapping around (trace, which only a build or
/// config can select, steps to the first).
LogLevel nextLogLevelChoice(LogLevel current) {
  final at = logLevelChoices.indexOf(current);
  return logLevelChoices[(at + 1) % logLevelChoices.length];
}

/// How Settings shows the active level: the name, and "until restart" when it
/// differs from the configured one.
String logLevelLabel(LogLevel active, LogLevel configured) =>
    active == configured ? active.title : '${active.title} (until the app restarts, then ${configured.title})';
