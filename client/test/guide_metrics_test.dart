// The guide's two families of design units, and the two touch paths a phone
// gains: a tap on a program, and a horizontal drag on the time axis.
//
// The cells are proportional (a fixed channel column and a fixed visible span),
// which is why the phone's numbers are not cosmetic: at television numbers a
// half-hour program on a 360 dp-wide screen is a 24 dp sliver. These tests pin
// both the numbers and the rule that television numbers did not move.
import 'package:client/cache/schedule_cache.dart';
import 'package:client/playhead_api.dart';
import 'package:client/ui/form_factor.dart';
import 'package:client/ui/guide_metrics.dart';
import 'package:client/ui/ui_scale.dart';
import 'package:client/widgets/epg_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// The width of one 30-minute cell, at a window whose grid area is what is left
/// of [size] after the guide's own paddings and channel column.
double halfHourCell(Size size, ClientFormFactor formFactor) {
  final m = GuideMetrics.of(formFactor);
  final scale = resolveUiScale(size, formFactor: formFactor);
  final padding = 2 * 16 * scale;
  final column = m.channelColumnWidth * scale;
  final viewport = size.width - padding - column;
  return viewport / m.visibleSpan.inMinutes * 30;
}

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
    ClientFormFactorState.notifier.value = ClientFormFactor.tv;
    tuned.clear();
  });
  tearDown(() => ClientFormFactorState.notifier.value = ClientFormFactor.tv);

  group('GuideMetrics', () {
    test('the television keeps the numbers the overlay always had', () {
      const m = GuideMetrics.tv;
      expect(m.visibleSpan, const Duration(hours: 3));
      expect(m.channelColumnWidth, 260);
      expect(m.rowHeight, 84);
      expect(m.headerHeight, 44);
      expect(m.channelName, isTrue, reason: 'a television has room for the name');
      // And it is what the overlay gets by default.
      expect(GuideMetrics.of(ClientFormFactor.tv), same(GuideMetrics.tv));
    });

    test('the phone shows 90 minutes and a narrow column', () {
      const m = GuideMetrics.phone;
      expect(m.visibleSpan, const Duration(minutes: 90));
      expect(m.channelColumnWidth, 72);
      expect(m.channelName, isFalse, reason: '72 px cannot hold a number and a name');
      expect(GuideMetrics.of(ClientFormFactor.phone), same(GuideMetrics.phone));
    });

    test('a half hour is a real touch target on a phone in both orientations', () {
      // The number that motivated the slice: with television metrics a half hour
      // on a 360 dp screen is 24 dp wide.
      final tvPortrait = halfHourCell(const Size(360, 780), ClientFormFactor.tv);
      expect(tvPortrait, lessThan(48), reason: 'measured ${tvPortrait.toStringAsFixed(1)} dp: the problem');

      expect(halfHourCell(const Size(360, 780), ClientFormFactor.phone), greaterThanOrEqualTo(48));
      expect(halfHourCell(const Size(780, 360), ClientFormFactor.phone), greaterThanOrEqualTo(64));
      // A television is untouched: same numbers, same cell.
      expect(halfHourCell(const Size(1920, 1080), ClientFormFactor.tv), greaterThan(48));
    });
  });

  group('guide touch', () {
    final now = DateTime.utc(2030, 1, 1, 12, 10);
    final channels = [for (var i = 0; i < 4; i++) PlayheadChannel(id: 'c$i', number: i + 1, name: 'Channel ${i + 1}')];

    ScheduleCache seed() {
      final cache = ScheduleCache.inMemory(clock: () => now);
      cache.replaceChannels(channels);
      EpgEntry program(String channel, int n, Duration from, Duration length) => EpgEntry(
        id: '$channel-$n',
        kind: 'program',
        catalogItemId: 'item-$channel-$n',
        startAt: now.add(from),
        endAt: now.add(from + length),
        title: 'Program $channel $n',
      );
      cache.applySnapshot({
        for (final c in channels)
          c.id: [
            program(c.id, 0, const Duration(minutes: -50), const Duration(minutes: 80)),
            program(c.id, 1, const Duration(minutes: 30), const Duration(minutes: 45)),
            program(c.id, 2, const Duration(minutes: 75), const Duration(minutes: 90)),
          ],
      }, from: now.subtract(const Duration(hours: 3)));
      return cache;
    }

    Future<void> pumpGuide(WidgetTester tester, ScheduleCache cache) async {
      tester.view.physicalSize = const Size(780, 360);
      tester.view.devicePixelRatio = 1.0;
      addTearDown(tester.view.reset);
      ClientFormFactorState.notifier.value = ClientFormFactor.phone;
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
              videoThumbnailRect: null,
              onSelectLiveChannel: (id) => tuned.add(id),
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

    testWidgets('a tap on a live program tunes to that channel', (tester) async {
      final cache = seed();
      addTearDown(cache.close);
      await pumpGuide(tester, cache);

      // Row 2's first program is on air (it started 50 minutes ago): the same
      // condition Select uses on the focused cell.
      await tester.tap(find.byKey(const Key('epg-cell-c1-c1-0')));
      await tester.pump(const Duration(milliseconds: 300));
      expect(tuned, ['c1']);
    });

    testWidgets('a tap on a program that is not on air only focuses it', (tester) async {
      final cache = seed();
      addTearDown(cache.close);
      await pumpGuide(tester, cache);

      // Program 2 of row 1 starts 30 minutes from now: it begins inside the
      // visible span, so its left edge is on screen while its centre is not —
      // tap where it is actually drawn.
      final cell = find.byKey(const Key('epg-cell-c1-c1-1'));
      await tester.tapAt(tester.getTopLeft(cell) + const Offset(12, 12));
      await tester.pump(const Duration(milliseconds: 300));
      expect(tuned, isEmpty);
    });

    testWidgets('a horizontal drag pans the time axis without tuning', (tester) async {
      final cache = seed();
      addTearDown(cache.close);
      await pumpGuide(tester, cache);

      final before = tester.getRect(find.byKey(const Key('epg-cell-c0-c0-1'))).left;
      await tester.drag(find.byKey(const Key('epg-rows')), const Offset(-120, 0));
      await tester.pump(const Duration(milliseconds: 300));
      final after = tester.getRect(find.byKey(const Key('epg-cell-c0-c0-1'))).left;
      expect(after, lessThan(before), reason: 'the picture follows the finger');
      expect(tuned, isEmpty, reason: 'a drag is not a tap');
    });
  });
}

/// What the guide asked the page to tune to.
final List<String> tuned = [];
