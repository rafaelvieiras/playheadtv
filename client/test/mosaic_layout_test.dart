import 'dart:ui';

import 'package:client/mosaic_layout.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  const margin = defaultMosaicSafeMargin;
  const ratio = mosaicFrameUnitWidth / mosaicFrameUnitHeight;

  test('fills the available height minus the safe margin on a 16:9 display', () {
    final l = computeMosaicLayout(const Size(1920, 1080));
    expect(l.frame.height, closeTo(1080 * (1 - 2 * margin), 1e-6));
    expect(l.frame.width / l.frame.height, closeTo(ratio, 1e-9));
    expect(l.frame.center.dx, closeTo(960, 1e-6)); // centred
    expect(l.frame.center.dy, closeTo(540, 1e-6));
  });

  test('a 21:9 display keeps the height and leaves the sides empty', () {
    final l = computeMosaicLayout(const Size(2560, 1080));
    expect(l.frame.height, closeTo(1080 * 0.96, 1e-6));
    expect(l.frame.left, greaterThan(300));
    expect(l.frame.left + l.frame.right, closeTo(2560, 1e-6));
  });

  test('a portrait window fits the width instead of overflowing', () {
    final l = computeMosaicLayout(const Size(600, 1000));
    expect(l.frame.width, closeTo(600 * 0.96, 1e-6));
    expect(l.frame.height, lessThan(1000 * 0.96));
    expect(l.frame.left, greaterThanOrEqualTo(0));
    expect(l.frame.right, lessThanOrEqualTo(600));
  });

  test('the frame always fits inside the available area', () {
    for (final size in const [Size(1280, 720), Size(3840, 2160), Size(800, 600), Size(1000, 1000), Size(300, 900), Size(50, 30)]) {
      final l = computeMosaicLayout(size);
      expect(l.frame.left, greaterThanOrEqualTo(0), reason: '$size');
      expect(l.frame.top, greaterThanOrEqualTo(0), reason: '$size');
      expect(l.frame.right, lessThanOrEqualTo(size.width + 1e-9), reason: '$size');
      expect(l.frame.bottom, lessThanOrEqualTo(size.height + 1e-9), reason: '$size');
    }
  });

  test('cells keep 16:9 and the gap scales with the frame', () {
    final l = computeMosaicLayout(const Size(3840, 2160));
    final c = l.cell(0, 0);
    expect(c.width / c.height, closeTo(16 / 9, 1e-9));
    expect(l.cell(0, 1).left - l.cell(0, 0).right, closeTo(l.gap, 1e-9));
    expect(l.cell(3, 3).right, closeTo(l.frame.width, 1e-6));
    expect(l.cell(3, 3).bottom, closeTo(l.frame.height, 1e-6));
  });

  test('the featured block spans the central 2x2 cells', () {
    final l = computeMosaicLayout(const Size(1920, 1080));
    expect(l.featured.topLeft, l.cell(1, 1).topLeft);
    expect(l.featured.bottomRight.dx, closeTo(l.cell(2, 2).right, 1e-9));
    expect(l.featured.bottomRight.dy, closeTo(l.cell(2, 2).bottom, 1e-9));
  });

  test('the layout scales linearly with the display', () {
    final a = computeMosaicLayout(const Size(1280, 720));
    final b = computeMosaicLayout(const Size(2560, 1440));
    expect(b.scale, closeTo(a.scale * 2, 1e-9));
  });

  test('a zero safe margin uses the whole height', () {
    final l = computeMosaicLayout(const Size(1920, 1080), safeMargin: 0);
    expect(l.frame.height, closeTo(1080, 1e-6));
  });

  test('absurd margins are clamped, tiny and degenerate sizes do not blow up', () {
    expect(computeMosaicLayout(const Size(1920, 1080), safeMargin: 5).frame.height, greaterThan(0));
    expect(computeMosaicLayout(const Size(1920, 1080), safeMargin: -1).frame.height, closeTo(1080, 1e-6));
    expect(computeMosaicLayout(Size.zero).isEmpty, isTrue);
    expect(computeMosaicLayout(const Size(double.infinity, 100)).isEmpty, isTrue);
    expect(computeMosaicLayout(const Size(20, 10)).frame.height, greaterThan(0));
  });
}
