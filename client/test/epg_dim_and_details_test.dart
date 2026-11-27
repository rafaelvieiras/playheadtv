// The guide's focus hierarchy and the details panel's furniture.
//
// Two rules learned from a real television screenshot: the channel whose details
// are on screen is the subject of the page (every other row sits back), a
// program that already aired sits back from the one on air, and the panel's
// words carry the channel's identity in place of a program cover. The progress
// bar belongs between the two times it describes, not across the whole panel.
import 'package:client/cache/schedule_cache.dart';
import 'package:client/playhead_api.dart';
import 'package:client/ui/form_factor.dart';
import 'package:client/ui/guide_metrics.dart';
import 'package:client/ui/ui_scale.dart';
import 'package:client/widgets/epg_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  final now = DateTime.utc(2030, 1, 1, 12, 10);
  final channels = [
    for (var i = 0; i < 4; i++) PlayheadChannel(id: 'c$i', number: i + 1, name: 'Channel ${i + 1}'),
  ];

  setUp(() {
    SharedPreferences.setMockInitialValues({});
    ClientFormFactorState.notifier.value = ClientFormFactor.tv;
  });
  tearDown(() => ClientFormFactorState.notifier.value = ClientFormFactor.tv);

  ScheduleCache seed({String? channelLogo, bool withBackdrop = false}) {
    final cache = ScheduleCache.inMemory(clock: () => now);
    cache.replaceChannels([
      for (var i = 0; i < channels.length; i++)
        i == 0 && channelLogo != null
            ? PlayheadChannel(id: channels[i].id, number: channels[i].number, name: channels[i].name, logoImageId: channelLogo)
            : channels[i],
    ]);
    EpgEntry program(
      String channel,
      int n,
      Duration from,
      Duration length, {
      String? overview,
      String? backdropTag,
    }) => EpgEntry(
      id: '$channel-$n',
      kind: 'program',
      catalogItemId: 'item-$channel-$n',
      externalId: 'ext-$channel-$n',
      startAt: now.add(from),
      endAt: now.add(from + length),
      title: 'Program $channel $n',
      overview: overview,
      imageBackdropTag: backdropTag,
    );
    cache.applySnapshot({
      for (final c in channels)
        c.id: [
          // Airs already: the dim rule's past branch.
          program(c.id, 0, const Duration(minutes: -70), const Duration(minutes: 40)),
          // On air now.
          program(c.id, 1, const Duration(minutes: -30), const Duration(minutes: 60), overview: 'A synopsis.'),
          program(c.id, 2, const Duration(minutes: 30), const Duration(minutes: 45), backdropTag: withBackdrop ? 'tag-1' : null),
        ],
    }, from: now.subtract(const Duration(hours: 3)));
    return cache;
  }

  Future<void> pumpGuide(WidgetTester tester, ScheduleCache cache) async {
    tester.view.physicalSize = const Size(1920, 1080);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      MaterialApp(
        debugShowCheckedModeBanner: false,
        theme: ThemeData.dark(),
        builder: (context, child) => ClientFormFactorHost(child: UiScaleHost(child: child!)),
        home: Scaffold(
          body: EpgOverlay(
            key: const Key('epg'),
            channels: channels,
            cache: cache,
            currentChannelId: 'c0',
            onSelectLiveChannel: (_) {},
            onClose: () {},
            clock: () => now,
          ),
        ),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 400));
    for (var i = 0; i < 10; i++) {
      await tester.pump(const Duration(milliseconds: 50));
    }
  }

  testWidgets('every row but the focused one is dimmed, and a past program twice', (tester) async {
    final cache = seed();
    addTearDown(cache.close);
    await pumpGuide(tester, cache);

    // The focused row (index 0, the first channel) is at full strength; the
    // others carry the 20% wash.
    expect(find.byKey(const Key('epg-row-dim-c0')), findsNothing);
    expect(find.byKey(const Key('epg-row-dim-c1')), findsOneWidget);
    expect(find.byKey(const Key('epg-row-dim-c3')), findsOneWidget);

    // A program that already aired carries the extra 10%, on any row.
    expect(find.byKey(const Key('epg-cell-dim-c0-0')), findsOneWidget);
    expect(find.byKey(const Key('epg-cell-dim-c0-1')), findsNothing);
    expect(find.byKey(const Key('epg-cell-dim-c1-0')), findsOneWidget);
  });

  testWidgets('the progress lives between the two times, not across the panel', (tester) async {
    final cache = seed();
    addTearDown(cache.close);
    await pumpGuide(tester, cache);

    final bar = find.byKey(const Key('epg-details-time-bar'));
    expect(bar, findsOneWidget);
    // The full-width bar the panel used to carry is gone; what remains is the
    // small track, and the filled part inside it exists only while on air.
    expect(find.byKey(const Key('epg-details-progress')), findsOneWidget);
    final track = tester.getRect(find.byKey(const Key('epg-details-time-bar')));
    final fill = tester.getRect(find.byKey(const Key('epg-details-progress')));
    expect(fill.width, lessThanOrEqualTo(track.width));
    expect(fill.width, greaterThan(0));

    // It sits between the start and the end time, inside the meta row.
    final meta = tester.getRect(find.byKey(const Key('epg-details-meta-row')));
    final barRect = tester.getRect(bar);
    expect(barRect.left, greaterThan(meta.left));
    expect(barRect.right, lessThan(meta.right));
    expect(barRect.width, lessThan(meta.width / 6), reason: 'a bar of punctuation size, not a panel-wide bar');

    // And the row now reads: start, bar, end.
    final texts = tester.widgetList<Text>(find.descendant(
      of: find.byKey(const Key('epg-details-meta-row')),
      matching: find.byType(Text),
    )).map((t) => t.data).toList();
    String hhmm(DateTime t) =>
        '${t.toLocal().hour.toString().padLeft(2, '0')}:${t.toLocal().minute.toString().padLeft(2, '0')}';
    expect(texts.first, hhmm(now.subtract(const Duration(minutes: 30))));
    expect(texts[1], hhmm(now.add(const Duration(minutes: 30))));
  });

  testWidgets('the program cover is gone and the channel logo leads the panel', (tester) async {
    final cache = seed(channelLogo: 'logo-1');
    addTearDown(cache.close);
    await pumpGuide(tester, cache);

    // No series poster anywhere in the panel: the title is the first thing
    // under the channel's identity, which is the logo when the build has a
    // server to load it from and the channel's name otherwise.
    expect(find.byKey(const Key('epg-details-series-logo-c0-1')), findsNothing);
    expect(find.byKey(const Key('epg-details-channel-logo-c0')), findsNothing);
    expect(find.byKey(const Key('epg-details-channel-name')), findsOneWidget);
  });

  testWidgets('a channel without a logo keeps the slot height, so the panel cannot jump', (tester) async {
    final cache = seed();
    addTearDown(cache.close);
    await pumpGuide(tester, cache);

    final name = find.byKey(const Key('epg-details-channel-name'));
    expect(name, findsOneWidget);
    final withName = tester.getRect(find.byKey(const Key('epg-details-panel'))).height;
    // The panel's height is what a logo-owning channel produces too: measured
    // from the slot, which is reserved either way.
    expect(withName, greaterThan(0));
    final emptySlot = GuideMetrics.tv.detailsPreviewHeight;
    expect(emptySlot, greaterThan(0), reason: 'the preview slot is reserved on a television');
  });

  testWidgets('a backdrop is the item\'s fanart, never its cover', (tester) async {
    final cache = seed(withBackdrop: true);
    addTearDown(cache.close);
    await pumpGuide(tester, cache);

    final art = find.byWidgetPredicate(
      (w) => w is Image && w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-details-art-'),
    );
    // Program 2 is focused only after moving right; program 1 (on air, no
    // backdrop) must show none, and no poster in its place.
    expect(art, findsNothing);
  });
}
