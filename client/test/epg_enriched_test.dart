// Unit test (mock HTTP client, no backend) for the enriched GET /epg model:
// parsing of the optional fields and of a grouped tail, the fallback to
// per-item lookups against a backend without them, and the playable units a
// program turns into.
import 'dart:convert';

import 'package:client/main.dart' show unitsOfProgram;
import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

Map<String, dynamic> _plain(String id, int start, int end, {String kind = 'program'}) => {
      'id': 'e-$id',
      'kind': kind,
      'catalog_item_id': 'ci-$id',
      'start_at': start,
      'end_at': end,
    };

Map<String, dynamic> _enriched(String id, int start, int end) => {
      ..._plain(id, start, end),
      'external_id': 'jf-$id',
      'title': 'Title $id',
      'series_title': 'Show',
      'series_external_id': 'jf-show',
      'item_kind': 'episode',
      'season_number': 1,
      'episode_number': 2,
      'duration_ms': end - start,
      'image_primary_tag': 'pt',
      'series_image_primary_tag': 'spt',
    };

Map<String, dynamic> _group(int clips, {int start = 0, int clipMs = 10000}) => {
      'id': 'e-t0',
      'tenant_id': 't',
      'channel_id': 'c',
      'kind': 'tail',
      'title': 'Tail',
      'start_at': start,
      'end_at': start + clips * clipMs,
      'clips': [
        for (var i = 0; i < clips; i++)
          {
            'id': 'e-t$i',
            'catalog_item_id': 'ci-t${i % 3}',
            'external_id': 'jf-t${i % 3}',
            'title': 'Clip ${i % 3}',
            'start_at': start + i * clipMs,
            'end_at': start + (i + 1) * clipMs,
            'duration_ms': clipMs,
          },
      ],
    };

void main() {
  test('parses the optional enrichment and a grouped tail, tolerating their absence', () {
    final e = EpgEntry.fromJson(_enriched('a', 0, 1800000));
    expect(e.externalId, 'jf-a');
    expect(e.displayTitle, 'Show · S1E2 · Title a');
    expect(e.primaryArt, (itemId: 'jf-a', tag: 'pt'));
    expect(e.isResolved, isTrue);

    final bare = EpgEntry.fromJson(_plain('b', 0, 1000));
    expect(bare.externalId, isNull);
    expect(bare.isResolved, isFalse);
    expect(bare.primaryArt, isNull);
    expect(bare.displayTitle, '');

    final g = EpgEntry.fromJson(_group(300));
    expect(g.isTailGroup, isTrue);
    expect(g.clips, hasLength(300));
    expect(g.catalogItemId, 'ci-t0');
    expect(g.displayTitle, 'Tail');
    expect(g.isResolved, isTrue);
  });

  test('an episode falls back to its series art when it has none of its own', () {
    final e = EpgEntry.fromJson({..._enriched('a', 0, 1000)}..remove('image_primary_tag'));
    expect(e.primaryArt, (itemId: 'jf-show', tag: 'spt'));
  });

  test('getEpg asks for tail grouping only when told to, and one request returns everything', () async {
    final seen = <Uri>[];
    final client = MockClient((request) async {
      seen.add(request.url);
      return http.Response(jsonEncode([_enriched('a', 0, 1000), _group(500, start: 1000)]), 200);
    });
    final api = PlayheadApi(baseUrl: 'http://example.test', client: client);
    final t = DateTime.utc(2026);

    var entries = await api.getEpg('c', t, t.add(const Duration(hours: 1)), groupTail: true);
    expect(seen.single.queryParameters['group_tail'], '1');
    expect(entries.last.clips, hasLength(500));
    // Nothing else is needed to play or label any of it.
    final resolved = await Future.wait(entries.map(api.resolveCatalog));
    expect(seen, hasLength(1));
    expect(resolved.first.externalId, 'jf-a');

    seen.clear();
    entries = await api.getEpg('c', t, t.add(const Duration(hours: 1)));
    expect(seen.single.queryParameters.containsKey('group_tail'), isFalse);
  });

  test('without the new fields, resolveCatalog falls back to a cached, bounded per-item lookup', () async {
    var inFlight = 0;
    var peak = 0;
    final lookups = <String>[];
    final client = MockClient((request) async {
      if (request.url.path.startsWith('/catalog-items/')) {
        final id = request.url.pathSegments.last;
        lookups.add(id);
        inFlight++;
        if (inFlight > peak) peak = inFlight;
        await Future<void>.delayed(const Duration(milliseconds: 10));
        inFlight--;
        return http.Response(jsonEncode({'id': id, 'external_id': 'jf-$id', 'title': 'T $id', 'duration_ms': 1000}), 200);
      }
      // Old backend: plain rows only, a hundred of them over three items.
      return http.Response(jsonEncode([for (var i = 0; i < 100; i++) _plain('${i % 3}', i * 1000, (i + 1) * 1000)]), 200);
    });
    final api = PlayheadApi(baseUrl: 'http://example.test', client: client);
    final t = DateTime.utc(2026);

    final entries = await api.getEpg('c', t, t.add(const Duration(hours: 1)), groupTail: true);
    expect(entries.every((e) => !e.isTailGroup && e.externalId == null), isTrue);
    final resolved = await Future.wait(entries.map(api.resolveCatalog));
    expect(resolved.every((e) => e.externalId == 'jf-ci-${e.catalogItemId.substring(3)}'), isTrue);
    expect(resolved.first.displayTitle, 'T ci-0');
    expect(lookups.toSet(), {'ci-0', 'ci-1', 'ci-2'});
    expect(lookups, hasLength(3), reason: 'cached per id');
    expect(peak, lessThanOrEqualTo(PlayheadApi.maxConcurrentRequests));
  });

  test('unitsOfProgram: one unit per program, one per clip for a tail, trimmed and filtered', () {
    final single = EpgEntry.fromJson(_enriched('a', 0, 1000));
    final units = unitsOfProgram(single, 3);
    expect(units, hasLength(1));
    expect(units.single.id, 'jf-a');
    expect(units.single.programOrdinal, 3);
    expect(units.single.clipDuration, const Duration(seconds: 1));

    final group = EpgEntry.fromJson(_group(10));
    expect(unitsOfProgram(group, 0), hasLength(10));
    expect(unitsOfProgram(group, 0).every((u) => u.programLabel == 'Tail'), isTrue);
    // Tune-in 35s into the block: the first three clips already ended.
    final trimmed = unitsOfProgram(group, 0, notBefore: DateTime.fromMillisecondsSinceEpoch(35000, isUtc: true));
    expect(trimmed, hasLength(7));
    expect(trimmed.first.startAt.millisecondsSinceEpoch, 30000);
    expect(trimmed.first.programStartAt.millisecondsSinceEpoch, 0);

    // A zero-length one (bad metadata) never becomes a unit.
    expect(unitsOfProgram(EpgEntry.fromJson(_enriched('z', 5, 5)), 0), isEmpty);
    // Unresolved (no source id): nothing to play.
    expect(unitsOfProgram(EpgEntry.fromJson(_plain('x', 0, 1000)), 0), isEmpty);
  });
}
