import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'package:client/pages/server_url_page.dart';
import 'package:client/services/settings_service.dart';

Future<List<Uri>> _pump(WidgetTester tester, {int versionStatus = 200}) async {
  final seen = <Uri>[];
  final client = MockClient((r) async {
    seen.add(r.url);
    if (r.url.path == '/version') return http.Response('{"version":"dev","api_version":1}', versionStatus);
    // The pairing page the flow continues to.
    return http.Response('{"error":"x"}', 500);
  });
  tester.view.physicalSize = const Size(1920, 1080);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(MaterialApp(home: ServerUrlPage(httpClient: client)));
  await tester.pump(const Duration(milliseconds: 300));
  return seen;
}

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  testWidgets('normalises the typed address, probes /version and remembers the server', (tester) async {
    final seen = await _pump(tester);
    await tester.enterText(find.byKey(const Key('server-url-field')), ' server.example.com:9394/ ');
    await tester.ensureVisible(find.byKey(const Key('server-url-continue-button')));
    await tester.pump();
    await tester.tap(find.byKey(const Key('server-url-continue-button')), warnIfMissed: true);
    await tester.pump(const Duration(milliseconds: 300));
    expect(seen.map((u) => u.toString()), contains('http://server.example.com:9394/version'));
    expect((await SettingsService.create()).chosenServerUrl, 'http://server.example.com:9394');
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('an address that is not a Playhead TV server shows an error and stays', (tester) async {
    await _pump(tester, versionStatus: 404);
    await tester.enterText(find.byKey(const Key('server-url-field')), 'http://server.example.com:9394');
    await tester.tap(find.byKey(const Key('server-url-continue-button')));
    await tester.pump(const Duration(milliseconds: 200));
    expect(find.byKey(const Key('server-url-error')), findsOneWidget);
    expect(find.byKey(const Key('server-url-page')), findsOneWidget);
    expect((await SettingsService.create()).chosenServerUrl, isEmpty);
  });

  testWidgets('an empty address is refused without a request', (tester) async {
    final seen = await _pump(tester);
    await tester.tap(find.byKey(const Key('server-url-continue-button')));
    await tester.pump(const Duration(milliseconds: 100));
    expect(seen, isEmpty);
    expect(find.byKey(const Key('server-url-error')), findsOneWidget);
  });
}
