// E2E coverage for the enriched GET /epg, as the offline cache's background
// sync consumes it, and the rolling working set (boots the real app against a
// real Go backend, a fake video server and a counting proxy in front of the
// backend — see support/request_proxy.dart):
//
//  - a channel with hundreds of entries (a long tail of short clips) is
//    downloaded by ONE request for every channel, titles and art references
//    come with the entries, the player and the guide read them from the cache
//    (no further request, no per-item lookups) and the guide hides the tail
//    (it only shows programs);
//  - the working set is the program on air plus the next N programs and
//    keeps rolling while the channel plays, read from the cache;
//  - against a backend without the new fields (the proxy's legacy mode) the
//    syncer falls back to per-item lookups, cached per id and bounded, before
//    the schedule is stored. That test has its OWN backend: the syncer
//    downloads every channel (no channel filter) and a legacy backend has no
//    tail groups, so a neighbouring channel with a long tail (tens of
//    thousands of flat entries over the window) would make the first sync take
//    longer than the boot's wait for it (startupSyncWait) and the channel
//    would start late, for reasons unrelated to what the test is about.
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
import 'support/key_focus.dart';
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
      BackendFixture? on,
      String? token,
      required String channelId,
      required bool legacy,
      required int workingSetSize,
    }) async {
      proxy = RequestProxy(backendBaseUrl: (on ?? backend).baseUrl, legacy: legacy);
      await proxy!.start();
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: proxy!.baseUrl,
        playheadDeviceToken: token ?? deviceToken,
        playheadChannelId: channelId,
        workingSetSize: workingSetSize,
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
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Select did not show the OSD');
      await pressKey(tester, LogicalKeyboardKey.enter);
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

    testWidgets('hundreds of entries load with a handful of requests; titles and art need no lookups; the guide hides the tail', (tester) async {
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

      // One GET /epg downloaded every channel (the syncer, no channel filter),
      // and no per-item lookups at all.
      expect(p.exact('/epg'), hasLength(1), reason: describe(p));
      expect(p.exact('/epg').single.query['group_tail'], '1');
      expect(p.exact('/epg').single.query.containsKey('channel_id'), isFalse);
      expect(p.to('/catalog-items'), isEmpty, reason: describe(p));
      expect(p.requests.length, lessThanOrEqualTo(10), reason: 'a handful of requests for a channel of hundreds of entries:\n  ${describe(p)}');
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

      // The guide hides the tail (no cell, no title) and needs no request at
      // all: it reads the cache.
      await openEpg(tester);
      expect(p.exact('/epg'), hasLength(1), reason: describe(p));
      expect(p.to('/catalog-items'), isEmpty, reason: describe(p));
      final cells = cellsOf(channelId);
      expect(cells.length, lessThanOrEqualTo(2), reason: 'A and B only: the tail has no cell, let alone one per clip');
      expect(find.descendant(of: find.byKey(const Key('epg-overlay')), matching: find.text('Tail')), findsNothing);

      // Art: long-press Select on the focused (live) program expands the
      // details; the poster URL is built from the media server address and
      // the tag the backend sent.
      await ensurePlayerFocus(tester);
      await tester.sendKeyDownEvent(LogicalKeyboardKey.enter);
      await tester.pump(const Duration(milliseconds: 700));
      await tester.sendKeyUpEvent(LogicalKeyboardKey.enter);
      await tester.pump();
      expect(find.byKey(const Key('epg-details-panel-expanded')), findsOneWidget);
      final art = find.byWidgetPredicate((w) => w is Image && w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-details-art-'));
      expect(art, findsOneWidget);
      expect(tester.widget<Image>(art).image, anyOf(isA<NetworkImage>(), isA<FileImage>()));
      // The syncer prefetched the poster from the media server (never through the backend).
      expect(video.imageRequests.where((u) => u.startsWith('/Items/big-a/Images/Primary?tag=tagA')), isNotEmpty, reason: '${video.imageRequests}');
      expect(p.to('/catalog-items'), isEmpty, reason: describe(p));

      await pressKey(tester, LogicalKeyboardKey.escape);
      await tester.pump();
    });

    testWidgets('the working set rolls forward while the channel plays, read from the cache', (tester) async {
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

      await boot(tester, channelId: channelId, legacy: false, workingSetSize: 1);
      int? currentProgram() {
        final t = osdTitle(tester);
        if (t == null || !t.startsWith('ROLL ')) return null;
        return int.parse(t.substring(5));
      }

      await pumpUntil(tester, () => currentProgram() != null, failMessage: 'never started playing');
      final p = proxy!;

      // The whole schedule came in with the first download: no slices.
      expect(p.exact('/epg').first.query.containsKey('end'), isFalse);

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

      // The working set rolled with the working set size, entirely from the
      // cache: playing on never asked the backend for the schedule again.
      expect(worstLead, 1);
      expect(p.exact('/epg'), hasLength(1), reason: describe(p));
      expect(p.to('/catalog-items'), isEmpty, reason: describe(p));
    });

    testWidgets('against a backend without the new fields the syncer falls back to per-item lookups, cached and bounded', (tester) async {
      // Own backend, see the header: only this test's channel is downloaded.
      final backend = BackendFixture();
      await backend.start();
      addTearDown(backend.stop);
      final deviceToken = await backend.createDeviceToken();
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

      await boot(tester, on: backend, token: deviceToken, channelId: channelId, legacy: true, workingSetSize: 1);
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'never started playing against the legacy backend');
      final p = proxy!;

      expect(osdTitle(tester), 'LEGACY A', reason: 'the title came from the per-item fallback');
      expect(p.exact('/epg'), hasLength(1));
      // The fallback resolved every distinct catalog item of the downloaded
      // schedule once, before it was stored (far fewer than the hundreds of
      // flat entries that reference them).
      final lookups = p.to('/catalog-items/');
      expect(lookups, isNotEmpty, reason: 'the fallback must actually have been exercised');
      expect(lookups.length, lessThan(60), reason: describe(p));
      expect(lookups.map((r) => r.path).toSet().length, lookups.length, reason: 'cached per id');
      expect(channel.map((u) => u.programOrdinal).toSet(), {0, 1});
      expect(channel.every((u) => u.program?.isTailGroup != true), isTrue, reason: 'a legacy backend sends no groups');

      // The guide still works, from the same stored entries: no further lookups.
      final before = p.to('/catalog-items/').length;
      await openEpg(tester);
      expect(p.to('/catalog-items/').length, before, reason: 'the guide reads the cache:\n  ${describe(p)}');
      expect(find.text('LEGACY A'), findsWidgets);
      await pressKey(tester, LogicalKeyboardKey.escape);
      await tester.pump();
    });
  });
}
