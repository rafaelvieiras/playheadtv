import 'dart:convert';
import 'dart:io';

import 'package:client/playhead_api.dart';
import 'package:client/plugin/plugin_installer.dart';
import 'package:client/plugin/plugin_manager.dart';
import 'package:client/plugin/plugin_manifest.dart';
import 'package:client/plugin/plugin_process.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

void main() {
  group('registry', () {
    test('is fetched once and reused', () async {
      var fetches = 0;
      final manager = await _manager(onRegistry: () => fetches++);

      await manager.registry();
      await manager.registry();
      expect(fetches, 1);

      await manager.registry(refresh: true);
      expect(fetches, 2);
    });

    test('reports a backend with no registry as such', () async {
      final manager = await _manager(
        registryJson: '{"registry_configured":false,"installed":[],"plugins":[]}',
      );
      final registry = await manager.registry();
      expect(registry.registryConfigured, isFalse);
      expect(registry.plugins, isEmpty);
    });
  });

  group('install', () {
    test('refuses a plugin the registry does not offer', () async {
      final manager = await _manager();
      await expectLater(manager.install('youtube'), throwsA(isA<PluginFormatException>()));
    });

    test('refuses a plugin with no build for this device', () async {
      final manager = await _manager(registryJson: _registryWithArtifact(os: 'beos', arch: 'vax'));
      await expectLater(
        manager.install('fake'),
        throwsA(isA<PluginFormatException>().having((e) => e.message, 'message', contains('no build'))),
      );
    });
  });

  group('missingFor', () {
    test('is empty when nothing is required', () async {
      final manager = await _manager();
      expect(await manager.missingFor(const []), isEmpty);
    });

    test('names a plugin that is not installed', () async {
      final manager = await _manager();
      final missing = await manager.missingFor(const [EpgRequirement(id: 'fake')]);
      expect(missing.single.id, 'fake');
      expect(missing.single.installedVersion, isNull);
      expect(missing.single.describe(), contains('not installed'));
    });

    test('accepts an installed plugin at or above the minimum', () async {
      final manager = await _manager();
      await _seedInstalled(manager, version: '1.4.0');
      expect(await manager.missingFor(const [EpgRequirement(id: 'fake', minVersion: '1.2.0')]), isEmpty);
      expect(await manager.missingFor(const [EpgRequirement(id: 'fake')]), isEmpty);
    });

    test('names an installed plugin that is too old', () async {
      final manager = await _manager();
      await _seedInstalled(manager, version: '1.0.0');
      final missing = await manager.missingFor(const [EpgRequirement(id: 'fake', minVersion: '1.2.0')]);
      expect(missing.single.tooOld, isTrue);
      expect(missing.single.describe(), contains('1.2.0'));
    });

    test('keeps the strictest minimum when entries disagree', () async {
      final manager = await _manager();
      await _seedInstalled(manager, version: '1.1.0');
      final missing = await manager.missingFor(const [
        EpgRequirement(id: 'fake', minVersion: '1.2.0'),
        EpgRequirement(id: 'fake', minVersion: '1.5.0'),
        EpgRequirement(id: 'fake'),
      ]);
      expect(missing, hasLength(1));
      expect(missing.single.minVersion, '1.5.0');
    });

    test('names a plugin that is installed but turned off', () async {
      final manager = await _manager();
      await _seedInstalled(manager, version: '1.0.0', enabled: false);
      final missing = await manager.missingFor(const [EpgRequirement(id: 'fake')]);
      expect(missing.single.disabled, isTrue);
      expect(missing.single.describe(), contains('turned off'));
    });

    test('says a plugin has no build here when the registry proves it', () async {
      final manager = await _manager(registryJson: _registryWithArtifact(os: 'beos', arch: 'vax'));
      final missing = await manager.missingFor(const [EpgRequirement(id: 'fake')]);
      expect(missing.single.unsupportedHere, isTrue);
      expect(missing.single.describe(), contains('no build'));
    });
  });

  group('resolve', () {
    test('refuses when the plugin is not installed', () async {
      final manager = await _manager();
      await expectLater(
        manager.resolve(requirement: const EpgRequirement(id: 'fake'), externalId: 'abc'),
        throwsA(isA<PluginProcessException>()),
      );
    });

    test('refuses a plugin older than the entry needs', () async {
      final manager = await _manager();
      await _seedInstalled(manager, version: '1.0.0');
      await expectLater(
        manager.resolve(
          requirement: const EpgRequirement(id: 'fake', minVersion: '2.0.0'),
          externalId: 'abc',
        ),
        throwsA(isA<PluginProcessException>()),
      );
    });

    test('resolves through the installed plugin', () async {
      final manager = await _manager();
      await _seedInstalled(manager, version: '1.0.0', script: true);
      addTearDown(manager.close);

      final resolution = await manager.resolve(
        requirement: const EpgRequirement(id: 'fake'),
        config: const {'url': 'https://example.test/channel'},
        externalId: 'abc',
      );
      expect(resolution.isSingle, isTrue);
      expect(resolution.url, Uri.parse('https://cdn.example.test/one.mp4'));
    });

    test('reuses one process for repeated resolves', () async {
      final manager = await _manager();
      final record = await _seedInstalled(manager, version: '1.0.0', script: true);
      addTearDown(manager.close);

      final first = await manager.resolve(requirement: const EpgRequirement(id: 'fake'), externalId: 'a');
      final second = await manager.resolve(requirement: const EpgRequirement(id: 'fake'), externalId: 'b');
      expect(first.isSingle && second.isSingle, isTrue);
      // The script writes its pid to a file, so a second spawn would show up.
      final pidFile = File('${record.directory.path}/pid.log');
      expect(pidFile.readAsLinesSync(), hasLength(1));
    });
  });
}

