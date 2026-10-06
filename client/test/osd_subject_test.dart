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
    expect(secs(pre2.end), 5400, reason: 'B plus the first 30 minutes of the tail');
    expect(pre2.next, isNull);
  });

  DateTime at(int seconds) => DateTime.fromMillisecondsSinceEpoch(seconds * 1000, isUtc: true);

  test('a tail inside its cap is described as the program before it', () {
    for (final t in [3600, 4000, 5399]) {
      final s = osdSubjectFor(lineup, lineup.last, now: at(t));
      expect(s.program.id, 'b', reason: '$t');
      expect(s.attributed, isTrue);
      expect(s.channelOnly, isFalse);
      expect(secs(s.end), 5400);
    }
  });

  test('a tail past its cap is the channel\'s, with no program', () {
    final s = osdSubjectFor(lineup, lineup.last, now: at(5400));
    expect(s.channelOnly, isTrue);
    expect(s.key, 'channel');
    expect(osdSubjectFor(lineup, lineup.last, now: at(7000)).channelOnly, isTrue);
  });

  test('a tail clip that starts past the cap is the channel\'s', () {
    final clips = [e('b', 'program', 0, 600), e('t1', 'tail', 600, 2400), e('t2', 'tail', 2400, 3000)];
    expect(osdSubjectFor(clips, clips[1], now: at(700)).program.id, 'b');
    expect(osdSubjectFor(clips, clips[2], now: at(2500)).channelOnly, isTrue);
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
    final p = e('p', 'postroll', 0, 600);
    final s = osdSubjectFor([p], p);
    expect(s.program.id, 'p');
    expect(secs(s.end), 600);
  });

  test('a tail with nothing to attach to is the channel\'s', () {
    final t = e('t', 'tail', 0, 600);
    final s = osdSubjectFor([t], t, now: at(10));
    expect(s.channelOnly, isTrue);
  });

  test('a split item is one program: its segments and the break between them share one announcement', () {
    EpgEntry seg(String id, int start, int end, int offsetMs) => EpgEntry(
          id: id,
          kind: 'program',
          catalogItemId: 'ci-item',
          title: 'item',
          startAt: DateTime.fromMillisecondsSinceEpoch(start * 1000, isUtc: true),
          endAt: DateTime.fromMillisecondsSinceEpoch(end * 1000, isUtc: true),
          itemOffsetMs: offsetMs,
          splitGroup: 'g',
        );
    final entries = [seg('a1', 0, 600, 0), e('mid', 'midroll', 600, 780), seg('a2', 780, 1500, 600000), e('b', 'program', 1500, 2400)];
    for (final id in ['a1', 'mid', 'a2']) {
      final s = osdSubjectFor(entries, entries.firstWhere((x) => x.id == id));
      expect(s.program.id, 'a1', reason: id);
      expect(secs(s.start), 0, reason: id);
      expect(secs(s.end), 1500, reason: id);
      expect(s.key, 'a1', reason: id);
      expect(s.next?.id, 'b', reason: id);
    }
    // Progress by wall clock over the merged span: halfway through the break
    // is 690 of 1500 seconds.
    final s = osdSubjectFor(entries, entries[1]);
    expect(s.end.difference(s.start), const Duration(minutes: 25));
  });
}
