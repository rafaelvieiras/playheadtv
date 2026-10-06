// Unit test for the guide's hidden-filler attribution (lib/epg_guide.dart).
// The cases mirror webui/test/guideDisplay.test.ts one for one.
import 'package:client/cache/schedule_cache.dart' show cachePastRetention;
import 'package:client/epg_guide.dart';
import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';

// Times are in seconds to keep the cases readable.
EpgEntry e(String id, String kind, int start, int end) => EpgEntry(
      id: id,
      kind: kind,
      catalogItemId: 'ci-$id',
      startAt: DateTime.fromMillisecondsSinceEpoch(start * 1000, isUtc: true),
      endAt: DateTime.fromMillisecondsSinceEpoch(end * 1000, isUtc: true),
    );

List<List<Object>> spans(List<GuideCell> cells) => [
      for (final c in cells) [c.entry.id, c.displayStart.millisecondsSinceEpoch ~/ 1000, c.displayEnd.millisecondsSinceEpoch ~/ 1000],
    ];

void main() {
  test('programs only: nothing changes', () {
    final cells = buildGuideCells([e('a', 'program', 0, 100), e('b', 'program', 100, 200)]);
    expect(spans(cells), [['a', 0, 100], ['b', 100, 200]]);
  });

  test('preroll extends the next program backwards', () {
    final cells = buildGuideCells([e('a', 'program', 0, 100), e('pre', 'preroll', 100, 110), e('b', 'program', 110, 200)]);
    expect(spans(cells), [['a', 0, 100], ['b', 100, 200]]);
    expect(cells[1].absorbed.map((x) => x.id), ['pre']);
  });

  test('postroll, midroll and tail extend the previous program forwards', () {
    final cells = buildGuideCells([
      e('a', 'program', 0, 100),
      e('mid', 'midroll', 100, 110),
      e('b', 'program', 110, 200),
      e('post', 'postroll', 200, 210),
      e('t1', 'tail', 210, 220),
      e('t2', 'tail', 220, 230),
    ]);
    expect(spans(cells), [['a', 0, 110], ['b', 110, 230]]);
    expect(cells[1].absorbed.map((x) => x.id), ['post', 't1', 't2']);
  });

  test('postroll then preroll between programs split at the boundary', () {
    final cells = buildGuideCells([e('a', 'program', 0, 100), e('post', 'postroll', 100, 110), e('pre', 'preroll', 110, 120), e('b', 'program', 120, 200)]);
    expect(spans(cells), [['a', 0, 110], ['b', 110, 200]]);
  });

  test('tail with no previous program falls back to the next one', () {
    final cells = buildGuideCells([e('t', 'tail', 0, 10), e('a', 'program', 10, 100)]);
    expect(spans(cells), [['a', 0, 100]]);
  });

  test('preroll with no next program falls back to the previous one', () {
    final cells = buildGuideCells([e('a', 'program', 0, 100), e('pre', 'preroll', 100, 110)]);
    expect(spans(cells), [['a', 0, 110]]);
  });

  test('hidden entries with no program at all leave nothing to show', () {
    expect(buildGuideCells([e('t', 'tail', 0, 10)]), isEmpty);
  });

  test('a fallback is its own cell and absorbs nothing', () {
    final cells = buildGuideCells([e('f', 'fallback', 0, 50), e('t', 'tail', 50, 60), e('a', 'program', 60, 100)]);
    // The fallback is in the way of the tail's previous side, so it goes to the next program.
    expect(spans(cells), [['f', 0, 50], ['a', 50, 100]]);
  });

  test('a real gap next to a filler is not painted over', () {
    final cells = buildGuideCells([e('a', 'program', 0, 100), e('t', 'tail', 500, 510)]);
    expect(spans(cells), [['a', 0, 100]]);
  });

  test('now inside a hidden filler belongs to the program it was attributed to', () {
    final cells = buildGuideCells([e('a', 'program', 0, 100), e('post', 'postroll', 100, 110), e('b', 'program', 110, 200)]);
    final now = DateTime.fromMillisecondsSinceEpoch(105000, isUtc: true);
    expect(cells.where((c) => c.isLiveAt(now)).map((c) => c.entry.id), ['a']);
  });

  test('input order does not matter and the input is not mutated', () {
    final input = [e('b', 'program', 110, 200), e('pre', 'preroll', 100, 110), e('a', 'program', 0, 100)];
    final ids = input.map((x) => x.id).toList();
    expect(spans(buildGuideCells(input)), [['a', 0, 100], ['b', 100, 200]]);
    expect(input.map((x) => x.id), ids);
  });

  // A 20 minute episode with three 5 minute fillers is ONE 35 minute program
  // (times in seconds: 20 min = 1200, 5 min = 300).
  test('a 20 minute episode with preroll, postroll and midroll displays as one 35 minute program', () {
    final cells = buildGuideCells([
      e('pre', 'preroll', 0, 300),
      e('a', 'program', 300, 1500),
      e('post', 'postroll', 1500, 1800),
      e('mid', 'midroll', 1800, 2100),
      e('pre2', 'preroll', 2100, 2400),
      e('b', 'program', 2400, 3600),
    ]);
    expect(spans(cells), [['a', 0, 2100], ['b', 2100, 3600]]);
    expect(cells[0].displayEnd.difference(cells[0].displayStart), const Duration(minutes: 35));
    expect(cells[0].absorbed.map((x) => x.id), ['pre', 'post', 'mid']);
  });

  test('filler at the start only: the preroll belongs to the program after it', () {
    expect(spans(buildGuideCells([e('pre', 'preroll', 0, 300), e('a', 'program', 300, 1500)])), [['a', 0, 1500]]);
  });

  test('filler in the middle: a midroll between two programs belongs to the one before it', () {
    expect(spans(buildGuideCells([e('a', 'program', 0, 1200), e('mid', 'midroll', 1200, 1500), e('b', 'program', 1500, 2700)])),
        [['a', 0, 1500], ['b', 1500, 2700]]);
  });

  test('filler at the end only: the postroll belongs to the program before it', () {
    expect(spans(buildGuideCells([e('a', 'program', 0, 1200), e('post', 'postroll', 1200, 1500)])), [['a', 0, 1500]]);
  });

  test('two adjacent fillers go to the same program', () {
    final cells = buildGuideCells([e('a', 'program', 0, 1200), e('post', 'postroll', 1200, 1500), e('mid', 'midroll', 1500, 1800), e('b', 'program', 1800, 3000)]);
    expect(spans(cells), [['a', 0, 1800], ['b', 1800, 3000]]);
    expect(cells[0].absorbed.map((x) => x.id), ['post', 'mid']);
  });

  test('a first entry that is a filler whose program started before the window goes to the next one', () {
    // The previous program is not in the list (it lies before the window).
    expect(spans(buildGuideCells([e('post', 'postroll', 0, 300), e('b', 'program', 300, 1500)])), [['b', 0, 1500]]);
  });

  // The tail cap is 30 minutes (1800 s). A tail's attribution is measured from
  // the start of its run; what lies past it belongs to no program.
  group('tail attribution cap', () {
    test('a tail shorter than the cap is absorbed whole', () {
      final cells = buildGuideCells([e('a', 'program', 0, 1200), e('t', 'tail', 1200, 2400)]);
      expect(spans(cells), [['a', 0, 2400]]);
      expect(cells[0].absorbed.map((x) => x.id), ['t']);
    });

    test('a tail exactly at the cap is absorbed whole', () {
      expect(spans(buildGuideCells([e('a', 'program', 0, 1200), e('t', 'tail', 1200, 3000)])), [['a', 0, 3000]]);
    });

    test('a longer tail is absorbed for the cap only, the rest is a gap', () {
      final cells = buildGuideCells([e('a', 'program', 0, 1200), e('t', 'tail', 1200, 9000), e('b', 'program', 9000, 9600)]);
      expect(spans(cells), [['a', 0, 3000], ['b', 9000, 9600]]);
      expect(cells[0].absorbed.map((x) => x.id), ['t']);
      expect(cells[1].absorbed, isEmpty);
    });

    test('back-to-back tail entries share one cap', () {
      final cells = buildGuideCells([
        e('a', 'program', 0, 1200),
        e('t1', 'tail', 1200, 1800),
        e('t2', 'tail', 1800, 3000),
        e('t3', 'tail', 3000, 3600),
        e('b', 'program', 3600, 4200),
      ]);
      expect(spans(cells), [['a', 0, 3000], ['b', 3600, 4200]]);
      expect(cells[0].absorbed.map((x) => x.id), ['t1', 't2']);
      expect(cells[1].absorbed, isEmpty);
    });

    test('the cap runs from the start of the tail, not from the end of the program', () {
      final cells = buildGuideCells([e('a', 'program', 0, 1200), e('post', 'postroll', 1200, 1500), e('t', 'tail', 1500, 9000)]);
      expect(spans(cells), [['a', 0, 3300]]);
    });

    test('a tail with no previous program falls to the next one only when it fits the cap', () {
      expect(spans(buildGuideCells([e('t', 'tail', 0, 1800), e('a', 'program', 1800, 2400)])), [['a', 0, 2400]]);
      expect(spans(buildGuideCells([e('t', 'tail', 0, 3600), e('a', 'program', 3600, 4200)])), [['a', 3600, 4200]]);
    });

    test('the part of a tail past the cap never goes to the next program', () {
      final cells = buildGuideCells([e('a', 'program', 0, 1200), e('t1', 'tail', 1200, 3000), e('t2', 'tail', 3000, 3300), e('b', 'program', 3300, 4000)]);
      expect(spans(cells), [['a', 0, 3000], ['b', 3300, 4000]]);
    });

    test('a tail followed by the next program\'s preroll: the preroll still belongs to the next program', () {
      final cells = buildGuideCells([e('a', 'program', 0, 1200), e('t', 'tail', 1200, 9000), e('pre', 'preroll', 9000, 9300), e('b', 'program', 9300, 10000)]);
      expect(spans(cells), [['a', 0, 3000], ['b', 9000, 10000]]);
      expect(cells[1].absorbed.map((x) => x.id), ['pre']);
    });

    test('the cap is a parameter', () {
      final input = [e('a', 'program', 0, 100), e('t', 'tail', 100, 1000)];
      expect(spans(buildGuideCells(input, tailCap: const Duration(seconds: 60))), [['a', 0, 160]]);
    });

    test('the lookback both views read stays inside what the cache keeps, and covers the cap', () {
      expect(guideContextLookback, lessThanOrEqualTo(cachePastRetention));
      expect(guideContextLookback, greaterThan(defaultTailAttributionCap));
    });
  });

  group('guideVerticalTarget', () {
    DateTime t(int sec) => DateTime.fromMillisecondsSinceEpoch(sec * 1000, isUtc: true);
    const now = 1000;

    // Row 1: a long program started well before now. Row 2: a short finished
    // program covering row 1's start, then the live one. Row 3: the same
    // shape, shifted so the short one covers row 2's live start.
    final row1 = buildGuideCells([e('long1', 'program', 100, 5000)]);
    final row2 = buildGuideCells([e('old2', 'program', 0, 200), e('live2', 'program', 200, 5000)]);
    final row3 = buildGuideCells([e('old3a', 'program', 0, 100), e('old3b', 'program', 100, 300), e('live3', 'program', 300, 5000)]);
    final row4 = buildGuideCells([e('old4', 'program', 0, 150), e('mid4', 'program', 150, 400), e('live4', 'program', 400, 5000)]);
    final gapRow = buildGuideCells([e('old', 'program', 0, 200), e('later', 'program', 2000, 3000)]);

    String id(List<GuideCell> row, int i) => row[i].entry.id;

    // A tiny model of the overlay's focus: a vertical move sets the anchor to
    // now when it followed the live column, a horizontal move to the start of
    // the new cell (as _followFocusHorizontally does).
    late List<List<GuideCell>> rows;
    late int r;
    late int c;
    late DateTime anchor;
    void vertical(int delta) {
      final target = guideVerticalTarget(rows[r + delta], anchor: anchor, now: t(now), fromLive: rows[r][c].isLiveAt(t(now)));
      r += delta;
      c = target.index;
      anchor = target.landedLive ? t(now) : rows[r][c].displayStart;
    }

    void horizontal(int delta) {
      c += delta;
      anchor = rows[r][c].displayStart;
    }

    setUp(() {
      rows = [row1, row2, row3, row4];
      r = 0;
      c = 0;
      anchor = row1[0].displayStart;
    });

    test('live goes to the live cell of the next row, not the one covering its start', () {
      expect(id(row2, guideIndexForTime(row2, row1[0].displayStart)), 'old2');
      final target = guideVerticalTarget(row2, anchor: row1[0].displayStart, now: t(now), fromLive: true);
      expect(id(row2, target.index), 'live2');
      expect(target.landedLive, isTrue);
    });

    test('live goes up to the live cell as well', () {
      final target = guideVerticalTarget(row2, anchor: t(100), now: t(now), fromLive: true);
      expect(id(row2, target.index), 'live2');
    });

    test('live with no live cell in the target row falls back to the anchor rule', () {
      final target = guideVerticalTarget(gapRow, anchor: t(150), now: t(now), fromLive: true);
      expect(id(gapRow, target.index), 'old');
      expect(target.landedLive, isFalse);
    });

    test('non-live keeps the anchor rule', () {
      final target = guideVerticalTarget(row2, anchor: t(100), now: t(now), fromLive: false);
      expect(id(row2, target.index), 'old2');
      expect(target.landedLive, isFalse);
    });

    test('repeated Down keeps the live column, and Up comes back along it', () {
      vertical(1);
      expect(id(rows[r], c), 'live2');
      vertical(1);
      expect(id(rows[r], c), 'live3');
      expect(anchor, t(now));
      vertical(1);
      expect(id(rows[r], c), 'live4');
      vertical(-1);
      expect(id(rows[r], c), 'live3');
    });

    test('Left then Down re-anchors on the non-live cell; Right back to live follows the live column again', () {
      vertical(1);
      vertical(1);
      horizontal(-1);
      expect(id(rows[r], c), 'old3b');
      vertical(1);
      expect(id(rows[r], c), 'old4', reason: 'old3b starts at 100, which old4 covers');
      horizontal(1);
      horizontal(1);
      expect(id(rows[r], c), 'live4');
      vertical(-1);
      expect(id(rows[r], c), 'live3');
    });
  });

  // Segments of one split item: program entries sharing a splitGroup, with the
  // item's midroll breaks between them, are ONE program. Mirrors the "segments"
  // cases of webui/test/guideDisplay.test.ts.
  group('split items', () {
    EpgEntry seg(String id, int start, int end, String group, [int offsetMs = 0]) => EpgEntry(
          id: id,
          kind: 'program',
          catalogItemId: 'ci-item',
          startAt: DateTime.fromMillisecondsSinceEpoch(start * 1000, isUtc: true),
          endAt: DateTime.fromMillisecondsSinceEpoch(end * 1000, isUtc: true),
          itemOffsetMs: offsetMs,
          splitGroup: group,
        );

    test('segments of one item with a midroll between them are one program', () {
      final cells = buildGuideCells([seg('a1', 0, 100, 'g'), e('mid', 'midroll', 100, 110), seg('a2', 110, 200, 'g', 100000)]);
      expect(spans(cells), [['a1', 0, 200]]);
      expect(cells[0].absorbed.map((x) => x.id), ['mid', 'a2']);
    });

    test('three segments, preroll before and postroll after: one program over the whole span', () {
      final cells = buildGuideCells([
        e('pre', 'preroll', 0, 10),
        seg('a1', 10, 100, 'g'),
        e('m1', 'midroll', 100, 110),
        seg('a2', 110, 200, 'g', 90000),
        e('m2', 'midroll', 200, 210),
        seg('a3', 210, 300, 'g', 180000),
        e('post', 'postroll', 300, 310),
        e('b', 'program', 310, 400),
      ]);
      expect(spans(cells), [['a1', 0, 310], ['b', 310, 400]]);
    });

    test('the same item twice in a row without a split group stays two programs', () {
      final cells = buildGuideCells([e('a', 'program', 0, 100), e('mid', 'midroll', 100, 110), e('b', 'program', 110, 200)]);
      expect(spans(cells), [['a', 0, 110], ['b', 110, 200]]);
    });

    test('two airings of one item are two programs, each with its own group', () {
      final cells = buildGuideCells([
        seg('a1', 0, 100, 'g1'),
        e('m', 'midroll', 100, 110),
        seg('a2', 110, 200, 'g1', 100000),
        e('m2', 'midroll', 200, 210),
        seg('b1', 210, 300, 'g2'),
        e('m3', 'midroll', 300, 310),
        seg('b2', 310, 400, 'g2', 100000),
      ]);
      expect(spans(cells), [['a1', 0, 210], ['b1', 210, 400]]);
    });

    test('a window that starts at a later segment shows that segment as a program of its own', () {
      final cells = buildGuideCells([e('m', 'midroll', 100, 110), seg('a2', 110, 200, 'g', 100000)]);
      expect(spans(cells), [['a2', 100, 200]]);
    });

    test('a tail after a split item is capped from the last segment as for any program', () {
      final cells = buildGuideCells(
        [seg('a1', 0, 100, 'g'), e('mid', 'midroll', 100, 110), seg('a2', 110, 200, 'g', 100000), e('t', 'tail', 200, 9000)],
        tailCap: const Duration(seconds: 60),
      );
      expect(spans(cells), [['a1', 0, 260]]);
    });

    test('the merged cell is live from the first segment to the last, breaks included', () {
      final cells = buildGuideCells([seg('a1', 0, 100, 'g'), e('mid', 'midroll', 100, 110), seg('a2', 110, 200, 'g', 100000)]);
      DateTime at(int s) => DateTime.fromMillisecondsSinceEpoch(s * 1000, isUtc: true);
      expect(cells[0].isLiveAt(at(50)), isTrue);
      expect(cells[0].isLiveAt(at(105)), isTrue);
      expect(cells[0].isLiveAt(at(150)), isTrue);
      expect(cells[0].isLiveAt(at(200)), isFalse);
    });
  });
}
