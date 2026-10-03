// Unit test for the guide's hidden-filler attribution (lib/epg_guide.dart).
// The cases mirror webui/test/guideDisplay.test.ts one for one.
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
}
