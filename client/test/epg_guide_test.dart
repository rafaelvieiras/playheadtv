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
}
