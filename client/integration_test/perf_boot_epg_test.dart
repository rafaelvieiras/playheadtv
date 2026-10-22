// Performance suite: boot and EPG. Boots the real app once against a real
// backend and a fake media server, and records, per phase, wall time, frame
// timings, CPU and RSS (support/perf_probe.dart). It prints a `PERF_JSON` line
// and writes `$PERF_OUT` when set. `PERF_ENFORCE=1` also fails the suite when a
// phase exceeds the budgets in client/perf/budgets.json (the regression gate).
//
// One real boot per file, per AGENTS.md ("Several real PlayheadSpikeApp boots").
import 'dart:convert';
import 'dart:io';

import 'package:client/services/perf_trace.dart';
import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/perf_keys.dart';
import 'support/perf_probe.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('performance: boot and EPG (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    // A library the size of a real one: the EPG cost scales with channels and
    // with programs per channel inside the 3 h window.
    const channelCount = 30;
    const itemsPerChannel = 24;
    const itemMs = 1800000;
    final channelIds = <String>[];

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      for (var number = 1; number <= channelCount; number++) {
        final channelId = await backend.createChannel(number: number, name: 'Perf Channel $number');
        channelIds.add(channelId);
        final sourceId = await backend.createLocalFolderMediaSource(name: 'Perf source $number');
        final ids = await backend.syncCatalogItems(sourceId, [
          for (var i = 0; i < itemsPerChannel; i++)
            {'external_id': 'perf-$number-$i', 'kind': 'episode', 'title': 'PERF $number ITEM $i', 'duration_ms': itemMs},
        ]);
        final blockId = await backend.createBlockWithItems(ids);
        await backend.createScheduleOnce(
          channelId: channelId,
          blockId: blockId,
          startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 3)),
        );
      }
      // Every channel must have its guide materialized before the app boots, or
      // the first phases would measure the backend, not the client.
      for (final id in channelIds) {
        await backend.waitForEpgEntries(id, 4, timeout: const Duration(seconds: 120));
      }
      final deviceToken = await backend.createDeviceToken();
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
      );
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      // No history: boot goes to the mosaic, and the player is reached from it
      // with Select, which is also the real first-use path.
    });

    Future<void> pumpUntil(
      WidgetTester tester,
      bool Function() condition, {
      Duration timeout = const Duration(seconds: 60),
      String? failMessage,
    }) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met within $timeout');
        await tester.pump(const Duration(milliseconds: 16));
      }
    }

    bool has(String key) => find.byKey(Key(key)).evaluate().isNotEmpty;

    bool hasPrefix(String prefix) => find
        .byWidgetPredicate((w) => w.key is ValueKey<String> && (w.key! as ValueKey<String>).value.startsWith(prefix))
        .evaluate()
        .isNotEmpty;

    String? osdChannelNumber(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-channel-number'));
      if (finder.evaluate().isEmpty) return null;
      return tester.widget<Text>(finder).data;
    }

    bool osdVisible(WidgetTester tester) {
      final finder = find.ancestor(of: find.byKey(const Key('channel-osd')), matching: find.byType(AnimatedOpacity));
      if (finder.evaluate().isEmpty) return false;
      return tester.widget<AnimatedOpacity>(finder).opacity == 1;
    }

    testWidgets('boot to a playing channel, open the EPG, move around it', (tester) async {
      final probe = PerfProbe('perf_boot_epg_test');

      // Phase 1: process-side boot, from the first widget to the mosaic and then
      // to a channel actually tuned. Includes the first backend sync.
      BootTrace.reset();
      BootTrace.mark('main');
      await probe.begin('boot_to_mosaic');
      await tester.pumpWidget(const PlayheadSpikeApp());
      // The page exists from the loading state; the tiles mean it has data.
      probe.mark('first_pump');
      await pumpUntil(tester, () => hasPrefix('mosaic-tile-'), failMessage: 'the mosaic never showed its tiles');
      probe.mark('mosaic_tiles');
      await probe.end();

      // Select on the focused tile (the featured one at boot) enters its channel.
      await probe.begin('mosaic_to_playing');
      await perfPressKey(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => osdChannelNumber(tester) != null, timeout: const Duration(seconds: 90),
          failMessage: 'no channel was tuned from the mosaic (mosaic still up: ${has('mosaic-page')})');
      probe.mark('channel_tuned');
      await pumpUntil(tester, () => !osdVisible(tester), timeout: const Duration(seconds: 20), failMessage: 'the OSD never hid');
      await probe.end();

      // Phase 2: opening the guide (Select shows the OSD, Select again opens
      // the EPG). The phase ends when the grid has rows on screen.
      await probe.begin('epg_open');
      await perfPressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Select did not show the OSD');
      await perfPressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => has('epg-details-panel') && has('epg-rows'), failMessage: 'the EPG never finished loading');
      probe.mark('epg_ready');
      await probe.end();

      // Phase 3: moving around the guide, the interaction that is slow on a TV
      // box: down through the rows, then forward in time and back.
      await probe.begin('epg_navigate');
      for (var i = 0; i < 12; i++) {
        await perfPressKey(tester, LogicalKeyboardKey.arrowDown);
        await tester.pump(const Duration(milliseconds: 120));
      }
      for (var i = 0; i < 8; i++) {
        await perfPressKey(tester, LogicalKeyboardKey.arrowRight);
        await tester.pump(const Duration(milliseconds: 120));
      }
      for (var i = 0; i < 8; i++) {
        await perfPressKey(tester, LogicalKeyboardKey.arrowLeft);
        await tester.pump(const Duration(milliseconds: 120));
      }
      for (var i = 0; i < 12; i++) {
        await perfPressKey(tester, LogicalKeyboardKey.arrowUp);
        await tester.pump(const Duration(milliseconds: 120));
      }
      await probe.end();

      // Phase 4: the guide open and nothing happening. This is the steady cost
      // (the now marker and progress bars tick, the video keeps playing).
      await probe.begin('epg_idle');
      final until = DateTime.now().add(const Duration(seconds: 8));
      while (DateTime.now().isBefore(until)) {
        await tester.pump(const Duration(milliseconds: 100));
      }
      await probe.end();

      probe.extra['scenario'] = {'channels': channelCount, 'items_per_channel': itemsPerChannel, 'item_minutes': itemMs ~/ 60000};
      probe.extra['boot_marks_ms'] = {for (final m in BootTrace.marks) m.$1: m.$2};
      probe.report();
      _enforce(probe);
    });
  });
}

