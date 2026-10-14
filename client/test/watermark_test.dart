import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:client/cache/schedule_cache.dart';
import 'package:client/playhead_api.dart';
import 'package:client/watermark.dart';
import 'package:client/widgets/watermark_overlay.dart';

// A valid 1x1 PNG.
const _png = <int>[
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89, //
  0, 0, 0, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xff, 0xff, 0x3f, 0, 5, 0xfe, 2, 0xfe, 0xdc, 0xcc, 0x59, 0xe7, //
  0, 0, 0, 0, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
];

void main() {
  group('EpgWatermark parsing', () {
    test('reads a full object', () {
      final w = EpgWatermark.tryParse({'image_id': 'i1', 'position': 'top_left', 'opacity_pct': 40, 'size_pct': 12})!;
      expect(w.imageId, 'i1');
      expect(w.position, WatermarkPosition.topLeft);
      expect(w.opacityPct, 40);
      expect(w.sizePct, 12);
      expect(w.opacity, closeTo(0.4, 1e-9));
      expect(w.heightIn(1000), 120);
    });

    test('anything unusable is no watermark', () {
      expect(EpgWatermark.tryParse(null), isNull);
      expect(EpgWatermark.tryParse('x'), isNull);
      expect(EpgWatermark.tryParse({'position': 'top_left'}), isNull);
      expect(EpgWatermark.tryParse({'image_id': ''}), isNull);
      expect(EpgWatermark.tryParse({'image_id': 5}), isNull);
    });

    test('options are clamped and an unknown corner is the default one', () {
      final w = EpgWatermark.tryParse({'image_id': 'i', 'position': 'middle', 'opacity_pct': 500, 'size_pct': 1})!;
      expect(w.position, WatermarkPosition.bottomRight);
      expect(w.opacityPct, 100);
      expect(w.sizePct, watermarkMinSizePct);
      final big = EpgWatermark.tryParse({'image_id': 'i', 'opacity_pct': -3, 'size_pct': 99})!;
      expect(big.opacityPct, 0);
      expect(big.sizePct, watermarkMaxSizePct);
      final bare = EpgWatermark.tryParse({'image_id': 'i'})!;
      expect(bare.opacityPct, watermarkDefaultOpacityPct);
      expect(bare.sizePct, watermarkDefaultSizePct);
    });

    test('an entry carries it through JSON, and an old backend has none', () {
      final entry = EpgEntry.fromJson({
        'id': 'e',
        'kind': 'program',
        'catalog_item_id': 'c',
        'start_at': 1000,
        'end_at': 2000,
        'watermark': {'image_id': 'img', 'position': 'bottom_left', 'opacity_pct': 55, 'size_pct': 9},
      });
      expect(entry.watermark, const EpgWatermark(imageId: 'img', position: WatermarkPosition.bottomLeft, opacityPct: 55, sizePct: 9));
      expect(EpgEntry.fromJson(entry.toJson()).watermark, entry.watermark);
      expect(entry.shifted(const Duration(hours: 1), idPrefix: 'x-').watermark, entry.watermark);

      final old = EpgEntry.fromJson({'id': 'e', 'kind': 'program', 'catalog_item_id': 'c', 'start_at': 1000, 'end_at': 2000});
      expect(old.watermark, isNull);
      expect(old.toJson().containsKey('watermark'), isFalse);
    });

    test('a malformed watermark never breaks the entry', () {
      final entry = EpgEntry.fromJson({
        'id': 'e',
        'kind': 'program',
        'catalog_item_id': 'c',
        'start_at': 1000,
        'end_at': 2000,
        'watermark': {'image_id': 7},
      });
      expect(entry.watermark, isNull);
    });
  });

  test('the offline cache keeps an entry watermark', () {
    final cache = ScheduleCache.inMemory(clock: () => DateTime.utc(2030, 1, 1, 12));
    addTearDown(cache.close);
    cache.replaceChannels([PlayheadChannel(id: 'c1', number: 1, name: 'One')]);
    final start = DateTime.utc(2030, 1, 1, 12);
    cache.applySnapshot({
      'c1': [
        EpgEntry(
          id: 'e1',
          kind: 'program',
          catalogItemId: 'x',
          startAt: start,
          endAt: start.add(const Duration(hours: 1)),
          externalId: 'ext',
          title: 'T',
          watermark: const EpgWatermark(imageId: 'img', position: WatermarkPosition.topRight, opacityPct: 30, sizePct: 5),
        ),
      ],
    }, from: start.subtract(const Duration(hours: 1)));
    final got = cache.entries('c1', start, start.add(const Duration(hours: 2)));
    expect(got.single.watermark?.imageId, 'img');
    expect(got.single.watermark?.position, WatermarkPosition.topRight);
  });

  group('WatermarkOverlay', () {
    late File file;
    setUpAll(() {
      final dir = Directory.systemTemp.createTempSync('wm_overlay');
      file = File('${dir.path}/w.png')..writeAsBytesSync(_png);
    });

    Future<void> pump(WidgetTester tester, ValueNotifier<EpgWatermark?> source, {WatermarkImageResolver? resolve}) async {
      await tester.binding.setSurfaceSize(const Size(1000, 500));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(MaterialApp(
        home: Stack(children: [
          const Positioned.fill(child: ColoredBox(color: Colors.black)),
          WatermarkOverlay(source: source, resolve: resolve ?? (_) => file),
        ]),
      ));
      await tester.pump();
    }

    testWidgets('draws nothing without a watermark', (tester) async {
      final source = ValueNotifier<EpgWatermark?>(null);
      await pump(tester, source);
      expect(find.byKey(const Key('watermark-image')), findsNothing);
    });

    testWidgets('size is a percent of the height, in the chosen corner, with the chosen opacity', (tester) async {
      final source = ValueNotifier<EpgWatermark?>(
        const EpgWatermark(imageId: 'a', position: WatermarkPosition.topLeft, opacityPct: 40, sizePct: 10),
      );
      await pump(tester, source);
      final image = find.byKey(const Key('watermark-image'));
      expect(image, findsOneWidget);
      expect(tester.getSize(image).height, closeTo(50, 0.5)); // 10% of 500
      expect(tester.widget<Opacity>(find.byKey(const Key('watermark-opacity'))).opacity, closeTo(0.4, 1e-9));
      final topLeft = tester.getTopLeft(image);
      expect(topLeft.dx, closeTo(32, 0.5));
      expect(topLeft.dy, closeTo(32, 0.5));

      source.value = const EpgWatermark(imageId: 'a', position: WatermarkPosition.bottomRight, opacityPct: 100, sizePct: 20);
      await tester.pump();
      final br = tester.getBottomRight(find.byKey(const Key('watermark-image')));
      expect(br.dx, closeTo(1000 - 32, 0.5));
      expect(br.dy, closeTo(500 - 32, 0.5));
      expect(tester.getSize(find.byKey(const Key('watermark-image'))).height, closeTo(100, 0.5));
    });

    testWidgets('going back to none removes it', (tester) async {
      final source = ValueNotifier<EpgWatermark?>(const EpgWatermark(imageId: 'a'));
      await pump(tester, source);
      expect(find.byKey(const Key('watermark-image')), findsOneWidget);
      source.value = null;
      await tester.pump();
      expect(find.byKey(const Key('watermark-image')), findsNothing);
    });

    testWidgets('an image that cannot be had is no watermark, never an error', (tester) async {
      final source = ValueNotifier<EpgWatermark?>(const EpgWatermark(imageId: 'a'));
      await pump(tester, source, resolve: (_) => null);
      expect(find.byKey(const Key('watermark-image')), findsNothing);
      await pump(tester, source, resolve: (_) => throw StateError('boom'));
      expect(find.byKey(const Key('watermark-image')), findsNothing);
      await pump(tester, source, resolve: (_) async => null);
      expect(find.byKey(const Key('watermark-image')), findsNothing);
      expect(tester.takeException(), isNull);
    });

    testWidgets('it does not take pointer events', (tester) async {
      var taps = 0;
      final source = ValueNotifier<EpgWatermark?>(const EpgWatermark(imageId: 'a', sizePct: 40));
      await tester.binding.setSurfaceSize(const Size(1000, 500));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(MaterialApp(
        home: Stack(children: [
          Positioned.fill(child: GestureDetector(onTap: () => taps++, child: const ColoredBox(color: Colors.black))),
          WatermarkOverlay(source: source, resolve: (_) => file),
        ]),
      ));
      await tester.pump();
      await tester.tapAt(tester.getCenter(find.byKey(const Key('watermark-image'))));
      expect(taps, 1);
    });
  });
}
