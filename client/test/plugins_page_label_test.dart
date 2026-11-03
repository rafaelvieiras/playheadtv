import 'package:client/pages/plugins_page.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('an installed plugin says so, with its version', () {
    final label = pluginStateLabel(id: 'fake', installedVersion: '1.0.0', installedName: 'Fake');
    expect(label, contains('1.0.0'));
    expect(label, contains('on'));
  });

  test('a turned-off plugin says that instead', () {
    final label = pluginStateLabel(id: 'fake', installedVersion: '1.0.0', enabled: false);
    expect(label, contains('turned off'));
  });

  test('an error replaces whatever the row would have said', () {
    expect(
      pluginStateLabel(id: 'fake', installedVersion: '1.0.0', error: 'Install failed: hash mismatch'),
      'Install failed: hash mismatch',
    );
  });

  test('a plugin this backend does not offer says so', () {
    expect(pluginStateLabel(id: 'fake', offered: false), 'Not offered by this backend');
  });

  test('an offering nobody can install on this backend says so', () {
    expect(pluginStateLabel(id: 'fake', offered: true), 'Not installable on this backend');
  });

  test('a plugin the backend could install names the version and who installs it', () {
    final label = pluginStateLabel(id: 'fake', offered: true, availableVersion: '1.2.0');
    expect(label, contains('Not installed here'));
    expect(label, contains('1.2.0'));
    // This page cannot install: saying "available" would read as "press Select".
    expect(label, contains('admin'));
  });
}
