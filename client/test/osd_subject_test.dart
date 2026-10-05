// Unit test for what the player OSD describes while filler airs
// (lib/osd_subject.dart): always the guide's program, over its displayed span.
import 'package:client/osd_subject.dart';
import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';

// Times are in seconds to keep the cases readable.
EpgEntry e(String id, String kind, int start, int end) => EpgEntry(
      id: id,
      kind: kind,
      catalogItemId: 'ci-$id',
      title: id,
      startAt: DateTime.fromMillisecondsSinceEpoch(start * 1000, isUtc: true),
      endAt: DateTime.fromMillisecondsSinceEpoch(end * 1000, isUtc: true),
    );

int secs(DateTime t) => t.millisecondsSinceEpoch ~/ 1000;

void main() {
  // 20 minute episode with three 5 minute fillers: preroll, postroll, midroll.
  final lineup = [
    e('pre', 'preroll', 0, 300),
    e('a', 'program', 300, 1500),
    e('post', 'postroll', 1500, 1800),
    e('mid', 'midroll', 1800, 2100),
    e('pre2', 'preroll', 2100, 2400),
    e('b', 'program', 2400, 3600),
    e('tail', 'tail', 3600, 7200),
  ];

  test('every entry of the episode is described as the episode over its 35 minute span', () {
    for (final id in ['pre', 'a', 'post', 'mid']) {
      final s = osdSubjectFor(lineup, lineup.firstWhere((x) => x.id == id));
      expect(s.program.id, 'a', reason: id);
      expect(secs(s.start), 0, reason: id);
      expect(secs(s.end), 2100, reason: id);
      expect(s.end.difference(s.start), const Duration(minutes: 35), reason: id);
      expect(s.next?.id, 'b', reason: id);
      expect(s.attributed, id != 'a', reason: id);
    }
  });

  test('the next program skips the filler and a filler before it shows its own program', () {
    final pre2 = osdSubjectFor(lineup, lineup.firstWhere((x) => x.id == 'pre2'));
    expect(pre2.program.id, 'b');
    expect(secs(pre2.start), 2100);
    expect(secs(pre2.end), 7200, reason: 'B plus the tail attributed to it');
    expect(pre2.next, isNull);
  });

  test('a tail is described as the program before it', () {
    final s = osdSubjectFor(lineup, lineup.last);
    expect(s.program.id, 'b');
    expect(s.attributed, isTrue);
  });

  test('the key only changes with the program, so a filler never re-announces', () {
    final keys = [for (final x in lineup) osdSubjectFor(lineup, x).key];
    expect(keys, ['a', 'a', 'a', 'a', 'b', 'b', 'b']);
  });

  test('a first entry that is a filler uses the context before it', () {
    final context = [e('a', 'program', 0, 1200)];
    final first = e('post', 'postroll', 1200, 1500);
    final withContext = osdSubjectFor([...context, first, e('b', 'program', 1500, 2700)], first);
    expect(withContext.program.id, 'a');
    expect(secs(withContext.end), 1500);
    // Without the context it falls to the other neighbour, like the guide.
    final without = osdSubjectFor([first, e('b', 'program', 1500, 2700)], first);
    expect(without.program.id, 'b');
  });

  test('a fallback is described as itself', () {
    final f = e('f', 'fallback', 0, 600);
    final s = osdSubjectFor([f, e('a', 'program', 600, 1200)], f);
    expect(s.program.id, 'f');
    expect(s.attributed, isFalse);
    expect(s.next?.id, 'a');
  });

  test('a filler with nothing to attach to is described as itself', () {
    final t = e('t', 'tail', 0, 600);
    final s = osdSubjectFor([t], t);
    expect(s.program.id, 't');
    expect(secs(s.end), 600);
  });
}
