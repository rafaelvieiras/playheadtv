// Talking to an installed plugin: one JSON object per line over its
// stdin/stdout (protocol 1, docs/PLUGINS.md).
//
// The client only ever needs two calls — `info`, to confirm what it installed,
// and `media.resolve`, to turn one guide item into something the player can
// open. It never lists a catalog: the backend owns the grid.
//
// A plugin is a child process of this app, so its output must never reach
// protocol parsing by accident, its answers must never be trusted blindly, and
// a call that does not answer must not wedge the player: every call has a bound
// and a missed bound kills the process.
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'plugin_installer.dart';
import 'plugin_manifest.dart';

/// A resolve answer, in whichever shape the plugin could give.
class PluginResolution {
  /// `single`, `pair` or `remux`.
  final String mode;

  /// Set for `single` and `remux`.
  final Uri? url;
  final String mime;

  /// Set for `pair`: one video track and one audio track, same instant.
  final List<PluginSource> sources;

  final Map<String, String> headers;
  final DateTime? expiresAt;

  /// True for a url the plugin produces as it plays (mode `remux`): a player may
  /// seek back into what exists, but forward only as fast as it is produced.
  final bool live;

  const PluginResolution({
    required this.mode,
    this.url,
    this.mime = '',
    this.sources = const [],
    this.headers = const {},
    this.expiresAt,
    this.live = false,
  });

  /// A single openable url, when the plugin answered with one.
  bool get isSingle => url != null && sources.isEmpty;

  /// Two tracks the player has to merge itself.
  bool get isPair => sources.length == 2;
}

/// One track of a resolved pair.
class PluginSource {
  final String type; // video | audio
  final Uri url;
  final String mime;
  final Map<String, String> headers;
  final DateTime? expiresAt;

  const PluginSource({
    required this.type,
    required this.url,
    this.mime = '',
    this.headers = const {},
    this.expiresAt,
  });
}

/// A plugin answered with an error it named (`config`, `not_found`, ...).
class PluginError implements Exception {
  final String code;
  final String message;
  const PluginError(this.code, this.message);

  /// Temporary or upstream: asking again later may work.
  bool get retryable => code == 'unavailable' || code == 'rate_limited';

  /// The plugin cannot serve this at all, and retrying will not change that.
  bool get unsupported => code == 'unsupported' || code == 'not_found';

  @override
  String toString() => 'PluginError($code): $message';
}

/// The plugin process is not running, or stopped while a call was in flight.
class PluginProcessException implements Exception {
  final String message;
  const PluginProcessException(this.message);
  @override
  String toString() => 'PluginProcessException: $message';
}

/// One running plugin.
class PluginProcess {
  final InstalledPluginRecord plugin;
  final Duration infoTimeout;
  final Duration callTimeout;

  Process? _process;
  final _pending = <int, Completer<Map<String, dynamic>>>{};
  final _stderr = <String>[];
  int _nextId = 1;
  Future<PluginManifest>? _starting;

  PluginProcess(
    this.plugin, {
    this.infoTimeout = const Duration(seconds: 20),
    this.callTimeout = const Duration(seconds: 90),
  });

  bool get isRunning => _process != null;

  /// Starts the process if it is not already up, then confirms it speaks a
  /// protocol this client knows and answers as itself.
  Future<PluginManifest> start() {
    final existing = _starting;
    if (existing != null) return existing;
    final started = _start();
    _starting = started;
    // A failed start must not be cached: the next call may well succeed (the
    // plugin was reinstalled, or the machine was simply busy).
    started.then((_) {}, onError: (Object _) {
      if (identical(_starting, started)) _starting = null;
    });
    return started;
  }

  Future<PluginManifest> _start() async {
    if (_process != null) {
      return PluginManifest.fromJson(await _call('info', const {}, infoTimeout));
    }
    if (!plugin.executable.existsSync()) {
      throw PluginProcessException('${plugin.id} is installed but ${plugin.executable.path} is missing');
    }

    final process = await Process.start(
      plugin.executable.path,
      const [],
      workingDirectory: plugin.directory.path,
    );
    _process = process;
    _stderr.clear();

    process.stdout
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .listen(_onLine, onDone: _onExit, onError: (_) => _onExit());
    process.stderr.transform(utf8.decoder).transform(const LineSplitter()).listen((line) {
      // Diagnostics only: never protocol data. Kept short, for the Settings page
      // and the log.
      _stderr.add(line);
      if (_stderr.length > 40) _stderr.removeAt(0);
    });

    final manifest = PluginManifest.fromJson(await _call('info', const {}, infoTimeout));
    manifest.validate();
    if (manifest.id != plugin.id) {
      await close();
      throw PluginProcessException('${plugin.id} answered as "${manifest.id}"');
    }
    if (manifest.version != plugin.version) {
      await close();
      throw PluginProcessException('${plugin.id} answered as version ${manifest.version}, installed ${plugin.version}');
    }
    return manifest;
  }

  /// Resolves one item into something the player can open.
  ///
  /// [modes] is what this client's player can actually do. The pair mode is the
  /// default because it costs the plugin nothing; `remux` is only ever offered
  /// where the engine cannot merge two tracks, and never on a small device where
  /// the plugin producing a stream would compete with playback itself.
  Future<PluginResolution> resolve({
    required Map<String, dynamic> config,
    required String externalId,
    int? durationMs,
    int startAtMs = 0,
    List<String> modes = const ['single', 'pair'],
  }) async {
    await start();
    final result = await _call('media.resolve', {
      'config': config,
      'item': {
        'external_id': externalId,
        'duration_ms': ?durationMs,
      },
      if (startAtMs > 0) 'start_at_ms': startAtMs,
      'modes': modes,
    }, callTimeout);
    return _parseResolution(result);
  }

