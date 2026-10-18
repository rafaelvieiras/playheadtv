import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'package:client/config/connection_settings.dart';
import 'package:client/main.dart' show AppConfig;
import 'package:client/pages/pairing_page.dart';
import 'package:client/services/settings_service.dart';

const _server = 'http://server.example.com:9394';

http.Response _json(Object body, [int status = 200]) =>
    http.Response(jsonEncode(body), status, headers: {'content-type': 'application/json'});

Map<String, Object> _started(String pin) =>
    {'pin': pin, 'poll_secret': 'a' * 64, 'expires_in': 600, 'poll_interval': 1};

Future<void> _pump(WidgetTester tester, http.Client client, {PairingDone? onDone}) async {
  tester.view.physicalSize = const Size(1920, 1080);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(MaterialApp(
    home: PairingPage(serverUrl: _server, httpClient: client, onDone: onDone),
  ));
  await tester.pump();
  await tester.pump(const Duration(milliseconds: 50));
}

String _pinShown(WidgetTester tester) => tester.widget<Text>(find.byKey(const Key('pairing-pin'))).data!;

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
    AppConfig.applyStoredConnection(const ConnectionSettings());
  });

  testWidgets('shows the PIN and a QR that encodes <server>/pin?code=<PIN>', (tester) async {
    final client = MockClient((r) async => r.url.path == '/pair' ? _json(_started('ABCD-EFGH')) : _json({'status': 'pending'}));
    await _pump(tester, client);
    expect(_pinShown(tester), 'ABCD-EFGH');
    expect(find.byKey(const ValueKey<String>('pairing-qr-data:$_server/pin?code=ABCD-EFGH')), findsOneWidget);
    // Leave no pending poll timer behind.
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('pending then approved stores the connection and calls onDone', (tester) async {
    var polls = 0;
    var done = false;
    final client = MockClient((r) async {
      if (r.url.path == '/pair') return _json(_started('ABCD-EFGH'));
      polls++;
      if (polls < 2) return _json({'status': 'pending'});
      return _json({
        'status': 'approved',
        'device_token': 'device-token-value',
        'jellyfin_url': 'http://jellyfin.example.com:8096',
        'jellyfin_api_key': 'jellyfin-key-value',
      });
    });
    await _pump(tester, client, onDone: (_) => done = true);
    for (var i = 0; i < 6 && !done; i++) {
      await tester.pump(const Duration(seconds: 1));
      await tester.pump(const Duration(milliseconds: 50));
    }
    expect(polls, greaterThanOrEqualTo(2));
    expect(done, isTrue);
    final stored = (await SettingsService.create()).connection;
    expect(stored.playheadBaseUrl, _server);
    expect(stored.playheadDeviceToken, 'device-token-value');
    expect(stored.jellyfinUrl, 'http://jellyfin.example.com:8096');
    expect(AppConfig.isConnectionConfigured, isTrue);
  });

  testWidgets('an expired pairing (poll 404) asks for a fresh PIN', (tester) async {
    var starts = 0;
    final client = MockClient((r) async {
      if (r.url.path == '/pair') {
        starts++;
        return _json(_started(starts == 1 ? 'AAAA-BBBB' : 'CCCC-DDDD'));
      }
      return _json({'error': 'not found'}, 404);
    });
    await _pump(tester, client);
    expect(_pinShown(tester), 'AAAA-BBBB');
    await tester.pump(const Duration(seconds: 1));
    await tester.pump(const Duration(milliseconds: 100));
    expect(starts, 2);
    expect(_pinShown(tester), 'CCCC-DDDD');
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('an invalid server response shows an error and no PIN', (tester) async {
    final client = MockClient((_) async => http.Response('<html>not the api</html>', 200));
    await _pump(tester, client);
    expect(find.byKey(const Key('pairing-error')), findsOneWidget);
    expect(find.byKey(const Key('pairing-pin')), findsNothing);
    expect(find.byKey(const Key('pairing-retry-button')), findsOneWidget);
  });

  testWidgets('only arrows and Select reach "Select another server"', (tester) async {
    final client = MockClient((r) async => r.url.path == '/pair' ? _json(_started('ABCD-EFGH')) : _json({'status': 'pending'}));
    await _pump(tester, client);
    for (var i = 0; i < 4; i++) {
      final focused = FocusManager.instance.primaryFocus;
      final label = focused?.debugLabel ?? '';
      if (label.contains('select-server')) break;
      await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
      await tester.pump();
    }
    await tester.sendKeyEvent(LogicalKeyboardKey.select);
    await tester.pump(const Duration(milliseconds: 100));
    await tester.pumpAndSettle(const Duration(milliseconds: 100));
    expect(find.byKey(const Key('server-url-page')), findsOneWidget);
  });
}
