// The one logging entry point of the client and the updater. Pure Dart (no
// Flutter imports).
//
//   Log.info('player', 'channel loaded in ${ms}ms');
//   Log.error('player', 'media open failed', error: e, stack: st);
//
// Never use print/debugPrint in app code and never log a secret: the logger
// redacts the API key, the device token and Authorization values itself, but a
// message should not carry them in the first place. Levels:
//  - error: a failure the user could notice or a maintainer must see. No media
//    titles or paths.
//  - warn: something unexpected that was handled (a retry, a fallback). No
//    media titles or paths.
//  - info: lifecycle summaries (a channel loaded, a sync finished).
//  - debug: step-by-step detail (preloads, crossfades, queue waits).
//  - trace: very chatty detail.
// Logging never throws, never blocks (file writes are buffered and drained by
// a timer) and never touches the widget tree. See docs/ARCHITECTURE.md "Client
// logging".
import 'dart:collection';
import 'dart:io';

import 'log_file.dart';
import 'log_level.dart';
import 'redact.dart';

export 'log_file.dart' show logBundleName, logFileName;
export 'log_level.dart';

class Log {
  Log._();

  static LogLevel _level = LogLevel.error;
  static LogLevel _configured = LogLevel.error;
  static LogFile? _file;
  static final Redactor _redactor = Redactor();
  static final Queue<String> _tail = Queue<String>();
  static const tailCapacity = 50;

  /// A longer message (a stack trace, an HTTP body) is cut here.
  static const maxMessageLength = 4000;
  static void Function(String line)? _extraSink;

  /// The level in force right now.
  static LogLevel get level => _level;

  /// The level the app started with (before any Settings change); what the
  /// level returns to at the next start.
  static LogLevel get configuredLevel => _configured;

  /// The directory of the log file, or null when logging to a file is off.
  static Directory? get directory => _file?.dir;

  /// The current log file, or null when logging to a file is off.
  static File? get file => _file?.file;

  /// True when a message at [at] would be written.
  static bool enabled(LogLevel at) => at.index <= _level.index;

  /// Sets up the logger. [level] is both the active and the configured level;
  /// [directory] null keeps the log in memory only (the tail). [secrets] are
  /// redacted wherever they appear. [extraSink] (tests) also receives every
  /// written line.
  static void configure({
    required LogLevel level,
    Directory? directory,
    Iterable<String?> secrets = const [],
    int maxBytes = defaultLogMaxBytes,
    int rotatedFiles = defaultLogRotatedFiles,
    Duration flushDelay = const Duration(milliseconds: 200),
    void Function(String line)? extraSink,
  }) {
    try {
      _level = level;
      _configured = level;
      _redactor.setSecrets(secrets);
      _tail.clear();
      _extraSink = extraSink;
      final old = _file;
      _file = directory == null
          ? null
          : LogFile(directory, maxBytes: maxBytes, rotated: rotatedFiles, flushDelay: flushDelay);
      if (old != null) {
        // Fire and forget; a failure to close an old file is irrelevant.
        old.close().catchError((Object _) {});
      }
    } catch (_) {}
  }

  /// Changes the active level for this run only (the Settings choice). The
  /// configured level, and so the next start, is untouched.
  static void setRuntimeLevel(LogLevel level) {
    final changed = _level != level;
    _level = level;
    if (changed && enabled(LogLevel.info)) _write(LogLevel.info, 'log', 'level set to ${level.label} until restart');
  }

  /// Replaces the secrets the logger redacts literally.
  static void setSecrets(Iterable<String?> secrets) {
    try {
      _redactor.setSecrets(secrets);
    } catch (_) {}
  }

  /// The last written lines (already redacted), oldest first, at most [count].
  static List<String> tail([int count = 10]) {
    try {
      final all = _tail.toList();
      return all.length <= count ? all : all.sublist(all.length - count);
    } catch (_) {
      return const [];
    }
  }

  static void error(String tag, Object message, {Object? error, StackTrace? stack}) =>
      _log(LogLevel.error, tag, message, error, stack);
  static void warn(String tag, Object message, {Object? error, StackTrace? stack}) =>
      _log(LogLevel.warn, tag, message, error, stack);
  static void info(String tag, Object message, {Object? error, StackTrace? stack}) =>
      _log(LogLevel.info, tag, message, error, stack);
  static void debug(String tag, Object message, {Object? error, StackTrace? stack}) =>
      _log(LogLevel.debug, tag, message, error, stack);
  static void trace(String tag, Object message, {Object? error, StackTrace? stack}) =>
      _log(LogLevel.trace, tag, message, error, stack);

  /// [message] is a String, or a `String Function()` that is only called when
  /// the level is enabled (use it where building the text costs something).
  static void _log(LogLevel at, String tag, Object message, Object? error, StackTrace? stack) {
    if (at.index > _level.index) return;
    try {
      var text = message is String Function() ? message() : '$message';
      if (error != null) text = '$text: $error';
      if (stack != null && (at == LogLevel.error || _level.index >= LogLevel.debug.index)) text = '$text\n$stack';
      _write(at, tag, text);
    } catch (_) {}
  }

  static void _write(LogLevel at, String tag, String text) {
    try {
      final stamp = DateTime.now().toUtc().toIso8601String();
      if (text.length > maxMessageLength) text = '${text.substring(0, maxMessageLength)}... (truncated)';
      final body = _redactor.redact(text).trimRight().replaceAll('\n', '\n    ');
      final line = '$stamp ${at.label.toUpperCase().padRight(5)} [$tag] $body';
      _tail.addLast(line);
      while (_tail.length > tailCapacity) {
        _tail.removeFirst();
      }
      _file?.add(line);
      _extraSink?.call(line);
    } catch (_) {}
  }

  /// Writes what is queued. Completes when it is on its way to disk.
  static Future<void> flush() async {
    try {
      await _file?.flush();
    } catch (_) {}
  }

  /// Flushes and closes the file (tests, and the app on exit).
  static Future<void> close() async {
    try {
      final f = _file;
      _file = null;
      await f?.close();
    } catch (_) {}
  }

  /// Concatenates the rotated logs and the current one into one `.txt` in the
  /// log directory. Returns it, or null when there is no log file or it could
  /// not be written.
  static Future<File?> exportBundle({String? appVersion}) async {
    try {
      final f = _file;
      if (f == null) return null;
      final header = '# Playhead TV log bundle. Secrets are redacted; review it before sharing.\n'
          '# version: ${appVersion ?? 'unknown'}, level: ${_level.label}, os: ${Platform.operatingSystem}';
      return await f.writeBundle(header: header);
    } catch (_) {
      return null;
    }
  }
}