  PluginResolution _parseResolution(Map<String, dynamic> result) {
    final mode = result['mode'] is String ? result['mode'] as String : '';
    final headers = _stringMap(result['headers']);
    final expires = _epochMillis(result['expires_at']);

    if (result['url'] is String) {
      return PluginResolution(
        mode: mode.isEmpty ? 'single' : mode,
        url: _absolute(result['url'] as String),
        mime: result['mime'] as String? ?? '',
        headers: headers,
        expiresAt: expires,
        live: result['live'] == true,
      );
    }

    final sources = (result['sources'] as List? ?? const [])
        .whereType<Map<String, dynamic>>()
        .map((src) => PluginSource(
              type: src['type'] as String? ?? '',
              url: _absolute(src['url'] as String? ?? ''),
              mime: src['mime'] as String? ?? '',
              headers: _stringMap(src['headers']),
              expiresAt: _epochMillis(src['expires_at']),
            ))
        .where((src) => src.url.hasScheme)
        .toList(growable: false);

    if (sources.isEmpty) {
      throw const PluginProcessException('resolved neither a url nor a pair of sources');
    }
    return PluginResolution(mode: mode.isEmpty ? 'pair' : mode, mime: result['mime'] as String? ?? '', sources: sources);
  }

  /// Sends one request and waits for its answer.
  Future<Map<String, dynamic>> _call(String method, Map<String, dynamic> params, Duration timeout) async {
    final process = _process;
    if (process == null) {
      throw const PluginProcessException('plugin is not running');
    }
    final id = _nextId++;
    final completer = Completer<Map<String, dynamic>>();
    _pending[id] = completer;

    try {
      process.stdin.writeln(jsonEncode({'id': id, 'method': method, 'params': params}));
      await process.stdin.flush();
    } on Object catch (error) {
      _pending.remove(id);
      throw PluginProcessException('cannot write to the plugin: $error');
    }

    try {
      return await completer.future.timeout(timeout, onTimeout: () {
        _pending.remove(id);
        // A plugin that misses its bound is killed rather than left behind: the
        // next call starts a fresh process, and nothing else waits on this one.
        unawaited(close());
        throw PluginProcessException('$method did not answer within ${timeout.inSeconds}s');
      });
    } on PluginError {
      rethrow;
    }
  }

  void _onLine(String line) {
    if (line.trim().isEmpty) return;
    Object? decoded;
    try {
      decoded = jsonDecode(line);
    } on FormatException {
      // Anything on stdout that is not protocol traffic is a plugin bug; it is
      // dropped rather than allowed to break a caller.
      return;
    }
    if (decoded is! Map<String, dynamic>) return;
    final id = decoded['id'];
    if (id is! int) return;
    final completer = _pending.remove(id);
    if (completer == null) return;

    final error = decoded['error'];
    if (error is Map<String, dynamic>) {
      completer.completeError(PluginError(
        error['code'] as String? ?? 'unavailable',
        error['message'] as String? ?? 'the plugin reported an error',
      ));
      return;
    }
    final result = decoded['result'];
    completer.complete(result is Map<String, dynamic> ? result : <String, dynamic>{});
  }

  void _onExit() {
    _process = null;
    _starting = null;
    final pending = List.of(_pending.entries);
    _pending.clear();
    for (final entry in pending) {
      entry.value.completeError(PluginProcessException(
        '${plugin.id} stopped before answering',
      ));
    }
  }

  /// Last lines the plugin wrote to stderr, for a diagnostics view. Never shown
  /// raw to a viewer: it is plugin output, not ours.
  List<String> get diagnostics => List.unmodifiable(_stderr);

  /// Shuts the plugin down politely, then stops it.
  Future<void> close() async {
    final process = _process;
    _process = null;
    _starting = null;
    if (process == null) return;
    try {
      process.stdin.writeln(jsonEncode({'id': _nextId++, 'method': 'shutdown', 'params': const {}}));
      await process.stdin.flush();
      await process.stdin.close();
    } on Object {
      // Already gone: killing below is all that is left to do.
    }
    await process.exitCode.timeout(const Duration(seconds: 2), onTimeout: () {
      process.kill(ProcessSignal.sigkill);
      return -1;
    });
  }

  static Uri _absolute(String raw) {
    final trimmed = raw.trim();
    if (trimmed.isEmpty) return Uri();
    final uri = Uri.tryParse(trimmed);
    if (uri == null) return Uri();
    if (uri.hasScheme) return uri;
    // A relative segment path refers to the plugin's own output file.
    return File(trimmed).absolute.uri;
  }

  static Map<String, String> _stringMap(Object? value) {
    if (value is! Map) return const {};
    final out = <String, String>{};
    value.forEach((key, val) {
      if (key is String && val is String) out[key] = val;
    });
    return out;
  }

  static DateTime? _epochMillis(Object? value) {
    final millis = value is num ? value.toInt() : int.tryParse('$value');
    if (millis == null || millis <= 0) return null;
    return DateTime.fromMillisecondsSinceEpoch(millis, isUtc: true);
  }
}
