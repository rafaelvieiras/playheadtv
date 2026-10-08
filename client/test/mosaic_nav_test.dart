import 'package:client/mosaic_nav.dart';
import 'package:flutter_test/flutter_test.dart';

// Tile numbering: 0 featured; 1..12 the frame, clockwise from the top-left:
//   1  2  3  4
//   12 F  F  5
//   11 F  F  6
//   10 9  8  7
const up = MosaicDirection.up;
const down = MosaicDirection.down;
const left = MosaicDirection.left;
const right = MosaicDirection.right;

int go(int count, int from, MosaicDirection d, {int? back}) =>
    mosaicNavigate(tileCount: count, focused: from, direction: d, returnTo: back);

void main() {
  group('full frame (13 tiles)', () {
    const n = 13;

    test('rows and columns walk the outer ring both ways', () {
      expect(go(n, 1, right), 2);
      expect(go(n, 2, right), 3);
      expect(go(n, 3, right), 4);
      expect(go(n, 4, left), 3);
      expect(go(n, 4, down), 5);
      expect(go(n, 5, down), 6);
      expect(go(n, 6, down), 7);
      expect(go(n, 7, left), 8);
      expect(go(n, 10, up), 11);
      expect(go(n, 11, up), 12);
      expect(go(n, 12, up), 1);
      expect(go(n, 1, down), 12);
    });

    test('moving toward the centre lands on the featured block', () {
      expect(go(n, 2, down), 0);
      expect(go(n, 3, down), 0);
      expect(go(n, 9, up), 0);
      expect(go(n, 8, up), 0);
      expect(go(n, 12, right), 0);
      expect(go(n, 11, right), 0);
      expect(go(n, 5, left), 0);
      expect(go(n, 6, left), 0);
    });

    test('from the featured block each arrow reaches the facing side', () {
      expect(go(n, 0, up), 2);
      expect(go(n, 0, down), 9);
      expect(go(n, 0, left), 12);
      expect(go(n, 0, right), 5);
    });

    test('nothing in the direction keeps the focus', () {
      expect(go(n, 1, up), 1);
      expect(go(n, 1, left), 1);
      expect(go(n, 4, up), 4);
      expect(go(n, 4, right), 4);
      expect(go(n, 7, down), 7);
      expect(go(n, 10, left), 10);
    });

    test('every tile is reachable from every other, with no dead ends', () {
      for (var start = 0; start < n; start++) {
        final seen = {start};
        final queue = [start];
        while (queue.isNotEmpty) {
          final t = queue.removeLast();
          for (final d in MosaicDirection.values) {
            final next = go(n, t, d);
            if (seen.add(next)) queue.add(next);
          }
        }
        expect(seen.length, n, reason: 'from $start');
      }
    });
  });

  group('partial frames', () {
    test('a lone Settings tile next to the featured video (2 tiles)', () {
      // Tile 1 (the Settings cell) is at the top-left corner.
      expect(go(2, 0, left), 1);
      expect(go(2, 0, up), 1);
      expect(go(2, 1, right), 0);
      expect(go(2, 1, down), 0);
      expect(go(2, 0, down), 0);
      expect(go(2, 1, left), 1);
    });

    test('channel + Settings (3 tiles): Up from the block reaches Settings, Down returns', () {
      // Tile 1 (a channel) at the top-left corner, tile 2 (Settings) above the
      // block's left half.
      expect(go(3, 0, up), 2);
      expect(go(3, 2, down), 0);
      expect(go(3, 0, left), 1);
      expect(go(3, 1, right), 2);
    });

    test('the featured block alone has nowhere to go', () {
      for (final d in MosaicDirection.values) {
        expect(go(1, 0, d), 0);
      }
    });

    test('3 tiles: Right at the end of a short top row enters the block', () {
      // Tiles 1,2 in the top row; the rest of the grid is empty.
      expect(go(3, 1, right), 2);
      expect(go(3, 2, right), 0); // toward the centre block, not stuck
      expect(go(3, 2, down), 0);
      expect(go(3, 1, down), 0);
      expect(go(3, 0, up), 2); // nearest across: the cell above the block's centre-left
    });

    test('7 tiles: top row complete plus the right column down to row 1 (tiles 1..6)', () {
      // 1 2 3 4 / . F F 5 / . F F 6 / . . . .  (7 tiles = featured + 1..6)
      expect(go(7, 4, down), 5);
      expect(go(7, 5, down), 6);
      expect(go(7, 6, down), 6); // nothing below
      expect(go(7, 6, up), 5);
      expect(go(7, 6, left), 0);
      expect(go(7, 1, down), 0);
      expect(go(7, 1, left), 1);
      expect(go(7, 0, down), 0);
    });

    test('11 tiles: the left column is partly empty', () {
      // Everything but tile 11 (grid row 2, col 0) and 12 (row 1, col 0).
      expect(go(11, 10, up), 0); // tile 10 is at the bottom-left corner; above it is empty, the block is up-right
      expect(go(11, 1, down), 0);
      expect(go(11, 0, left), 1); // nearest to the left of the block is the corner cell above
    });

    test('empty slots are skipped, never dead ends, in every partial frame', () {
      for (var n = 2; n <= 13; n++) {
        final seen = {0};
        final queue = [0];
        while (queue.isNotEmpty) {
          final t = queue.removeLast();
          for (final d in MosaicDirection.values) {
            final next = go(n, t, d);
            expect(next, inInclusiveRange(0, n - 1));
            if (seen.add(next)) queue.add(next);
          }
        }
        expect(seen.length, n, reason: '$n tiles: every tile reachable from the featured video');
      }
    });
  });

  group('reversibility', () {
    test('an arrow followed by its opposite returns to the origin', () {
      for (var n = 1; n <= 13; n++) {
        for (var from = 0; from < n; from++) {
          for (final d in MosaicDirection.values) {
            final to = go(n, from, d);
            if (to == from) continue;
            final back = go(n, to, d.opposite, back: from);
            expect(back, from, reason: '$n tiles: $from $d -> $to, then ${d.opposite}');
          }
        }
      }
    });

    test('without the memory a tie goes to the first tile in reading order', () {
      expect(go(13, 0, up), 2);
    });

    test('with the memory, Down then Up from the right half of the top row returns', () {
      final onFeatured = go(13, 3, down);
      expect(onFeatured, 0);
      expect(go(13, onFeatured, up, back: 3), 3);
    });
  });

  test('an out-of-range focus is left alone', () {
    expect(go(5, 9, right), 9);
    expect(go(0, 0, right), 0);
  });
}
