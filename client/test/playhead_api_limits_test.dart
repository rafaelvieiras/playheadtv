// Unit test (mock HTTP client, no backend) for the request fan-out guards in
// PlayheadApi: bounded concurrency and per-id catalog item caching.
import 'dart:async';
import 'dart:convert';

import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

http.Response _item(String id) =>
    http.Response(jsonEncode({'id': id, 'external_id': 'ext-$id', 'title': 'Item $id', 'duration_ms': 1000}), 200);

void main() {
  test('never has more than maxConcurrentRequests requests in flight', () async {
    var inFlight = 0;
    var peak = 0;
    final client = MockClient((request) async {
      inFlight++;
      if (inFlight > peak) peak = inFlight;
      await Future<void>.delayed(const Duration(milliseconds: 20));
      inFlight--;
      return _item(request.url.pathSegments.last);
    });
    final api = PlayheadApi(baseUrl: 'http://example.test', client: client);

    final ids = List.generate(100, (i) => 'id-$i');
    final items = await Future.wait(ids.map(api.getCatalogItem));

    expect(items, hasLength(100));
    expect(peak, lessThanOrEqualTo(PlayheadApi.maxConcurrentRequests));
  });

  test('looks each catalog item up once, and does not cache failures', () async {
    var calls = 0;
    var fail = true;
    final client = MockClient((request) async {
      calls++;
      if (fail) return http.Response('boom', 500);
      return _item(request.url.pathSegments.last);
    });
    final api = PlayheadApi(baseUrl: 'http://example.test', client: client);

    await expectLater(api.getCatalogItem('a'), throwsA(isA<Exception>()));
    fail = false;
    await api.getCatalogItem('a');
    await api.getCatalogItem('a');
    await Future.wait([api.getCatalogItem('b'), api.getCatalogItem('b')]);

    expect(calls, 3, reason: 'failed a, ok a (cached after), one shared b');
  });
}
