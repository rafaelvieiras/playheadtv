import 'dart:convert';
import 'dart:io';

import 'package:client/plugin/plugin_installer.dart';
import 'package:client/plugin/plugin_process.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('handshake', () {
    test('confirms the plugin answers as itself', () async {
      final fake = await _FakePlugin.start();
      addTearDown(fake.dispose);

      final manifest = await fake.process.start();
      expect(manifest.id, 'fake');
      expect(manifest.version, '1.0.0');
      expect(manifest.has('media.resolve'), isTrue);
    });

    test('refuses a plugin that answers as another plugin', () async {
      final fake = await _FakePlugin.start(infoId: 'impostor');
      addTearDown(fake.dispose);

      await expectLater(fake.process.start(), throwsA(isA<PluginProcessException>()));
    });
  });

  group('resolve', () {
    test('returns a pair', () async {
      final fake = await _FakePlugin.start(
        // One line: the protocol delimiter is a newline, so an answer that
        // spans lines would never arrive.
        resolveJson: '{"mode":"pair","sources":['
            '{"type":"video","url":"https://cdn.example.test/v","mime":"video/mp4","expires_at":1793602656000},'
            '{"type":"audio","url":"https://cdn.example.test/a","mime":"audio/mp4"}]}',
      );
      addTearDown(fake.dispose);

      final resolution = await fake.process.resolve(config: const {'url': 'x'}, externalId: 'abc123');
      expect(resolution.isPair, isTrue);
      expect(resolution.isSingle, isFalse);
      expect(resolution.sources.first.type, 'video');
      expect(resolution.sources.first.expiresAt,
          DateTime.fromMillisecondsSinceEpoch(1793602656000, isUtc: true));
      expect(resolution.sources[1].url, Uri.parse('https://cdn.example.test/a'));
    });

    test('returns a single url', () async {
      final fake = await _FakePlugin.start(
        resolveJson: '{"mode":"single","url":"https://cdn.example.test/one.mp4","mime":"video/mp4"}',
      );
      addTearDown(fake.dispose);

      final resolution = await fake.process.resolve(config: const {'url': 'x'}, externalId: 'abc123');
      expect(resolution.isSingle, isTrue);
      expect(resolution.mime, 'video/mp4');
      expect(resolution.headers, isEmpty);
    });

    test('passes an error code through', () async {
      final fake = await _FakePlugin.start(
        resolveJson: '{"error":{"code":"rate_limited","message":"slow down"}}',
      );
      addTearDown(fake.dispose);

      await expectLater(
        fake.process.resolve(config: const {'url': 'x'}, externalId: 'abc123'),
        throwsA(isA<PluginError>()
            .having((e) => e.code, 'code', 'rate_limited')
            .having((e) => e.retryable, 'retryable', isTrue)),
      );
    });

    test('treats "not found" as permanent', () async {
      final fake = await _FakePlugin.start(
        resolveJson: '{"error":{"code":"not_found","message":"gone"}}',
      );
      addTearDown(fake.dispose);

      await expectLater(
        fake.process.resolve(config: const {'url': 'x'}, externalId: 'abc123'),
        throwsA(isA<PluginError>().having((e) => e.unsupported, 'unsupported', isTrue)),
      );
    });

    test('ignores noise the plugin prints on stdout', () async {
      final fake = await _FakePlugin.start(
        junk: 'this is not json',
        resolveJson: '{"mode":"single","url":"https://cdn.example.test/one.mp4"}',
      );
      addTearDown(fake.dispose);

      final resolution = await fake.process.resolve(config: const {'url': 'x'}, externalId: 'abc123');
      expect(resolution.isSingle, isTrue);
    });

    test('kills a plugin that misses its bound', () async {
      final fake = await _FakePlugin.start(hangOnResolve: true, callTimeout: const Duration(milliseconds: 400));
      addTearDown(fake.dispose);

      await fake.process.start();
      await expectLater(
        fake.process.resolve(config: const {'url': 'x'}, externalId: 'abc123'),
        throwsA(isA<PluginProcessException>()),
      );
      expect(fake.process.isRunning, isFalse);
    });
  });
}

/// A plugin written as a shell script: the protocol is one JSON object per line,
/// so a script is enough to exercise the client's side of it. The answers live in
/// files, so nothing has to survive shell quoting.
class _FakePlugin {
  final Directory dir;
  final PluginProcess process;

  _FakePlugin(this.dir, this.process);

  static Future<_FakePlugin> start({
    String infoId = 'fake',
    String resolveJson = '{"mode":"single","url":"https://cdn.example.test/one.mp4"}',
    String junk = '',
    bool hangOnResolve = false,
    Duration callTimeout = const Duration(seconds: 5),
  }) async {
    final dir = await Directory.systemTemp.createTemp('ph-plugin-proc');
    final manifest = {
      'id': infoId,
      'name': 'Fake',
      'version': '1.0.0',
      'protocol': 1,
      'entrypoint': 'plugin-fake',
      'capabilities': ['catalog.list', 'media.resolve'],
    };
    final manifestJson = jsonEncode(manifest);
    await File('${dir.path}/plugin.json').writeAsString(manifestJson);

    // @ID@ is replaced by the script, so one template answers every request.
    // Each answer ends with a newline: a line-oriented protocol cannot deliver a
    // final line that never ends.
    await File('${dir.path}/info.body').writeAsString('{"id":@ID@,"result":$manifestJson}\n');
    final resolves = resolveJson.contains('"error"')
        // An error travels in its own member, at the top level of the line.
        ? '{"id":@ID@,${resolveJson.substring(1)}'
        : '{"id":@ID@,"result":$resolveJson}';
    await File('${dir.path}/resolve.body').writeAsString('$resolves\n');
    if (junk.isNotEmpty) {
      await File('${dir.path}/junk.body').writeAsString('$junk\n');
    }

    if (hangOnResolve) {
      await File('${dir.path}/hang').writeAsString('');
    }
    // Raw string: the script is shell, not Dart, so nothing here is escaped.
    const script = r'''#!/bin/sh
dir=$(dirname "$0")
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *media.resolve*) body=resolve ;;
    *info*) body=info ;;
    *shutdown*) printf '{"id":%s,"result":{}}\n' "$id"; exit 0 ;;
    *) body=info ;;
  esac
  if [ "$body" = resolve ] && [ -f "$dir/hang" ]; then sleep 60; fi
  if [ -f "$dir/junk.body" ]; then cat "$dir/junk.body"; fi
  sed "s/@ID@/$id/" "$dir/$body.body"
done
''';
    final executable = File('${dir.path}/plugin-fake');
    await executable.writeAsString(script);
    await Process.run('chmod', ['0755', executable.path]);

    final record = InstalledPluginRecord(
      id: 'fake',
      version: '1.0.0',
      protocol: 1,
      name: 'Fake',
      description: '',
      entrypoint: 'plugin-fake',
      capabilities: const ['catalog.list', 'media.resolve'],
      sha256: 'a' * 64,
      os: 'linux',
      arch: 'amd64',
      enabled: true,
      directory: dir,
    );
    return _FakePlugin(dir, PluginProcess(record, callTimeout: callTimeout));
  }

  Future<void> dispose() async {
    await process.close();
    if (await dir.exists()) await dir.delete(recursive: true);
  }
}
