import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'package:client/config/connection_settings.dart';
import 'package:client/main.dart' show AppConfig;
import 'package:client/pages/setup_page.dart';
import 'package:client/services/settings_service.dart';

Future<void> _pump(WidgetTester tester, {bool firstRun = true, SetupSaved? onSaved, http.Client? client}) async {
  tester.view.physicalSize = const Size(1920, 1080);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(MaterialApp(
    home: SetupPage(firstRun: firstRun, onSaved: onSaved, httpClient: client),
  ));
  await tester.pumpAndSettle();
}

Future<void> _type(WidgetTester tester, String key, String text) async {
  await tester.enterText(find.byKey(Key(key)), text);
}

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
    AppConfig.applyStoredConnection(const ConnectionSettings());
  });

  testWidgets('saving with nothing typed names the first missing field and does not leave', (tester) async {
    var saved = false;
    await _pump(tester, onSaved: (_) => saved = true);
    await tester.tap(find.byKey(const Key('setup-save-button')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('setup-error')), findsOneWidget);
    expect(tester.widget<Text>(find.byKey(const Key('setup-error'))).data, contains('Media server URL'));
    expect(saved, isFalse);
  });

  testWidgets('valid values are normalised, stored and applied, then onSaved runs', (tester) async {
    var saved = false;
    await _pump(tester, onSaved: (_) => saved = true);
    await _type(tester, 'setup-jellyfin-url', '192.168.1.10:8096/');
    await _type(tester, 'setup-jellyfin-key', ' abc ');
    await _type(tester, 'setup-backend-url', 'http://192.168.1.10:9394');
    await tester.tap(find.byKey(const Key('setup-save-button')));
    await tester.pumpAndSettle();
    expect(saved, isTrue);
    final stored = (await SettingsService.create()).connection;
    expect(stored.jellyfinUrl, 'http://192.168.1.10:8096');
    expect(stored.jellyfinApiKey, 'abc');
    expect(AppConfig.isConnectionConfigured, isTrue);
  });

  testWidgets('test connection shows each server result', (tester) async {
    final client = MockClient((r) async => r.url.path == '/System/Info'
        ? http.Response('{"ServerName":"Home"}', 200)
        : http.Response('', 401));
    await _pump(tester, client: client);
    await _type(tester, 'setup-jellyfin-url', 'http://m');
    await _type(tester, 'setup-jellyfin-key', 'k');
    await _type(tester, 'setup-backend-url', 'http://b');
    await tester.tap(find.byKey(const Key('setup-test-button')));
    await tester.pumpAndSettle();
    expect(find.textContaining('Home'), findsOneWidget);
    expect(find.textContaining('device token'), findsOneWidget);
  });
}
