// Helpers shared by the offline-cache integration suites (see
// offline_playback_test.dart, offline_coldstart_test.dart,
// offline_reconcile_test.dart, offline_exhausted_test.dart and
// settings_offline_test.dart): pumping, reading the OSD, and seeding a channel
// whose items play back to back.
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

import 'backend_fixture.dart';
import 'key_focus.dart';

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

/// Select (OSD hidden) shows the OSD, a second Select opens the full EPG: the
/// six-button path to the guide.
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
    failMessage: 'EPG never finished loading (visible: ${find.byType(Text).evaluate().map((e) => (e.widget as Text).data).whereType<String>().where((t) => t.startsWith('ERROR') || t.startsWith('Channel')).toList()}, epg open: ${find.byKey(const Key('epg-overlay')).evaluate().isNotEmpty}, focus: ${FocusManager.instance.primaryFocus}, osd: ${osdVisible(tester)}, title: ${osdTitle(tester)})',
  );
}

/// The guide cells drawn for [channelId].
List<Widget> epgCellsOf(String channelId) => find
    .byWidgetPredicate((w) =>
        w.key is ValueKey<String> &&
        (w.key as ValueKey<String>).value.startsWith('epg-cell-$channelId-') &&
        !(w.key as ValueKey<String>).value.startsWith('epg-cell-progress-'))
    .evaluate()
    .map((e) => e.widget)
    .toList();

/// Seeds a channel (number [number]) whose [items] items, each [itemMs] long,
/// play back to back from [startOffset] before now (a `once` schedule), titled
/// "PREFIX 0", "PREFIX 1", and so on. Returns the channel id.
Future<String> seedChannel(
  BackendFixture backend, {
  required int number,
  required String prefix,
  int items = 6,
  int itemMs = 6000,
  Duration startOffset = const Duration(seconds: -2),
  Map<String, Object?> extraFields = const {},
}) async {
  final channelId = await backend.createChannel(number: number, name: '$prefix channel');
  final source = await backend.createLocalFolderMediaSource(name: '$prefix source');
  final ids = await backend.syncCatalogItems(source, [
    for (var i = 0; i < items; i++)
      {'external_id': '$prefix-$i', 'kind': 'episode', 'title': '$prefix $i', 'duration_ms': itemMs, ...extraFields},
  ]);
  final block = await backend.createBlockWithItems(ids);
  await backend.createScheduleOnce(channelId: channelId, blockId: block, startAt: DateTime.now().toUtc().add(startOffset));
  return channelId;
}
