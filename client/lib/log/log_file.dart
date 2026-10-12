// The log file: appended through a buffer that a short timer drains off the hot
// path, rotated by size. Pure Dart. Nothing here may throw to a caller.
import 'dart:async';
import 'dart:io';

/// The current file plus [rotated] older ones: `playheadtv.log`,
/// `playheadtv.log.1` (newest rotated), `playheadtv.log.2`, ...
const logFileName = 'playheadtv.log';
const logBundleName = 'playheadtv-log-bundle.txt';

/// Default size at which the current file is rotated, and how many rotated
/// files are kept: about 6 MB at most on disk.
const defaultLogMaxBytes = 2 * 1024 * 1024;
const defaultLogRotatedFiles = 2;

class LogFile {
  final Directory dir;
  final int maxBytes;
  final int rotated;
  final Duration flushDelay;

  LogFile(
    this.dir, {
    this.maxBytes = defaultLogMaxBytes,
    this.rotated = defaultLogRotatedFiles,
    this.flushDelay = const Duration(milliseconds: 200),
  });

  File get file => File('${dir.path}/$logFileName');
  File rotatedFile(int n) => File('${dir.path}/$logFileName.$n');

  // Lines waiting for the next drain. Bounded: a stuck disk must not grow memory.
  final StringBuffer _pending = StringBuffer();
  int _pendingBytes = 0;
  static const _maxPendingBytes = 1024 * 1024;
  Timer? _timer;
  Future<void>? _draining;
  IOSink? _sink;
  int _size = 0;
  bool _failed = false;
  bool _closed = false;

  /// Queues [line] (no trailing newline) for writing. Returns at once.
  void add(String line) {
    if (_closed || _failed) return;
    try {
      if (_pendingBytes > _maxPendingBytes) return; // drop: the disk is not keeping up
      _pending.writeln(line);
      _pendingBytes += line.length + 1;
      _timer ??= Timer(flushDelay, () {
        _timer = null;
        unawaited(flush());
      });
    } catch (_) {}
  }

  /// Writes everything queued so far; completes when it is on its way to disk.
  Future<void> flush() {
    final running = _draining;
    if (running != null) return running.then((_) => flush());
    if (_pendingBytes == 0) return Future.value();
    final future = _drain().whenComplete(() {
      _draining = null;
    });
    _draining = future;
    return future;
  }

  Future<void> _drain() async {
    try {
      final text = _pending.toString();
      _pending.clear();
      _pendingBytes = 0;
      var sink = _sink;
      if (sink == null) {
        dir.createSync(recursive: true);
        _size = file.existsSync() ? file.lengthSync() : 0;
        sink = file.openWrite(mode: FileMode.append);
        _sink = sink;
      }
      sink.write(text);
      _size += text.length; // close enough: the text is almost all ASCII
      await sink.flush();
      if (_size >= maxBytes) await _rotate();
    } catch (_) {
      // The disk is full or the directory is not writable: stop trying rather
      // than fail on every line. The in-memory tail keeps working.
      _failed = true;
      _pending.clear();
      _pendingBytes = 0;
      try {
        await _sink?.close();
      } catch (_) {}
      _sink = null;
    }
  }

  Future<void> _rotate() async {
    try {
      await _sink?.close();
    } catch (_) {}
    _sink = null;
    try {
      if (rotated > 0) {
        final oldest = rotatedFile(rotated);
        if (oldest.existsSync()) oldest.deleteSync();
        for (var n = rotated - 1; n >= 1; n--) {
          final f = rotatedFile(n);
          if (f.existsSync()) f.renameSync(rotatedFile(n + 1).path);
        }
        file.renameSync(rotatedFile(1).path);
      } else {
        file.deleteSync();
      }
    } catch (_) {}
    _size = 0;
  }

  /// Flushes and releases the file. Further lines are dropped.
  Future<void> close() async {
    _timer?.cancel();
    _timer = null;
    await flush();
    _closed = true;
    try {
      await _sink?.close();
    } catch (_) {}
    _sink = null;
  }

  /// The log files, oldest first (the current file last), those that exist.
  List<File> filesOldestFirst() => [
        for (var n = rotated; n >= 1; n--)
          if (rotatedFile(n).existsSync()) rotatedFile(n),
        if (file.existsSync()) file,
      ];

  /// Concatenates the rotated logs and the current one, oldest first, into
  /// [logBundleName] in the same directory (replaced each time) and returns it,
  /// or null when there is nothing to bundle or the write failed.
  Future<File?> writeBundle({String header = ''}) async {
    try {
      await flush();
      final out = File('${dir.path}/$logBundleName');
      final sink = out.openWrite();
      if (header.isNotEmpty) sink.writeln(header);
      for (final f in filesOldestFirst()) {
        await sink.addStream(f.openRead());
      }
      await sink.close();
      return out;
    } catch (_) {
      return null;
    }
  }
}
