import 'dart:io';

import 'package:client/log/log.dart';
import 'package:client/log/log_location.dart';
import 'package:client/log/log_setup.dart';
import 'package:client/log/redact.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  late Directory dir;

  setUp(() {
    dir = Directory.systemTemp.createTempSync('playheadtv_log_test_');
  });

  tearDown(() async {
    await Log.close();
    Log.configure(level: LogLevel.error);
    dir.deleteSync(recursive: true);
  });

  group('level parsing and precedence', () {
    test('parses names case-insensitively and rejects the rest', () {
      expect(parseLogLevel(' Debug '), LogLevel.debug);
      expect(parseLogLevel('TRACE'), LogLevel.trace);
      expect(parseLogLevel('verbose'), isNull);
      expect(parseLogLevel(''), isNull);
      expect(parseLogLevel(null), isNull);
      expect(parseLogLevel(3), isNull);
    });

    test('environment beats the build define beats the config file beats the default', () {
      const file = {'log_level': 'warn'};
      expect(resolveLogLevel(env: 'info', define: 'debug', file: file, fallback: LogLevel.error), LogLevel.info);
      expect(resolveLogLevel(define: 'debug', file: file, fallback: LogLevel.error), LogLevel.debug);
      expect(resolveLogLevel(file: file, fallback: LogLevel.error), LogLevel.warn);
      expect(resolveLogLevel(fallback: LogLevel.error), LogLevel.error);
    });

    test('an invalid value falls through to the next source', () {
      expect(
        resolveLogLevel(env: 'loud', define: 'nope', file: {'log_level': 7}, fallback: LogLevel.info),
        LogLevel.info,
      );
      expect(resolveLogLevel(env: 'loud', file: {'log_level': 'trace'}, fallback: LogLevel.info), LogLevel.trace);
    });

    test('packaged builds default to error, local builds to debug', () {
      expect(defaultLogLevelFor('1.4.0'), LogLevel.error);
      expect(defaultLogLevelFor('0.0.0-dev'), LogLevel.debug);
      expect(startupLogLevel(env: {}, file: {}, appVersion: '1.4.0', define: ''), LogLevel.error);
      expect(startupLogLevel(env: {}, file: {}, appVersion: '0.0.0-dev', define: ''), LogLevel.debug);
      expect(startupLogLevel(env: {'PLAYHEADTV_LOG_LEVEL': 'trace'}, file: {}, appVersion: '1.4.0', define: ''),
          LogLevel.trace);
    });
  });

  group('filtering', () {
    test('writes a message only at or above the active level', () {
      final lines = <String>[];
      Log.configure(level: LogLevel.warn, extraSink: lines.add);
      Log.error('t', 'e');
      Log.warn('t', 'w');
      Log.info('t', 'i');
      Log.debug('t', 'd');
      Log.trace('t', 'x');
      expect(lines, hasLength(2));
      expect(lines[0], contains('ERROR [t] e'));
      expect(lines[1], contains('WARN  [t] w'));
    });

    test('a lazy message is not built when the level is off', () {
      var built = 0;
      Log.configure(level: LogLevel.error);
      Log.debug('t', () {
        built++;
        return 'expensive';
      });
      expect(built, 0);
      Log.setRuntimeLevel(LogLevel.debug);
      Log.debug('t', () {
        built++;
        return 'expensive';
      });
      expect(built, 1);
    });

    test('the runtime level leaves the configured level alone', () {
      Log.configure(level: LogLevel.error);
      Log.setRuntimeLevel(LogLevel.trace);
      expect(Log.level, LogLevel.trace);
      expect(Log.configuredLevel, LogLevel.error);
    });

    test('the tail keeps the last lines', () {
      Log.configure(level: LogLevel.info);
      for (var i = 0; i < 80; i++) {
        Log.info('t', 'line $i');
      }
      final tail = Log.tail(10);
      expect(tail, hasLength(10));
      expect(tail.last, contains('line 79'));
      expect(tail.first, contains('line 70'));
    });
  });

  group('redaction', () {
    final r = Redactor()..setSecrets(['jf-secret-key-123', 'dev-token-456']);

    test('known secrets are removed wherever they appear', () {
      expect(r.redact('key jf-secret-key-123 and dev-token-456'), 'key $redactedMark and $redactedMark');
    });

    test('API key URL parameters, any spelling', () {
      for (final q in ['ApiKey', 'api_key', 'apikey', 'api-key', 'X-Emby-Token', 'access_token']) {
        final out = r.redact('GET http://jf/Videos/1/stream?$q=abc123def&Static=true');
        expect(out, isNot(contains('abc123def')), reason: q);
        expect(out, contains('Static=true'), reason: q);
      }
    });

    test('Authorization header values', () {
      expect(r.redact('Authorization: Bearer abc.def.ghi'), 'Authorization: $redactedMark');
      expect(r.redact('authorization=Basic Zm9vOmJhcg=='), 'authorization=$redactedMark');
      expect(r.redact('Authorization: MediaBrowser Client="x", Token="zzz999"'), 'Authorization: $redactedMark');
      expect(r.redact('{"Authorization": "Bearer abc", "x": 1}'), '{"Authorization": "$redactedMark", "x": 1}');
    });

    test('device token pattern and a url-encoded known secret', () {
      expect(r.redact('device_token=zzz999&x=1'), 'device_token=$redactedMark&x=1');
      final enc = Redactor()..setSecrets(['a b&c=d']);
      expect(enc.redact('q=${Uri.encodeComponent('a b&c=d')}'), 'q=$redactedMark');
    });

    test('ordinary text is untouched', () {
      const text = 'channel 7 loaded in 120ms (tokens: none) status=200';
      expect(r.redact(text), text);
    });

    test('the logger redacts, including the tail and the exception', () {
      Log.configure(level: LogLevel.error, secrets: ['jf-secret-key-123']);
      Log.error('net', 'failed http://jf/x?ApiKey=jf-secret-key-123', error: Exception('Authorization: Bearer q1w2'));
      final line = Log.tail(1).single;
      expect(line, isNot(contains('jf-secret-key-123')));
      expect(line, isNot(contains('q1w2')));
      expect(line, contains(redactedMark));
    });
  });

  group('file', () {
    test('lines are appended and flushed', () async {
      Log.configure(level: LogLevel.info, directory: dir);
      Log.info('t', 'one');
      Log.info('t', 'two');
      await Log.flush();
      final text = File('${dir.path}/$logFileName').readAsStringSync();
      expect(text, contains('[t] one'));
      expect(text, contains('[t] two'));
      expect(text.indexOf('one'), lessThan(text.indexOf('two')));
    });

    test('rotates by size keeping the current file and two rotated ones', () async {
      Log.configure(level: LogLevel.info, directory: dir, maxBytes: 1000);
      for (var i = 0; i < 40; i++) {
        Log.info('t', 'entry number $i ${'x' * 60}');
        await Log.flush();
      }
      final names = dir.listSync().map((e) => e.path.split('/').last).toSet();
      expect(names, {logFileName, '$logFileName.1', '$logFileName.2'});
      expect(File('${dir.path}/$logFileName').lengthSync(), lessThan(1100));
      // The newest entry is in the current file, the oldest ones are gone.
      expect(File('${dir.path}/$logFileName').readAsStringSync() + File('${dir.path}/$logFileName.1').readAsStringSync(),
          contains('entry number 39'));
      final all = names.map((n) => File('${dir.path}/$n').readAsStringSync()).join();
      expect(all, isNot(contains('entry number 0 ')));
    });

    test('the bundle concatenates rotated files oldest first', () async {
      Log.configure(level: LogLevel.info, directory: dir, maxBytes: 600);
      for (var i = 0; i < 12; i++) {
        Log.info('t', 'entry $i ${'y' * 80}');
        await Log.flush();
      }
      final bundle = await Log.exportBundle(appVersion: '1.2.3');
      expect(bundle, isNotNull);
      expect(bundle!.path.endsWith(logBundleName), isTrue);
      final text = bundle.readAsStringSync();
      expect(text, startsWith('# Playhead TV log bundle'));
      expect(text, contains('version: 1.2.3'));
      expect(text.indexOf('entry 10 '), lessThan(text.indexOf('entry 11 ')));
      expect(text, contains('entry 11 '));
    });

    test('never throws on an unusable directory or odd input', () async {
      // A file where the directory should be.
      final blocker = File('${dir.path}/blocker')..writeAsStringSync('x');
      Log.configure(level: LogLevel.trace, directory: Directory('${blocker.path}/sub'));
      Log.error('t', 'still fine');
      Log.error('t', Object(), error: _Throws(), stack: StackTrace.current);
      Log.debug('t', () => throw StateError('boom'));
      await Log.flush();
      await Log.exportBundle();
      expect(Log.tail(), isNotEmpty);
    });
  });

  group('location', () {
    test('install root, else the state directory, else nothing', () {
      expect(logDirectory({'PLAYHEADTV_ROOT': '/r', 'HOME': '/h'})!.path, '/r');
      expect(logDirectory({'HOME': '/h'})!.path, '/h/.local/state/playheadtv');
      expect(logDirectory({'XDG_STATE_HOME': '/s', 'HOME': '/h'})!.path, '/s/playheadtv');
      expect(logDirectory({}), isNull);
    });
  });
}

class _Throws {
  @override
  String toString() => throw StateError('toString failed');
}
