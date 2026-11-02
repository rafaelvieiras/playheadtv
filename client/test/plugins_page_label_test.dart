import 'dart:io';

import 'package:client/pages/plugins_page.dart';
import 'package:client/plugin/plugin_installer.dart';
import 'package:client/plugin/plugin_manifest.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  InstalledPluginRecord installed({bool enabled = true, String version = '1.0.0'}) => InstalledPluginRecord(
        id: 'fake',
        version: version,
        protocol: 1,
        name: 'Fake',
        description: '',
        entrypoint: 'plugin-fake',
        capabilities: const ['media.resolve'],
        sha256: 'a' * 64,
        os: 'linux',
        arch: 'amd64',
        enabled: enabled,
        directory: Directory('/tmp/does-not-matter'),
      );

  const offering = PluginOffering(
    id: 'fake',
    name: 'Fake',
    releases: [
      PluginRelease(version: '1.2.0', protocol: 1, artifacts: [
        PluginArtifact(os: 'linux', arch: 'amd64', url: '/a', sha256: 'b', size: 1),
      ]),
    ],
  );

  test('an installed plugin says so, with its version', () {
    final label = pluginStateLabel(id: 'fake', installed: installed(), offering: offering, platformHas: const {});
    expect(label, contains('1.0.0'));
    expect(label, contains('on'));
  });

  test('a turned-off plugin says that instead', () {
    final label = pluginStateLabel(
        id: 'fake', installed: installed(enabled: false), offering: offering, platformHas: const {});
    expect(label, contains('turned off'));
  });

  test('a download in progress reports progress, not the old state', () {
    final label = pluginStateLabel(
      id: 'fake',
      installed: installed(),
      offering: offering,
      platformHas: const {},
      progressPercent: 42,
    );
    expect(label, 'Downloading... 42%');
  });

  test('an error replaces whatever the row would have said', () {
    final label = pluginStateLabel(
      id: 'fake',
      installed: installed(),
      offering: offering,
      platformHas: const {},
      error: 'Install failed: archive hash mismatch',
    );
    expect(label, 'Install failed: archive hash mismatch');
  });

  test('a plugin this backend does not offer says so', () {
    final label = pluginStateLabel(id: 'fake', installed: null, offering: null, platformHas: const {});
    expect(label, 'Not offered by this backend');
  });

  test('an offering with no build for this device says so', () {
    final label = pluginStateLabel(id: 'fake', installed: null, offering: offering, platformHas: const {});
    expect(label, 'No build for this device');
  });

  test('an installable plugin names the version waiting', () {
    final label =
        pluginStateLabel(id: 'fake', installed: null, offering: offering, platformHas: const {'fake': '1.2.0'});
    expect(label, contains('Not installed'));
    expect(label, contains('1.2.0'));
  });
}
