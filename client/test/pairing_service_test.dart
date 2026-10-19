// Unit tests for the public pairing client (client/lib/services/pairing_service.dart)
// against a MockClient — no backend. Covers the start response, the pending ->
// approved poll sequence, 404 expiry, malformed/hostile responses, and that
// secrets never appear in an error or a toString().
import 'dart:convert';

import 'package:client/services/pairing_service.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// JSON response helper (UTF-8 bytes so non-ASCII would survive too).
http.Response _json(Object body, [int status = 200]) => http.Response.bytes(
      utf8.encode(jsonEncode(body)),
      status,
      headers: const {'content-type': 'application/json'},
    );

/// Repeat a single character without Dart's missing string multiplication.
String _rep(String unit, int times) => List<String>.filled(times, unit).join();

Map<String, Object?> _validStart() => <String, Object?>{
      'pin': 'ABCD-EFGH',
      'poll_secret': _rep('a', 64),
      'expires_in': 600,
      'poll_interval': 3,
    };

Map<String, Object?> _approved() => <String, Object?>{
      'status': 'approved',
      'device_token': 'device-token',
      'jellyfin_url': 'https://media.example.com',
      'jellyfin_api_key': 'api-key',
    };

void main() {
  group('PairingApi.start', () {
    test('posts name/platform and returns the PIN, secret and intervals', () async {
      final client = MockClient((request) async {
        expect(request.method, 'POST');
        expect(request.url.path, '/pair');
        expect(request.headers['content-type'], contains('application/json'));
        final body = jsonDecode(request.body) as Map<String, Object?>;
        expect(body['name'], 'Living room TV');
        expect(body['platform'], 'android_tv');
        return _json(_validStart());
      });
      final api = PairingApi(baseUrl: 'http://example.test/', client: client);
      final result = await api.start(name: '  Living room TV  ', platform: 'android_tv');
      expect(result, isA<PairingStarted>());
      final started = result as PairingStarted;
      expect(started.pin, 'ABCD-EFGH');
      expect(started.pollSecret, _rep('a', 64));
      expect(started.expiresIn, const Duration(minutes: 10));
      expect(started.pollInterval, const Duration(seconds: 3));
      expect(api.baseUrl, 'http://example.test');
    });

    test('rejects an invalid base URL without calling the server', () async {
      var called = false;
      final api = PairingApi(
        baseUrl: 'javascript:alert(1)',
        client: MockClient((_) async {
          called = true;
          return _json(_validStart());
        }),
      );
      final result = await api.start(name: 'x', platform: 'linux');
      expect(result, isA<PairingStartFailure>());
      expect(called, isFalse);
    });

    test('rejects a non-200 response', () async {
      final api = PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => _json({'error': 'no'}, 429)));
      final result = await api.start(name: 'x', platform: 'linux');
      expect(result, isA<PairingStartFailure>());
      expect((result as PairingStartFailure).statusCode, 429);
    });

    test('rejects malformed and hostile start payloads', () async {
      final hostile = <Object>[
        'not json at all',
        _validStart()..['pin'] = 'IIII-IIII',
        _validStart()..['pin'] = 'ABCDEFGH',
        _validStart()..['poll_secret'] = _rep('a', 1000),
        _validStart()..['poll_secret'] = 42,
        _validStart()..['expires_in'] = '600',
        _validStart()..['expires_in'] = -1,
        _validStart()..['poll_interval'] = 0,
        _validStart()..['expires_in'] = 1 << 40,
      ];
      for (final body in hostile) {
        final response = body is String ? http.Response(body, 200) : _json(body);
        final api = PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => response));
        final result = await api.start(name: 'x', platform: 'linux');
        expect(result, isA<PairingStartFailure>(), reason: 'payload: $body');
      }
    });

    test('refuses an oversized response body', () async {
      final big = _json({'pin': 'ABCD-EFGH', 'poll_secret': _rep('a', 64), 'expires_in': 600, 'poll_interval': 3, 'pad': _rep('x', PairingApi.maxResponseBytes)});
      final api = PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => big));
      expect(await api.start(name: 'x', platform: 'linux'), isA<PairingStartFailure>());
    });

    test('says an older server lacks pairing, instead of "could not reach"', () async {
      for (final status in [401, 404, 405]) {
        final api = PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => _json({'error': 'x'}, status)));
        final result = await api.start(name: 'x', platform: 'linux');
        expect(result, isA<PairingStartFailure>(), reason: 'status $status');
        final message = (result as PairingStartFailure).message;
        expect(message, contains('does not support device pairing'), reason: 'status $status');
        expect(message, isNot(contains('Could not reach')));
      }
      final api = PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => _json({'error': 'x'}, 500)));
      expect(((await api.start(name: 'x', platform: 'linux')) as PairingStartFailure).message, contains('refused the request (500)'));
    });
    test('maps a transport failure to a safe message', () async {
      final api = PairingApi(
        baseUrl: 'http://example.test',
        client: MockClient((_) async => throw http.ClientException('boom')),
      );
      final result = await api.start(name: 'x', platform: 'linux');
      expect(result, isA<PairingStartFailure>());
      expect((result as PairingStartFailure).message, isNot(contains('boom')));
    });
  });

  group('PairingApi.poll', () {
    test('pending then approved follows the server sequence', () async {
      var calls = 0;
      final client = MockClient((request) async {
        calls++;
        expect(request.url.path, '/pair/poll');
        final body = jsonDecode(request.body) as Map<String, Object?>;
        expect(body['poll_secret'], _rep('a', 64));
        return calls == 1 ? _json({'status': 'pending'}) : _json(_approved());
      });
      final api = PairingApi(baseUrl: 'http://example.test', client: client);

      final first = await api.poll(_rep('a', 64));
      expect(first, isA<PairingPending>());
      expect((first as PairingPending).retryAfter, const Duration(seconds: 3));

      final second = await api.poll(_rep('a', 64));
      expect(second, isA<PairingApproved>());
      final approved = second as PairingApproved;
      expect(approved.deviceToken, 'device-token');
      expect(approved.jellyfinUrl, 'https://media.example.com');
      expect(approved.jellyfinApiKey, 'api-key');
      expect(calls, 2);
    });

    test('honours an explicit retryAfter on pending', () async {
      final api = PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => _json({'status': 'pending'})));
      final result = await api.poll('secret', retryAfter: const Duration(seconds: 7));
      expect((result as PairingPending).retryAfter, const Duration(seconds: 7));
    });

    test('404 means expired (unknown/expired/already delivered)', () async {
      final api = PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => _json({'error': 'gone'}, 404)));
      expect(await api.poll('secret'), isA<PairingExpired>());
    });

    test('an empty secret never calls the server', () async {
      var called = false;
      final api = PairingApi(
        baseUrl: 'http://example.test',
        client: MockClient((_) async {
          called = true;
          return _json({'status': 'pending'});
        }),
      );
      expect(await api.poll('   '), isA<PairingPollFailure>());
      expect(called, isFalse);
    });

    test('rejects hostile approved payloads', () async {
      final hostile = <Map<String, Object?>>[
        _approved()..['jellyfin_url'] = 'javascript:alert(1)',
        _approved()..['jellyfin_url'] = 'file:///etc/passwd',
        _approved()..['jellyfin_url'] = 'ftp://media.example.com',
        _approved()..['jellyfin_url'] = 'http://example.com/${_rep('a', 3000)}',
        _approved()..['device_token'] = '',
        _approved()..['device_token'] = _rep('t', 5000),
        _approved()..['jellyfin_api_key'] = _rep('k', 5000),
        _approved()..['device_token'] = 12345,
      ];
      for (final body in hostile) {
        final api = PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => _json(body)));
        expect(await api.poll('secret'), isA<PairingPollFailure>(), reason: 'payload: $body');
      }
    });

    test('rejects an unknown status and malformed bodies', () async {
      for (final response in [
        _json({'status': 'surprise'}),
        _json({'status': 'approved'}),
        http.Response('not json', 200),
      ]) {
        final api = PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => response));
        expect(await api.poll('secret'), isA<PairingPollFailure>());
      }
    });

    test('a non-200, non-404 status is a failure carrying the status', () async {
      final api = PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => _json({'error': 'busy'}, 503)));
      final result = await api.poll('secret');
      expect(result, isA<PairingPollFailure>());
      expect((result as PairingPollFailure).statusCode, 503);
    });
  });

  group('secret redaction', () {
    test('toString never contains the poll secret or the approved credentials', () async {
      final secret = _rep('a', 64);
      final started = await PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => _json(_validStart())))
          .start(name: 'x', platform: 'linux');
      expect(started.toString(), isNot(contains(secret)));

      final approved = await PairingApi(baseUrl: 'http://example.test', client: MockClient((_) async => _json(_approved())))
          .poll(secret);
      expect(approved.toString(), isNot(contains('device-token')));
      expect(approved.toString(), isNot(contains('api-key')));
    });
  });
}
