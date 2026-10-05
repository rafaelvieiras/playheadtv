// Unit test for how the player's known programs grow from the offline cache,
// notably a tail run that reaches the end of the cached window and keeps
// getting longer as the window rolls.
import 'package:client/main.dart' show growKnown;
import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';

final _t0 = DateTime.utc(2026, 10, 3, 12);

EpgEntry _program(String id, int startMin, int lenMin) => EpgEntry(
      id: id,
      kind: 'program',
      catalogItemId: 'ci-$id',
      externalId: 'jf-$id',
      title: id,
      startAt: _t0.add(Duration(minutes: startMin)),
      endAt: _t0.add(Duration(minutes: startMin + lenMin)),
    );

EpgEntry _tail(int startMin, int clips) => EpgEntry(
      id: 'tail',
      kind: 'tail',
      catalogItemId: 'ci-t0',
      startAt: _t0.add(Duration(minutes: startMin)),
      endAt: _t0.add(Duration(minutes: startMin, seconds: 10 * clips)),
      clips: [
        for (var i = 0; i < clips; i++)
          EpgClip(
            id: 'c$i',
            catalogItemId: 'ci-t$i',
            externalId: 'jf-t$i',
            title: 'Clip $i',
            startAt: _t0.add(Duration(minutes: startMin, seconds: 10 * i)),
            endAt: _t0.add(Duration(minutes: startMin, seconds: 10 * (i + 1))),
          ),
      ],
    );

void main() {
  test('nothing new in the cache means no growth', () {
    final a = _program('a', 0, 30);
    expect(growKnown(known: [a], knownExpanded: 1, cursor: a.endAt, cached: [a]), isNull);
  });

  test('entries starting at or after the cursor are appended and move the cursor', () {
    final a = _program('a', 0, 30);
    final b = _program('b', 30, 30);
    final g = growKnown(known: [a], knownExpanded: 1, cursor: a.endAt, cached: [a, b])!;
    expect(g.known.map((e) => e.id), ['a', 'b']);
    expect(g.cursor, b.endAt);
    expect(g.extraUnits, isEmpty);
  });

  test('a longer version of the last tail run replaces it and yields only its new clips', () {
    final a = _program('a', 0, 30);
    final oldTail = _tail(30, 6);
    final longer = _tail(30, 10);
    final g = growKnown(known: [a, oldTail], knownExpanded: 2, cursor: oldTail.endAt, cached: [longer])!;
    expect(g.known.map((e) => e.id), ['a', 'tail']);
    expect(g.known.last.clips.length, 10);
    expect(g.cursor, longer.endAt);
    expect(g.extraUnits.length, 4, reason: 'only the clips after the old end');
    expect(g.extraUnits.first.id, 'jf-t6');
    expect(g.extraUnits.every((u) => u.programOrdinal == 1), isTrue);
  });

  test('a longer tail run that was not expanded yet needs no extra units', () {
    final oldTail = _tail(0, 6);
    final g = growKnown(known: [oldTail], knownExpanded: 0, cursor: oldTail.endAt, cached: [_tail(0, 9)])!;
    expect(g.known.single.clips.length, 9);
    expect(g.extraUnits, isEmpty);
  });

  test('the same tail run, or a run that starts elsewhere, is not growth', () {
    final tail = _tail(30, 6);
    expect(growKnown(known: [tail], knownExpanded: 1, cursor: tail.endAt, cached: [tail]), isNull);
    expect(growKnown(known: [tail], knownExpanded: 1, cursor: tail.endAt, cached: [_tail(29, 20)]), isNull);
  });

  test('a zero-length entry is ignored', () {
    final a = _program('a', 0, 30);
    expect(growKnown(known: [a], knownExpanded: 1, cursor: a.endAt, cached: [_program('z', 30, 0)]), isNull);
  });
}