/// Fails the suite when a phase is over budget. Off unless PERF_ENFORCE=1: the
/// budgets are for one setup (the GUI container), a developer machine differs.
void _enforce(PerfProbe probe) {
  if (Platform.environment['PERF_ENFORCE'] != '1') return;
  final file = File(Platform.environment['PERF_BUDGETS'] ?? 'perf/budgets.json');
  if (!file.existsSync()) fail('PERF_ENFORCE=1 but ${file.path} does not exist');
  final budgets = jsonDecode(file.readAsStringSync()) as Map<String, dynamic>;
  final measured = probe.toJson()['phases'] as Map<String, Object?>;
  final over = <String>[];
  for (final entry in (budgets['phases'] as Map<String, dynamic>).entries) {
    final got = measured[entry.key] as Map<String, Object?>?;
    if (got == null) {
      over.add('${entry.key}: phase missing from the run');
      continue;
    }
    for (final limit in (entry.value as Map<String, dynamic>).entries) {
      final value = _lookup(got, limit.key);
      if (value != null && value > (limit.value as num)) {
        over.add('${entry.key}.${limit.key} = $value > ${limit.value}');
      }
    }
  }
  if (over.isNotEmpty) fail('over the performance budget:\n  ${over.join('\n  ')}');
}

double? _lookup(Map<String, Object?> json, String path) {
  Object? node = json;
  for (final part in path.split('.')) {
    if (node is! Map) return null;
    node = node[part];
  }
  return node is num ? node.toDouble() : null;
}
