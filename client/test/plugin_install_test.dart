import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:client/plugin/plugin_installer.dart';
import 'package:client/plugin/plugin_manifest.dart';
import 'package:crypto/crypto.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

void main() {
  group('registry document', () {
    test('parses what GET /plugins returns', () {
      final registry = PluginRegistry.fromJson(jsonDecode(_registryJson));

      expect(registry.registryConfigured, isTrue);
      expect(registry.installed.single.id, 'youtube');
      expect(registry.installed.single.enabled, isTrue);
      expect(registry.installed.single.capabilityList, ['catalog.list', 'media.resolve']);

      final offering = registry.offeringById('youtube')!;
      expect(offering.name, 'YouTube');
      expect(offering.releases.single.version, '1.4.0');
    });

    test('picks the release that has a build for this platform', () {
      final offering = PluginOffering.fromJson({
        'id': 'x',
        'name': 'X',
        'versions': [
          {
            'version': '1.0.0',
            'protocol': 1,
            'artifacts': [
              {'os': 'linux', 'arch': 'arm64', 'url': 'https://b/1', 'sha256': 'a' * 64, 'size': 1},
            ],
          },
          {
            'version': '1.2.0',
            'protocol': 1,
            'artifacts': [
              {'os': 'linux', 'arch': 'amd64', 'url': 'https://b/2', 'sha256': 'b' * 64, 'size': 1},
              {'os': 'linux', 'arch': 'arm64', 'url': 'https://b/3', 'sha256': 'c' * 64, 'size': 1},
            ],
          },
        ],
      });

      expect(offering.latestFor(const PluginPlatform('linux', 'arm64'))!.version, '1.2.0');
      expect(offering.latestFor(const PluginPlatform('linux', 'amd64'))!.version, '1.2.0');
      // No build for this platform: the plugin cannot run here, and the client
      // must say so instead of downloading something unusable.
      expect(offering.latestFor(const PluginPlatform('windows', 'arm64')), isNull);
    });

    test('refuses an artifact with no usable hash', () {
      expect(
        () => PluginArtifact.fromJson({'os': 'linux', 'arch': 'amd64', 'url': 'https://b/x', 'sha256': 'nope'}),
        throwsA(isA<PluginFormatException>()),
      );
      // A path the backend serves is fine; a relative guess is not.
      expect(
        () => PluginArtifact.fromJson({'os': 'linux', 'arch': 'amd64', 'url': 'relative/path', 'sha256': 'a' * 64}),
        throwsA(isA<PluginFormatException>()),
      );
      expect(
        PluginArtifact.fromJson(
                {'os': 'linux', 'arch': 'amd64', 'url': '/plugins/x/1/linux-amd64', 'sha256': 'a' * 64})
            .resolveAgainst(Uri.parse('http://backend.test')),
        Uri.parse('http://backend.test/plugins/x/1/linux-amd64'),
      );
    });

    test('orders versions by their numbers, not their text', () {
      expect(comparePluginVersions('2026.10.26', '2026.9.30'), greaterThan(0));
      expect(comparePluginVersions('1.0.0', '1.0.0'), 0);
      expect(comparePluginVersions('1.2', '1.10'), lessThan(0));
    });
  });

  group('manifest', () {
    test('accepts a well-formed manifest', () {
      PluginManifest.fromJson(_manifest('youtube')).validate();
    });

    test('rejects what the client would not survive', () {
      for (final broken in <Map<String, dynamic>>[
        _manifest('YouTube')..['id'] = 'YouTube',
        _manifest('x')..['version'] = '',
        _manifest('x')..['entrypoint'] = '../../etc/passwd',
        _manifest('x')..['entrypoint'] = '/usr/bin/sh',
        _manifest('x')..['protocol'] = 2,
        _manifest('x')..['capabilities'] = <String>[],
        _manifest('x')..['capabilities'] = ['media.download'],
      ]) {
        expect(
          () => PluginManifest.fromJson(broken).validate(),
          throwsA(isA<PluginFormatException>()),
          reason: 'accepted $broken',
        );
      }
    });
  });

  group('extractTarGz', () {
    test('writes the archive into the destination', () async {
      final dir = await Directory.systemTemp.createTemp('ph-plugin-test');
      addTearDown(() => dir.delete(recursive: true));

      final archive = File('${dir.path}/a.tar.gz');
      await archive.writeAsBytes(gzip.encode(_tar([
        _Entry.dir('bin/'),
        _Entry.file('plugin.json', utf8.encode(jsonEncode(_manifest('youtube')))),
        _Entry.file('bin/plugin', utf8.encode('#!/bin/sh\necho hi\n')),
      ])));

      final dest = Directory('${dir.path}/out');
      await dest.create();
      await extractTarGz(archive, dest);

      expect(File('${dest.path}/plugin.json').existsSync(), isTrue);
      expect(File('${dest.path}/bin/plugin').readAsStringSync(), contains('echo hi'));
      expect(Directory('${dest.path}/bin').existsSync(), isTrue);
    });

    test('refuses entries that would escape the destination', () async {
      final dir = await Directory.systemTemp.createTemp('ph-plugin-test');
      addTearDown(() => dir.delete(recursive: true));

      final archive = File('${dir.path}/evil.tar.gz');
      await archive.writeAsBytes(gzip.encode(_tar([
        _Entry.file('../escaped', utf8.encode('nope')),
        _Entry.file('plugin.json', utf8.encode(jsonEncode(_manifest('youtube')))),
      ])));

      final dest = Directory('${dir.path}/out');
      await dest.create();
      await extractTarGz(archive, dest);

      expect(File('${dir.path}/escaped').existsSync(), isFalse);
      // The rest of the archive is still read: one bad entry does not derail it.
      expect(File('${dest.path}/plugin.json').existsSync(), isTrue);
    });
  });

  group('install', () {
    test('verifies the hash and installs', () async {
      final fixture = await _fixture();
      addTearDown(fixture.dispose);

      final record = await fixture.installer.install(
        offering: fixture.offering,
        release: fixture.release,
        artifact: fixture.artifact,
        backend: Uri.parse('http://backend.test'),
      );

      expect(record.id, 'youtube');
      expect(record.version, '1.4.0');
      expect(record.enabled, isTrue);
      expect(record.executable.existsSync(), isTrue);
      expect(File('${record.directory.path}/installed.json').existsSync(), isTrue);

      final listed = await fixture.installer.installed();
      expect(listed.single.id, 'youtube');
    });

    test('refuses an archive whose bytes do not match the registry', () async {
      final fixture = await _fixture(mutateArchive: (bytes) => Uint8List.fromList([...bytes, 0]));
      addTearDown(fixture.dispose);

      await expectLater(
        fixture.installer.install(
          offering: fixture.offering,
          release: fixture.release,
          artifact: fixture.artifact,
          backend: Uri.parse('http://backend.test'),
        ),
        throwsA(isA<PluginFormatException>()),
      );
      expect(await fixture.installer.installed(), isEmpty);
    });

    test('refuses an archive that holds a different plugin', () async {
      final fixture = await _fixture(manifestId: 'someoneelse');
      addTearDown(fixture.dispose);

      await expectLater(
        fixture.installer.install(
          offering: fixture.offering,
          release: fixture.release,
          artifact: fixture.artifact,
          backend: Uri.parse('http://backend.test'),
        ),
        throwsA(isA<PluginFormatException>()),
      );
      expect(await fixture.installer.installed(), isEmpty);
    });

    test('keeps the previous install when an update fails', () async {
      final fixture = await _fixture();
      addTearDown(fixture.dispose);
      await fixture.installer.install(
        offering: fixture.offering,
        release: fixture.release,
        artifact: fixture.artifact,
        backend: Uri.parse('http://backend.test'),
      );

      final broken = await _fixture(
        root: fixture.root,
        mutateArchive: (bytes) => Uint8List.fromList([...bytes, 0]),
      );
      addTearDown(broken.dispose);
      await expectLater(
        broken.installer.install(
          offering: broken.offering,
          release: broken.release,
          artifact: broken.artifact,
          backend: Uri.parse('http://backend.test'),
        ),
        throwsA(isA<PluginFormatException>()),
      );

      expect((await fixture.installer.installed()).single.version, '1.4.0');
    });

    test('removes and toggles a plugin', () async {
      final fixture = await _fixture();
      addTearDown(fixture.dispose);
      await fixture.installer.install(
        offering: fixture.offering,
        release: fixture.release,
        artifact: fixture.artifact,
        backend: Uri.parse('http://backend.test'),
      );

      expect((await fixture.installer.setEnabled('youtube', false))!.enabled, isFalse);
      expect((await fixture.installer.installed()).single.enabled, isFalse);

      await fixture.installer.remove('youtube');
      expect(await fixture.installer.installed(), isEmpty);
    });
  });
}