const _registry = '{"registry_configured":true,"installed":[],"plugins":['
    '{"id":"fake","name":"Fake","description":"","versions":['
    '{"version":"1.0.0","protocol":1,"artifacts":['
    '{"os":"linux","arch":"amd64","url":"/plugins/fake/1.0.0/linux-amd64","sha256":"${_sha}","size":10}]},'
    '{"version":"1.2.0","protocol":1,"artifacts":['
    '{"os":"linux","arch":"amd64","url":"/plugins/fake/1.2.0/linux-amd64","sha256":"${_sha}","size":10}]}]}]}';

const _sha = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';

String _registryWithArtifact({required String os, required String arch}) =>
    '{"registry_configured":true,"installed":[],"plugins":['
    '{"id":"fake","name":"Fake","versions":[{"version":"1.0.0","protocol":1,"artifacts":['
    '{"os":"$os","arch":"$arch","url":"/plugins/fake/1.0.0/$os-$arch","sha256":"$_sha","size":10}]}]}]}';

Future<PluginManager> _manager({
  String registryJson = _registry,
  void Function()? onRegistry,
}) async {
  final root = await Directory.systemTemp.createTemp('ph-plugin-manager');
  addTearDown(() async {
    if (await root.exists()) await root.delete(recursive: true);
  });
  final client = MockClient((request) async {
    onRegistry?.call();
    return http.Response(registryJson, 200);
  });
  return PluginManager(
    backend: Uri.parse('http://backend.test:9394'),
    authHeaders: () => const {'Authorization': 'Bearer test'},
    installer: PluginInstaller(
      root: root,
      client: client,
      platform: () async => const PluginPlatform('linux', 'amd64'),
    ),
    client: client,
  );
}

/// Puts a plugin on disk the way a successful install would have.
Future<InstalledPluginRecord> _seedInstalled(
  PluginManager manager, {
  required String version,
  bool enabled = true,
  bool script = false,
}) async {
  final root = manager.installer.root;
  final dir = Directory('${root.path}${Platform.pathSeparator}fake');
  await dir.create(recursive: true);

  final manifest = {
    'id': 'fake',
    'name': 'Fake',
    'version': version,
    'protocol': 1,
    'entrypoint': 'plugin-fake',
    'capabilities': ['media.resolve'],
  };
  await File('${dir.path}/plugin.json').writeAsString(jsonEncode(manifest));

  final record = InstalledPluginRecord(
    id: 'fake',
    version: version,
    protocol: 1,
    name: 'Fake',
    description: '',
    entrypoint: 'plugin-fake',
    capabilities: const ['media.resolve'],
    sha256: _sha,
    os: 'linux',
    arch: 'amd64',
    enabled: enabled,
    directory: dir,
  );
  await File('${dir.path}/installed.json').writeAsString(jsonEncode(record.toJson()));

  if (script) {
    // Logs its own pid once: a second spawn appends a second line.
    await File('${dir.path}/resolve.body').writeAsString(
        '{"id":@ID@,"result":{"mode":"single","url":"https://cdn.example.test/one.mp4"}}\n');
    await File('${dir.path}/info.body')
        .writeAsString('{"id":@ID@,"result":${jsonEncode(manifest)}}\n');
    const body = r'''#!/bin/sh
dir=$(dirname "$0")
echo $$ >> "$dir/pid.log"
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *media.resolve*) body=resolve ;;
    *shutdown*) printf '{"id":%s,"result":{}}\n' "$id"; exit 0 ;;
    *) body=info ;;
  esac
  sed "s/@ID@/$id/" "$dir/$body.body"
done
''';
    final executable = File('${dir.path}/plugin-fake');
    await executable.writeAsString(body);
    await Process.run('chmod', ['0755', executable.path]);
  }
  return record;
}
