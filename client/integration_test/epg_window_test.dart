// E2E coverage for the enriched GET /epg and the rolling working set (boots
// the real app against a real Go backend, a fake video server and a counting
// proxy in front of the backend — see support/request_proxy.dart):
//
//  - a channel with hundreds of entries (a long tail of short clips) loads
//    with a handful of requests, titles and art references come with the
//    entries, and the guide shows the tail as ONE cell;
//  - the working set is the program on air plus the next N programs and
//    keeps rolling while the channel plays;
//  - against a backend without the new fields (the proxy's legacy mode) the
//    client falls back to per-item lookups, cached and bounded.
//
// Three real app boots only, on purpose (see AGENTS.md: many boots per file
// can stall a later one). Every key sent is one of the six baseline buttons
// (Up/Down/Left/Right/Select/Back = Enter held or tapped, Escape).
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/request_proxy.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('enriched EPG + rolling window (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;
    RequestProxy? proxy;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    tearDown(() async {
      await proxy?.stop();
      proxy = null;
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
    });

    Future<void> boot(
      WidgetTester tester, {
      required String channelId,
      required bool legacy,
      required int workingSetSize,
      Duration? epgFetchWindow,
      Duration? epgExtendThreshold,
    }) async {
      proxy = RequestProxy(backendBaseUrl: backend.baseUrl, legacy: legacy);
      await proxy!.start();
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: proxy!.baseUrl,
        playheadDeviceToken: deviceToken,
        playheadChannelId: channelId,
        workingSetSize: workingSetSize,
        epgFetchWindow: epgFetchWindow,
        epgExtendThreshold: epgExtendThreshold,
      );
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelId);
      await tester.pumpWidget(const PlayheadSpikeApp());
    }

    Future<void> pumpUntil(
      WidgetTester tester,
      bool Function() condition, {
      Duration timeout = const Duration(seconds: 20),
      String? failMessage,
    }) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met within $timeout');
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    String? osdTitle(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-item-title'));
      if (finder.evaluate().isEmpty) return null;
      return tester.widget<Text>(finder).data;
    }

    bool osdVisible(WidgetTester tester) {
      final finder = find.ancestor(of: find.byKey(const Key('channel-osd')), matching: find.byType(AnimatedOpacity));
      if (finder.evaluate().isEmpty) return false;
      return tester.widget<AnimatedOpacity>(finder).opacity == 1;
    }

    // Select (OSD hidden) shows the OSD, a second Select opens the full EPG.
    Future<void> openEpg(WidgetTester tester) async {
      await pumpUntil(tester, () => !osdVisible(tester), timeout: const Duration(seconds: 10), failMessage: 'OSD never auto-hid');
      await tester.sendKeyEvent(LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Select did not show the OSD');
      await tester.sendKeyEvent(LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-details-panel')).evaluate().isNotEmpty,
        timeout: const Duration(seconds: 30),
        failMessage: 'EPG never finished loading',
      );
    }

    List<Widget> cellsOf(String channelId) => find
        .byWidgetPredicate((w) => w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-cell-$channelId-') &&
            !(w.key as ValueKey<String>).value.startsWith('epg-cell-progress-'))
        .evaluate()
        .map((e) => e.widget)
        .toList();

    String describe(RequestProxy p) => p.requests.map((r) => r.toString()).join('\n  ');

    testWidgets('hundreds of entries load with a handful of requests; titles and art need no lookups; the tail is one cell', (tester) async {
      final channelId = await backend.createChannel(number: 31, name: 'Window big');
      final ms = await backend.createLocalFolderMediaSource(name: 'Window big source');
      final ids = await backend.syncCatalogItems(ms, [
        {
          'external_id': 'big-a',
          'kind': 'episode',
          'title': 'PROGRAM A',
          'series_title': 'The Show',
          'series_external_id': 'big-show',
          'season_number': 1,
          'episode_number': 2,
          'image_primary_tag': 'tagA',
          'series_image_primary_tag': 'tagS',
          'duration_ms': 30000,
        },
        {'external_id': 'big-b', 'kind': 'movie', 'title': 'PROGRAM B', 'duration_ms': 8000},
        for (var i = 0; i < 3; i++) {'external_id': 'big-clip-$i', 'kind': 'movie', 'title': 'TAIL CLIP $i', 'duration_ms': 5000},
      ]);
      final blockA = await backend.createBlockWithItems([ids[0]]);
      final blockB = await backend.createBlockWithItems([ids[1]]);
      final now = DateTime.now().toUtc();
      await backend.createScheduleWithItems(
        channelId: channelId,
        items: [
          (blockId: blockA, startAt: now.subtract(const Duration(seconds: 3))),
          (blockId: blockB, startAt: now.add(const Duration(minutes: 40))),
        ],
      );
      await backend.createTailRule(channelId: channelId, catalogItemIds: ids.sublist(2));
      await backend.waitForEpgEntries(channelId, 400);

      await boot(tester, channelId: channelId, legacy: false, workingSetSize: 1);
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'never started playing');
      final p = proxy!;

      // Title with series data, straight from the enriched entry.
      expect(osdTitle(tester), 'The Show · S1E2 · PROGRAM A');

      // One GET /epg for the whole channel load, no per-item lookups at all.
      expect(p.to('/epg'), hasLength(1), reason: describe(p));
      expect(p.to('/epg').single.query['group_tail'], '1');
      expect(p.to('/catalog-items'), isEmpty, reason: describe(p));
      expect(p.requests.length, lessThanOrEqualTo(8), reason: 'a handful of requests for a channel of hundreds of entries:\n  ${describe(p)}');
      // ignore: avoid_print
      print('REQUESTS for the channel load (${p.requests.length}):\n  ${describe(p)}');

      // Working set = program on air + next N (N = 1): program A, then the
      // WHOLE tail (one program carrying every clip), and nothing beyond it.
      expect(channel.map((u) => u.programOrdinal).toSet(), {0, 1});
      expect(channel.first.id, 'big-a');
      expect(channel.length, greaterThan(400), reason: 'the tail group carries all of its clips');
      final tail = channel.last.program!;
      expect(tail.isTailGroup, isTrue);
      expect(tail.displayTitle, 'Tail');
      expect(channel[1].programLabel, 'Tail');

      // The guide shows the tail as one cell, still without lookups.
      await openEpg(tester);
      expect(p.to('/epg'), hasLength(2));
      expect(p.to('/catalog-items'), isEmpty, reason: describe(p));
      final cells = cellsOf(channelId);
      expect(cells.length, lessThanOrEqualTo(4), reason: 'A, one tail cell, B (never one cell per clip)');
      expect(find.text('Tail'), findsWidgets);

      // Art: long-press Select on the focused (live) program expands the
      // details; the poster URL is built from the media server address and
      // the tag the backend sent.
      await tester.sendKeyDownEvent(LogicalKeyboardKey.enter);
      await tester.pump(const Duration(milliseconds: 700));
      await tester.sendKeyUpEvent(LogicalKeyboardKey.enter);
      await tester.pump();
      expect(find.byKey(const Key('epg-details-panel-expanded')), findsOneWidget);
      final art = find.byWidgetPredicate((w) => w is Image && w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-details-art-'));
      expect(art, findsOneWidget);
      final url = ((tester.widget<Image>(art).image) as NetworkImage).url;
      expect(url, startsWith('${video.baseUrl}/Items/big-a/Images/Primary?tag=tagA'));
      expect(p.to('/catalog-items'), isEmpty, reason: describe(p));

      await tester.sendKeyEvent(LogicalKeyboardKey.escape);
      await tester.pump();
    });

    testWidgets('the working set rolls forward while the channel plays and the schedule is fetched in slices', (tester) async {
      final channelId = await backend.createChannel(number: 32, name: 'Window roll');
      final ms = await backend.createLocalFolderMediaSource(name: 'Window roll source');
      final ids = await backend.syncCatalogItems(ms, [
        for (var i = 0; i < 10; i++) {'external_id': 'roll-$i', 'kind': 'episode', 'title': 'ROLL $i', 'duration_ms': 6000},
      ]);
      final block = await backend.createBlockWithItems(ids);
      await backend.createScheduleOnce(
        channelId: channelId,
        blockId: block,
        startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 2)),
      );
      await backend.waitForEpgEntries(channelId, 8);

      const fetchWindow = Duration(seconds: 12);
      await boot(
        tester,
        channelId: channelId,
        legacy: false,
        workingSetSize: 1,
        epgFetchWindow: fetchWindow,
        epgExtendThreshold: const Duration(seconds: 8),
      );
      int? currentProgram() {
        final t = osdTitle(tester);
        if (t == null || !t.startsWith('ROLL ')) return null;
        return int.parse(t.substring(5));
      }

      await pumpUntil(tester, () => currentProgram() != null, failMessage: 'never started playing');
      final p = proxy!;

      // Not hours up front: the first slice is exactly the configured window.
      final first = p.to('/epg').first.query;
      expect(int.parse(first['end']!) - int.parse(first['start']!), fetchWindow.inMilliseconds);

      var worstLead = 0;
      await pumpUntil(
        tester,
        () {
          final k = currentProgram();
          if (k == null) return false;
          final lead = channel.last.programOrdinal - k;
          if (lead > worstLead) worstLead = lead;
          expect(lead, lessThanOrEqualTo(1), reason: 'never more than the on-air program + N=1 in the working set');
          return k >= 4;
        },
        timeout: const Duration(seconds: 60),
        failMessage: 'never reached ROLL 4 (last shown: ${osdTitle(tester)})',
      );

      // The window really rolled: later slices were fetched, each starting
      // further ahead than the previous one, and the working set kept
      // exactly one program ahead of the one on air.
      final epgRequests = p.to('/epg');
      expect(epgRequests.length, greaterThanOrEqualTo(2), reason: describe(p));
      final starts = [for (final r in epgRequests) int.parse(r.query['start']!)];
      for (var i = 1; i < starts.length; i++) {
        expect(starts[i], greaterThan(starts[i - 1]));
      }
      expect(worstLead, 1);
      expect(p.to('/catalog-items'), isEmpty, reason: describe(p));
      // ignore: avoid_print
      print('ROLLING: ${epgRequests.length} /epg requests, starts relative to the first: '
          '${[for (final s in starts) '+${((s - starts.first) / 1000).round()}s']}');
    });

    testWidgets('against a backend without the new fields it falls back to per-item lookups, cached and bounded', (tester) async {
      final channelId = await backend.createChannel(number: 33, name: 'Window legacy');
      final ms = await backend.createLocalFolderMediaSource(name: 'Window legacy source');
      final ids = await backend.syncCatalogItems(ms, [
        {'external_id': 'leg-a', 'kind': 'episode', 'title': 'LEGACY A', 'duration_ms': 30000},
        {'external_id': 'leg-b', 'kind': 'movie', 'title': 'LEGACY B', 'duration_ms': 8000},
        for (var i = 0; i < 3; i++) {'external_id': 'leg-clip-$i', 'kind': 'movie', 'title': 'LEGACY CLIP $i', 'duration_ms': 5000},
      ]);
      final blockA = await backend.createBlockWithItems([ids[0]]);
      final blockB = await backend.createBlockWithItems([ids[1]]);
      final now = DateTime.now().toUtc();
      await backend.createScheduleWithItems(
        channelId: channelId,
        items: [
          (blockId: blockA, startAt: now.subtract(const Duration(seconds: 3))),
          (blockId: blockB, startAt: now.add(const Duration(minutes: 20))),
        ],
      );
      await backend.createTailRule(channelId: channelId, catalogItemIds: ids.sublist(2));
      await backend.waitForEpgEntries(channelId, 200);

      await boot(tester, channelId: channelId, legacy: true, workingSetSize: 1);
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'never started playing against the legacy backend');
      final p = proxy!;

      expect(osdTitle(tester), 'LEGACY A', reason: 'the title came from the per-item fallback');
      expect(p.to('/epg'), hasLength(1));
      // Fallback lookups only for the working set (A + the next program, one
      // flat tail clip), each id once.
      final lookups = p.to('/catalog-items/');
      expect(lookups, isNotEmpty, reason: 'the fallback must actually have been exercised');
      expect(lookups.length, lessThanOrEqualTo(3), reason: describe(p));
      expect(lookups.map((r) => r.path).toSet().length, lookups.length, reason: 'cached per id');
      expect(channel.map((u) => u.programOrdinal).toSet(), {0, 1});
      expect(channel.every((u) => u.program?.isTailGroup != true), isTrue, reason: 'a legacy backend sends no groups');

      // The guide still works: its rows (every channel of this shared
      // backend, hundreds of flat entries over a few distinct items) resolve
      // through the same cache, so each catalog item is looked up once, not
      // once per row — and never more than the bounded concurrency at a time.
      await openEpg(tester);
      final all = p.to('/catalog-items/').map((r) => r.path).toList();
      expect(all.toSet().length, all.length, reason: 'cached per id across the player and the guide:\n  ${describe(p)}');
      expect(all.length, lessThan(40), reason: 'one per distinct item, far below the hundreds of rows');
      expect(find.text('LEGACY CLIP 0'), findsWidgets);
      await tester.sendKeyEvent(LogicalKeyboardKey.escape);
      await tester.pump();
    });
  });
}