final _registryJson = '''
{
  "registry_configured": true,
  "installed": [
    {"id": "youtube", "version": "1.4.0", "protocol": 1, "os": "linux", "arch": "amd64",
     "artifact_sha256": "${'a' * 64}", "entrypoint": "plugin-youtube",
     "capabilities": "catalog.list,media.resolve", "enabled": true,
     "installed_at": 1, "updated_at": 1}
  ],
  "plugins": [
    {"id": "youtube", "name": "YouTube", "description": "Channels and videos.",
     "versions": [
       {"version": "1.4.0", "protocol": 1,
        "artifacts": [
          {"os": "linux", "arch": "amd64", "url": "/plugins/youtube/1.4.0/linux-amd64",
           "sha256": "${'a' * 64}", "size": 100}
        ]}
     ]}
  ]
}
''';

Map<String, dynamic> _manifest(String id) => {
      'id': id,
      'name': 'YouTube',
      'description': 'Channels, playlists and single videos.',
      'version': '1.4.0',
      'protocol': 1,
      'entrypoint': id == 'youtube' ? 'plugin-youtube' : 'plugin',
      'capabilities': ['catalog.list', 'media.resolve'],
    };

class _Fixture {
  final Directory root;
  final PluginInstaller installer;
  final PluginOffering offering;
  final PluginRelease release;
  final PluginArtifact artifact;

