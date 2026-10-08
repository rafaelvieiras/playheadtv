// Spatial navigation over the mosaic's 4x4 grid (pure: no widgets).
//
// A tile is an index: 0 is the featured video (the central 2x2 block), i >= 1
// is the frame cell `mosaicRingSlots[i - 1]`. The frame fills in order, so the
// tiles that exist are exactly 0..tileCount-1 and the rest of the grid is
// empty (the last tile is the Settings tile, which is just another cell here).
//
// An arrow moves to the nearest occupied tile in that direction: first by the
// gap along the arrow's axis, then by how far its centre is across it. Empty
// slots are skipped; with nothing in the direction the focus stays. The
// featured block is one target with its 2x2 extent, so moving toward the centre
// from any frame cell lands on it whenever nothing nearer is in the way.
//
// With nothing in the direction, a frame tile still reaches the featured block
// when its centre lies that way (Right from the second cell of the top row,
// with the rest of the row empty, enters the block below-right); otherwise the
// focus stays.
//
// Reversible: [returnTo] is the tile the previous move came from, passed only
// when the new arrow is the exact opposite of that move. That tile always lies
// that way (by centre), and it wins any choice: Down then Up returns to the
// origin.
import 'dart:ui' show Rect;

enum MosaicDirection {
  up,
  down,
  left,
  right;

  MosaicDirection get opposite => switch (this) {
        up => down,
        down => up,
        left => right,
        right => left,
      };
}

/// (row, column) of the frame cells in the 4x4 grid; the central 2x2 block
/// (rows 1-2, columns 1-2) is the featured video. Clockwise from the top-left
/// corner: this is also the order the frame is filled in.
const List<({int row, int col})> mosaicRingSlots = [
  (row: 0, col: 0), (row: 0, col: 1), (row: 0, col: 2), (row: 0, col: 3), // top
  (row: 1, col: 3), (row: 2, col: 3), // right side
  (row: 3, col: 3), (row: 3, col: 2), (row: 3, col: 1), (row: 3, col: 0), // bottom
  (row: 2, col: 0), (row: 1, col: 0), // left side
];

/// The featured tile's index; frame cells follow.
const int mosaicFeaturedIndex = 0;

/// Most tiles a mosaic can hold: the featured one plus the whole frame.
int get mosaicMaxTiles => 1 + mosaicRingSlots.length;

/// The grid area of [tile] in cell units (left/top/right/bottom).
Rect mosaicTileGridRect(int tile) {
  if (tile == mosaicFeaturedIndex) return const Rect.fromLTRB(1, 1, 3, 3);
  final slot = mosaicRingSlots[tile - 1];
  return Rect.fromLTWH(slot.col.toDouble(), slot.row.toDouble(), 1, 1);
}

/// The tile that gets the focus when [direction] is pressed on [focused], or
/// [focused] itself when nothing lies that way. [tileCount] tiles exist
/// (indices 0..tileCount-1).
int mosaicNavigate({
  required int tileCount,
  required int focused,
  required MosaicDirection direction,
  int? returnTo,
}) {
  if (tileCount <= 0 || focused < 0 || focused >= tileCount) return focused;
  final from = mosaicTileGridRect(focused);

  // Gap along the arrow's axis (>= 0 when the candidate lies that way) and
  // distance between the centres across it.
  double? gap(Rect to) => switch (direction) {
        MosaicDirection.right => to.left >= from.right ? to.left - from.right : null,
        MosaicDirection.left => to.right <= from.left ? from.left - to.right : null,
        MosaicDirection.down => to.top >= from.bottom ? to.top - from.bottom : null,
        MosaicDirection.up => to.bottom <= from.top ? from.top - to.bottom : null,
      };
  double across(Rect to) => switch (direction) {
        MosaicDirection.left || MosaicDirection.right => (to.center.dy - from.center.dy).abs(),
        MosaicDirection.up || MosaicDirection.down => (to.center.dx - from.center.dx).abs(),
      };

  // Whether the centre of [to] lies strictly in the arrow's direction.
  bool centreAhead(Rect to) => switch (direction) {
        MosaicDirection.right => to.center.dx > from.center.dx,
        MosaicDirection.left => to.center.dx < from.center.dx,
        MosaicDirection.down => to.center.dy > from.center.dy,
        MosaicDirection.up => to.center.dy < from.center.dy,
      };

  if (returnTo != null && returnTo != focused && returnTo >= 0 && returnTo < tileCount) {
    if (centreAhead(mosaicTileGridRect(returnTo))) return returnTo;
  }

  var best = focused;
  double? bestGap;
  double bestAcross = 0;
  for (var i = 0; i < tileCount; i++) {
    if (i == focused) continue;
    final rect = mosaicTileGridRect(i);
    final g = gap(rect);
    if (g == null) continue;
    final a = across(rect);
    final better = bestGap == null ||
        g < bestGap ||
        (g == bestGap && a < bestAcross) ||
        (g == bestGap && a == bestAcross && _before(rect, mosaicTileGridRect(best)));
    if (better) {
      best = i;
      bestGap = g;
      bestAcross = a;
    }
  }
  if (bestGap == null && focused != mosaicFeaturedIndex && centreAhead(mosaicTileGridRect(mosaicFeaturedIndex))) {
    return mosaicFeaturedIndex;
  }
  return best;
}

// Ties go to the tile that comes first reading the grid (top, then left).
bool _before(Rect a, Rect b) => a.top != b.top ? a.top < b.top : a.left < b.left;
