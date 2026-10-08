// Geometry of the mosaic frame (pure: no widgets). The frame is a fixed 4x4
// grid of 16:9 cells with a gap, scaled as a whole so it fills the available
// height (minus a small safe margin for display overscan); when the display is
// too narrow for that at full height (a portrait window) it fits the width
// instead. Whatever is left over stays on the sides, centred.
import 'dart:ui';

/// Reference geometry, in arbitrary units: 160x90 cells and an 8 gap, the
/// proportions the mosaic has always had.
const double mosaicCellUnitWidth = 160;
const double mosaicCellUnitHeight = 90;
const double mosaicGapUnit = 8;
const double mosaicFrameUnitWidth = mosaicCellUnitWidth * 4 + mosaicGapUnit * 3; // 664
const double mosaicFrameUnitHeight = mosaicCellUnitHeight * 4 + mosaicGapUnit * 3; // 384

/// Share of each dimension left empty on every side for display overscan.
const double defaultMosaicSafeMargin = 0.02;

class MosaicLayout {
  /// Units to logical pixels (frame height / 384).
  final double scale;

  /// The frame's rectangle inside the available area.
  final Rect frame;

  const MosaicLayout({required this.scale, required this.frame});

  static const empty = MosaicLayout(scale: 0, frame: Rect.zero);

  bool get isEmpty => scale <= 0;

  double get gap => mosaicGapUnit * scale;
  Size get cellSize => Size(mosaicCellUnitWidth * scale, mosaicCellUnitHeight * scale);

  /// Cell (row, col) relative to the frame's top-left corner.
  Rect cell(int row, int col) => Rect.fromLTWH(
        col * (mosaicCellUnitWidth + mosaicGapUnit) * scale,
        row * (mosaicCellUnitHeight + mosaicGapUnit) * scale,
        mosaicCellUnitWidth * scale,
        mosaicCellUnitHeight * scale,
      );

  /// The featured 2x2 block, relative to the frame's top-left corner.
  Rect get featured => Rect.fromLTWH(
        (mosaicCellUnitWidth + mosaicGapUnit) * scale,
        (mosaicCellUnitHeight + mosaicGapUnit) * scale,
        (mosaicCellUnitWidth * 2 + mosaicGapUnit) * scale,
        (mosaicCellUnitHeight * 2 + mosaicGapUnit) * scale,
      );
}

/// The frame for an [available] area. [safeMargin] is the fraction of each
/// dimension kept free on every side (clamped to 0..0.2).
MosaicLayout computeMosaicLayout(Size available, {double safeMargin = defaultMosaicSafeMargin}) {
  if (!available.width.isFinite || !available.height.isFinite) return MosaicLayout.empty;
  final m = safeMargin.isFinite ? safeMargin.clamp(0.0, 0.2) : defaultMosaicSafeMargin;
  final usableWidth = available.width * (1 - 2 * m);
  final usableHeight = available.height * (1 - 2 * m);
  if (usableWidth <= 0 || usableHeight <= 0) return MosaicLayout.empty;
  final scale = [usableHeight / mosaicFrameUnitHeight, usableWidth / mosaicFrameUnitWidth].reduce((a, b) => a < b ? a : b);
  final width = mosaicFrameUnitWidth * scale;
  final height = mosaicFrameUnitHeight * scale;
  return MosaicLayout(
    scale: scale,
    frame: Rect.fromLTWH((available.width - width) / 2, (available.height - height) / 2, width, height),
  );
}