  _Fixture(this.root, this.installer, this.offering, this.release, this.artifact);

  Future<void> dispose() async {
    if (await root.exists()) await root.delete(recursive: true);
  }
}

Future<_Fixture> _fixture({
  Directory? root,
  String manifestId = 'youtube',
  Uint8List Function(Uint8List)? mutateArchive,
}) async {
  final base = root ?? await Directory.systemTemp.createTemp('ph-plugin-install');
  final archive = gzip.encode(_tar([
    _Entry.file('plugin.json', utf8.encode(jsonEncode(_manifest(manifestId)))),
    _Entry.file('plugin-youtube', utf8.encode('#!/bin/sh\necho ok\n')),
  ]));
  // The hash the registry announces is always the pristine archive's: a server
  // that serves something else must be caught.
  final pristine = Uint8List.fromList(archive);
  final sha = sha256.convert(pristine).toString();
  final bytes = mutateArchive == null ? pristine : mutateArchive(pristine);

  final offering = PluginOffering.fromJson({
    'id': 'youtube',
    'name': 'YouTube',
    'versions': [
      {
        'version': '1.4.0',
        'protocol': 1,
        'artifacts': [
          {
            'os': 'linux',
            'arch': 'amd64',
            'url': '/plugins/youtube/1.4.0/linux-amd64',
            'sha256': sha,
            'size': bytes.length,
          },
        ],
      },
    ],
  });
  final release = offering.releases.single;
  final artifact = release.artifacts.single;

  final client = MockClient((request) async => http.Response.bytes(bytes, 200));
  final installer = PluginInstaller(
    root: Directory('${base.path}/plugins'),
    client: client,
    platform: () async => const PluginPlatform('linux', 'amd64'),
  );
  return _Fixture(base, installer, offering, release, artifact);
}

class _Entry {
  final String name;
  final List<int> data;
  final int mode;
  final int type;

  const _Entry(this.name, this.data, this.mode, this.type);

  factory _Entry.file(String name, List<int> data, {int mode = 0x1ed}) => _Entry(name, data, mode, 0x30);
  factory _Entry.dir(String name) => _Entry(name, const [], 0x1ed, 0x35);
}

/// Builds a POSIX (ustar) tar archive, which is what the release script
/// produces. Written by hand so the tests need no extra dependency.
Uint8List _tar(List<_Entry> entries) {
  final builder = BytesBuilder();
  for (final entry in entries) {
    final header = Uint8List(512);
    void put(int offset, int length, String value) {
      final bytes = utf8.encode(value);
      header.setRange(offset, offset + (bytes.length > length ? length : bytes.length), bytes);
    }

    put(0, 100, entry.name);
    put(100, 8, entry.mode.toRadixString(8).padLeft(7, '0'));
    put(108, 8, '0000000');
    put(116, 8, '0000000');
    put(124, 12, entry.data.length.toRadixString(8).padLeft(11, '0'));
    put(136, 12, '00000000000');
    put(156, 1, String.fromCharCode(entry.type));
    put(257, 6, 'ustar');
    put(263, 2, '00');

    // The checksum is computed with its own field read as spaces.
    for (var i = 148; i < 156; i++) {
      header[i] = 0x20;
    }
    var sum = 0;
    for (final byte in header) {
      sum += byte;
    }
    put(148, 8, sum.toRadixString(8).padLeft(6, '0'));
    header[154] = 0;
    header[155] = 0x20;

    builder.add(header);
    if (entry.data.isNotEmpty) {
      builder.add(entry.data);
      final padding = (512 - entry.data.length % 512) % 512;
      if (padding > 0) builder.add(Uint8List(padding));
    }
  }
  builder.add(Uint8List(1024)); // two zero blocks end the archive
  return builder.toBytes();
}
