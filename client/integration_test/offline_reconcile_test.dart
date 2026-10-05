// E2E: how the client reconciles with the backend after it was away, and the
// cheap check that keeps the cache current.
//
//  - ETag/304: with nothing changed, the background refresh is a conditional
//    request answered `304` (a request-counting proxy in front of the backend
//    records the validators and statuses).
//  - A backend that comes back with a changed schedule: the client applies it
//    exactly like an admin grid reset on that channel — the program on air
//    changed, so it switches to the new one.
//  - Same program on air, different programs after it: only what follows is
//    refreshed, in place, without restarting what is playing.
//
// Two real app boots (see AGENTS.md on keeping the count low).
import 'package:client/cache/offline_runtime.dart';
import 'package:client/cache/schedule_sync.dart';
import 'package:client/main.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/request_proxy.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('reconciling with the backend (real e2e)', () {
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

    Future<void> boot(WidgetTester tester, String channelId) async {
      proxy = RequestProxy(backendBaseUrl: backend.baseUrl);
      await proxy!.start();
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: proxy!.baseUrl,
        playheadDeviceToken: deviceToken,
        cacheSyncInterval: const Duration(seconds: 2),
      );
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelId);
      await tester.pumpWidget(const PlayheadSpikeApp());
    }

    String describe(RequestProxy p) => p.requests.map((r) => '$r -> ${r.status} (inm=${r.ifNoneMatch})').join('\n  ');

    testWidgets('an unchanged guide is a 304; a backend back with a changed schedule switches the program on air', (tester) async {
      // OLD is the program on air for an hour.
      final channelId = await backend.createChannel(number: 61, name: 'Reconcile switch');
      final source = await backend.createLocalFolderMediaSource(name: 'Reconcile switch source');
      final ids = await backend.syncCatalogItems(source, [
        {'external_id': 'rec-old', 'kind': 'movie', 'title': 'OLD PROGRAM', 'duration_ms': 3600000},
        {'external_id': 'rec-new', 'kind': 'movie', 'title': 'NEW PROGRAM', 'duration_ms': 3600000},
      ]);
      final blockOld = await backend.createBlockWithItems([ids[0]]);
      final blockNew = await backend.createBlockWithItems([ids[1]]);
      await backend.createScheduleOnce(channelId: channelId, blockId: blockOld, startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 2)));
      await backend.waitForEpgEntries(channelId, 1);

      await boot(tester, channelId);
      await pumpUntil(tester, () => osdTitle(tester) == 'OLD PROGRAM', failMessage: 'never started OLD PROGRAM');
      final p = proxy!;
      final runtime = OfflineRuntime.current!;

      // Nothing changes: the first download is a 200 without a validator, the
      // following refreshes carry it and are answered 304.
      await pumpUntil(tester, () => p.exact('/epg').length >= 3, timeout: const Duration(seconds: 30), failMessage: 'the cache never refreshed:\n  ${describe(p)}');
      final epg = p.exact('/epg');
      expect(epg.first.ifNoneMatch, isNull);
      expect(epg.first.status, 200);
      for (final r in epg.skip(1)) {
        expect(r.ifNoneMatch, isNotNull, reason: describe(p));
        expect(r.status, 304, reason: 'an unchanged guide must cost a 304:\n  ${describe(p)}');
      }
      expect(epg.first.query['group_tail'], '1');
      expect(osdTitle(tester), 'OLD PROGRAM');
      final revisionBefore = runtime.cache.revision;

      // --- the backend goes away (the player is not told anything) ---
      await backend.stopServer();
      await pumpUntil(tester, () => !runtime.syncer.status.value.lastSucceeded, failMessage: 'the syncer never noticed the outage');
      await tester.pump(const Duration(seconds: 3));
      expect(osdTitle(tester), 'OLD PROGRAM', reason: 'playback must not care that the backend is away');

      // --- it comes back with a different program on air ---
      await backend.restartServer();
      await backend.createScheduleWithItems(
        channelId: channelId,
        items: [(blockId: blockNew, startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 1)))],
      );
      await backend.waitForEpgEntries(channelId, 1);
      await pumpUntil(
        tester,
        () => osdTitle(tester) == 'NEW PROGRAM',
        timeout: const Duration(seconds: 40),
        failMessage: 'the client did not switch to the new program on air (still "${osdTitle(tester)}"):\n  ${describe(p)}',
      );
      expect(runtime.cache.revision, greaterThan(revisionBefore ?? 0));
      // The refresh that carried the change was a real download (a 200).
      expect(p.exact('/epg').where((r) => r.status == 200).length, greaterThanOrEqualTo(2), reason: describe(p));
    });

    testWidgets('same program on air, new programs after it: only what follows is refreshed, in place', (tester) async {
      final channelId = await backend.createChannel(number: 62, name: 'Reconcile in place');
      final source = await backend.createLocalFolderMediaSource(name: 'Reconcile in place source');
      final ids = await backend.syncCatalogItems(source, [
        {'external_id': 'keep', 'kind': 'movie', 'title': 'KEEP PROGRAM', 'duration_ms': 3600000},
        {'external_id': 'follow-old', 'kind': 'movie', 'title': 'FOLLOW OLD', 'duration_ms': 1800000},
        {'external_id': 'follow-new', 'kind': 'movie', 'title': 'FOLLOW NEW', 'duration_ms': 1800000},
      ]);
      final blockKeep = await backend.createBlockWithItems([ids[0]]);
      final blockOld = await backend.createBlockWithItems([ids[1]]);
      final blockNew = await backend.createBlockWithItems([ids[2]]);
      final keepStart = DateTime.now().toUtc().subtract(const Duration(seconds: 2));
      final followStart = keepStart.add(const Duration(minutes: 75));
      await backend.createScheduleWithItems(
        channelId: channelId,
        items: [(blockId: blockKeep, startAt: keepStart), (blockId: blockOld, startAt: followStart)],
      );
      await backend.waitForEpgEntries(channelId, 2);

      await boot(tester, channelId);
      await pumpUntil(tester, () => osdTitle(tester) == 'KEEP PROGRAM', failMessage: 'never started KEEP PROGRAM');
      await pumpUntil(tester, () => channel.any((u) => u.programLabel == 'FOLLOW OLD'), failMessage: 'FOLLOW OLD never entered the working set');
      final playingUnit = channel.first;

      // Same block at the same time (so the same airing), a different one after it.
      await backend.createScheduleWithItems(
        channelId: channelId,
        items: [(blockId: blockKeep, startAt: keepStart), (blockId: blockNew, startAt: followStart)],
      );
      await pumpUntil(
        tester,
        () => channel.any((u) => u.programLabel == 'FOLLOW NEW') && !channel.any((u) => u.programLabel == 'FOLLOW OLD'),
        timeout: const Duration(seconds: 40),
        failMessage: 'the working set never picked up the new program after the one on air: ${channel.map((u) => u.programLabel).toList()}',
      );
      expect(osdTitle(tester), 'KEEP PROGRAM');
      expect(identical(channel.first, playingUnit), isTrue, reason: 'the unit on air must be the same object: nothing was reloaded');
      expect(OfflineRuntime.current!.syncer.status.value.lastOutcome, anyOf(SyncOutcome.updated, SyncOutcome.unchanged));
    });
  });
}
