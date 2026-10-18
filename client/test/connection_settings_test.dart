import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import 'package:client/config/connection_probe.dart';
import 'package:client/config/connection_settings.dart';

void main() {
  group('normalizeUrl', () {
    test('adds http:// when no scheme was typed', () {
      expect(normalizeUrl('192.168.1.10:8096'), 'http://192.168.1.10:8096');
    });
    test('keeps an explicit scheme and drops trailing slashes and spaces', () {
      expect(normalizeUrl('  https://media.example.com//  '), 'https://media.example.com');
    });
    test('empty stays empty', () {
      expect(normalizeUrl('   '), '');
    });
  });

  group('ConnectionSettings.validate', () {
    const ok = ConnectionSettings(
      jellyfinUrl: 'media.example.com:8096',
      jellyfinApiKey: ' key ',
      playheadBaseUrl: 'backend.example.com:9394',
    );
    test('accepts typed values that only need normalising', () {
      expect(ok.validate(), isNull);
      expect(ok.normalized().jellyfinApiKey, 'key');
      expect(ok.normalized().isComplete, isTrue);
    });
    test('requires the media server URL, key and backend URL, and names the field', () {
      expect(const ConnectionSettings().validate(), contains('Media server URL'));
      expect(const ConnectionSettings(jellyfinUrl: 'a.example.com').validate(), contains('API key'));
      expect(
        const ConnectionSettings(jellyfinUrl: 'a.example.com', jellyfinApiKey: 'k').validate(),
        contains('Playhead TV URL'),
      );
    });
    test('rejects a non-http scheme', () {
      const bad = ConnectionSettings(
          jellyfinUrl: 'ftp://a.example.com', jellyfinApiKey: 'k', playheadBaseUrl: 'b.example.com');
      expect(bad.validate(), contains('http'));
    });
    test('an error never repeats the typed key', () {
      const bad = ConnectionSettings(jellyfinUrl: 'ftp://a.example.com', jellyfinApiKey: 'SECRETKEY');
      expect(bad.validate(), isNot(contains('SECRETKEY')));
    });
    test('the device token is optional', () {
      expect(ok.normalized().playheadDeviceToken, '');
      expect(ok.validate(), isNull);
    });
  });

  test('stored round trip', () {
    const value = ConnectionSettings(
        jellyfinUrl: 'http://a', jellyfinApiKey: 'k', playheadBaseUrl: 'http://b', playheadDeviceToken: 't');
    final back = ConnectionSettings.fromStored(value.toStored());
    expect(back.jellyfinUrl, 'http://a');
    expect(back.playheadDeviceToken, 't');
  });

  group('probes', () {
    test('media server ok reports its name and sends the key as a header', () async {
      late http.Request seen;
      final client = MockClient((r) async {
        seen = r;
        return http.Response('{"ServerName":"Home"}', 200);
      });
      final r = await probeMediaServer('http://m', 'key', client: client);
      expect(r.isOk, isTrue);
      expect(r.message, contains('Home'));
      expect(seen.headers['X-Emby-Token'], 'key');
      expect(seen.url.toString(), 'http://m/System/Info');
    });
    test('401 is "unauthorized", and the message does not leak the key', () async {
      final client = MockClient((_) async => http.Response('', 401));
      final r = await probeMediaServer('http://m', 'SECRETKEY', client: client);
      expect(r.status, ProbeStatus.unauthorized);
      expect(r.message, isNot(contains('SECRETKEY')));
    });
    test('404 does not look like the right server', () async {
      final client = MockClient((_) async => http.Response('', 404));
      expect((await probeBackend('http://b', '', client: client)).status, ProbeStatus.unexpected);
    });
    test('a connection error is unreachable and never throws', () async {
      final client = MockClient((_) async => throw http.ClientException('boom'));
      expect((await probeBackend('http://b', '', client: client)).status, ProbeStatus.unreachable);
    });
    test('backend counts channels and sends the token only when set', () async {
      final headers = <Map<String, String>>[];
      final client = MockClient((r) async {
        headers.add(r.headers);
        return http.Response('[{"id":"1"},{"id":"2"}]', 200);
      });
      final withToken = await probeBackend('http://b', 'tok', client: client);
      await probeBackend('http://b', '', client: client);
      expect(withToken.message, contains('2 channels'));
      expect(headers[0]['Authorization'], 'Bearer tok');
      expect(headers[1].containsKey('Authorization'), isFalse);
    });
  });
}
