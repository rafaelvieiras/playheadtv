// Unit tests for the pure first-run routing matrix and the official/server URL
// resolver (client/lib/services/pairing_boot.dart).
import 'package:client/services/pairing_boot.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('decideBootRoute', () {
    test('configured goes home regardless of the URL', () {
      expect(decideBootRoute(configured: true, serverUrl: ''), BootRoute.home);
      expect(decideBootRoute(configured: true, serverUrl: 'http://example.test'), BootRoute.home);
    });

    test('not configured with a known server goes to pairing', () {
      expect(decideBootRoute(configured: false, serverUrl: 'http://example.test'), BootRoute.pairing);
      expect(decideBootRoute(configured: false, serverUrl: 'https://example.test:8443/base'), BootRoute.pairing);
      expect(decideBootRoute(configured: false, serverUrl: 'example.test'), BootRoute.pairing);
    });

    test('not configured without a server asks for one', () {
      expect(decideBootRoute(configured: false, serverUrl: ''), BootRoute.serverUrl);
      expect(decideBootRoute(configured: false, serverUrl: '   '), BootRoute.serverUrl);
    });

    test('a hostile server value counts as unknown', () {
      expect(decideBootRoute(configured: false, serverUrl: 'javascript:alert(1)'), BootRoute.serverUrl);
      expect(decideBootRoute(configured: false, serverUrl: 'file:///etc/passwd'), BootRoute.serverUrl);
      expect(decideBootRoute(configured: false, serverUrl: 'ftp://example.test'), BootRoute.serverUrl);
    });
  });

  group('ServerUrlResolver', () {
    test('a stored URL wins over the build-time official URL', () {
      expect(
        ServerUrlResolver.knownServerUrl(storedBackendUrl: 'https://stored.example.test', officialDefine: 'https://official.example.test'),
        'https://stored.example.test',
      );
    });

    test('the official define is used when nothing is stored', () {
      expect(
        ServerUrlResolver.knownServerUrl(storedBackendUrl: '', officialDefine: 'https://official.example.test'),
        'https://official.example.test',
      );
      expect(ServerUrlResolver.officialUrl(define: 'https://official.example.test'), 'https://official.example.test');
      expect(ServerUrlResolver.officialUrl(define: ''), '');
    });

    test('a hostile stored value falls through to the official URL', () {
      expect(
        ServerUrlResolver.knownServerUrl(storedBackendUrl: 'javascript:alert(1)', officialDefine: 'https://official.example.test'),
        'https://official.example.test',
      );
    });

    test('normalize adds http://, trims and drops trailing slashes', () {
      expect(ServerUrlResolver.normalize('  example.test/  '), 'http://example.test');
      expect(ServerUrlResolver.normalize('https://example.test:8443/base/'), 'https://example.test:8443/base');
    });

    test('normalize rejects non-http(s) schemes, missing hosts and overlong values', () {
      expect(ServerUrlResolver.normalize('javascript:alert(1)'), '');
      expect(ServerUrlResolver.normalize('file:///etc/passwd'), '');
      expect(ServerUrlResolver.normalize('ftp://example.test'), '');
      expect(ServerUrlResolver.normalize('http://'), '');
      expect(ServerUrlResolver.normalize(''), '');
      final overlong = 'http://example.test/${List.filled(ServerUrlResolver.maxUrlLength + 1, 'a').join()}';
      expect(ServerUrlResolver.normalize(overlong), '');
    });
  });
}
