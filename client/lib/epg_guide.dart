// What the guide shows for one channel's entries: only programs (and
// fallbacks) get a cell; preroll, midroll, postroll and tail entries are
// hidden and the time they occupy is attributed to a neighbouring program, so
// the grid has no false holes. Pure and dependency-free; the same rules live
// in webui/src/lib/guideDisplay.ts.
//
// Attribution, per hidden entry:
//  - a preroll extends the NEXT program backwards (its displayed start moves
//    earlier);
//  - a postroll, midroll or tail extends the PREVIOUS program forwards (its
//    displayed end moves later);
//  - with no program on that side, it goes to the other neighbour; with none
//    there either, the entry stays unattributed and its time shows as a gap.
// A neighbour only absorbs an entry that touches it (within `tolerance`,
// counting what it already absorbed), so a real gap next to a filler is never
// painted over. A fallback is a visible cell of its own and absorbs nothing.
//
// A tail can last hours, so its attribution is capped: the previous program
// absorbs at most the first `tailAttributionCap` of a run of tail entries,
// measured from the start of the run. The rest of the tail is attributed to no
// program (not to the next one either) and shows as a gap; the OSD names the
// channel then (lib/osd_subject.dart). A tail with no previous program can only
// fall to the next program when it fits in the cap as a whole. The other
// hidden kinds are short and keep their rule.
import 'playhead_api.dart';

/// How much of a tail the previous program absorbs by default.
const defaultTailAttributionCap = Duration(minutes: 30);

/// The cap in force. A variable only so integration tests can shrink it to
/// seconds (`AppConfig.overrideForTest`); nothing else writes it.
Duration tailAttributionCap = defaultTailAttributionCap;

/// How far before a view's first instant the guide and the OSD both read the
/// cache, so that they attribute a tail identically. The query returns every
/// entry overlapping the span, so the previous program is found whatever its
/// length; what the span must cover is the time between that program's end and
/// the tail's start (its postroll/midroll run) plus the tail's capped
/// attribution. Two hours is that with a wide margin. Must stay below
/// `cachePastRetention` (lib/cache/schedule_cache.dart), which a unit test
/// checks.
const guideContextLookback = Duration(hours: 2);

const _hiddenGuideKinds = {'preroll', 'midroll', 'postroll', 'tail'};

/// True for the entry kinds the guide never shows as a cell of their own.
bool isHiddenGuideKind(String kind) => _hiddenGuideKinds.contains(kind);

/// One visible guide cell: the entry plus the span the guide draws for it,
/// which is the entry's own span widened by the hidden entries attributed to
/// it. The entry's own `startAt`/`endAt` stay the real airtime; "on air now"
/// for a cell is [isLiveAt], so a live filler counts as its program.
class GuideCell {
  final EpgEntry entry;
  DateTime displayStart;
  DateTime displayEnd;
  final List<EpgEntry> absorbed = [];

  GuideCell(this.entry)
      : displayStart = entry.startAt,
        displayEnd = entry.endAt;

  bool isLiveAt(DateTime now) => !now.isBefore(displayStart) && now.isBefore(displayEnd);
}

/// The cells for [entries] (any order, not mutated), sorted by start.
List<GuideCell> buildGuideCells(Iterable<EpgEntry> entries, {Duration tolerance = const Duration(seconds: 1), Duration? tailCap}) {
  final cap = tailCap ?? tailAttributionCap;
  final sorted = [...entries]..sort((a, b) {
      final byStart = a.startAt.compareTo(b.startAt);
      return byStart != 0 ? byStart : a.endAt.compareTo(b.endAt);
    });
  final cells = <EpgEntry, GuideCell>{
    for (final e in sorted)
      if (!isHiddenGuideKind(e.kind)) e: GuideCell(e),
  };
  final done = <EpgEntry>{};

  // Whether the nearest visible entry before each hidden one is a program: a
  // tail that has one never falls to the next program.
  final hasPreviousProgram = <EpgEntry, bool>{};
  var lastVisibleIsProgram = false;
  for (final e in sorted) {
    final cell = cells[e];
    if (cell != null) {
      lastVisibleIsProgram = e.kind == 'program';
    } else {
      hasPreviousProgram[e] = lastVisibleIsProgram;
    }
  }
  // Where the run of tail entries absorbed by a program starts.
  final tailStart = <GuideCell, DateTime>{};

  // Walks toward `next` (from the end backwards, so the nearest following
  // visible entry is known) or `previous`, and hands every hidden entry
  // accepted by `pick` to the nearest visible entry on that side when it is a
  // program that touches it.
  void attribute(bool Function(EpgEntry) pick, {required bool next}) {
    final order = next ? sorted.reversed.toList() : sorted;
    GuideCell? neighbour;
    for (final e in order) {
      final cell = cells[e];
      if (cell != null) {
        neighbour = e.kind == 'program' ? cell : null;
        continue;
      }
      if (neighbour == null || done.contains(e) || !pick(e)) continue;
      if (next) {
        if (e.endAt.isBefore(neighbour.displayStart.subtract(tolerance))) continue;
        if (e.kind == 'tail' && (hasPreviousProgram[e]! || neighbour.entry.startAt.difference(e.startAt) > cap)) continue;
        if (e.startAt.isBefore(neighbour.displayStart)) neighbour.displayStart = e.startAt;
        neighbour.absorbed.insert(0, e);
      } else {
        if (e.startAt.isAfter(neighbour.displayEnd.add(tolerance))) continue;
        var end = e.endAt;
        if (e.kind == 'tail') {
          final limit = (tailStart[neighbour] ?? e.startAt).add(cap);
          if (!e.startAt.isBefore(limit)) continue;
          tailStart[neighbour] ??= e.startAt;
          if (end.isAfter(limit)) end = limit;
        }
        if (end.isAfter(neighbour.displayEnd)) neighbour.displayEnd = end;
        neighbour.absorbed.add(e);
      }
      done.add(e);
    }
  }

  attribute((e) => e.kind == 'preroll', next: true);
  attribute((e) => e.kind != 'preroll', next: false);
  attribute((e) => e.kind == 'preroll', next: false);
  attribute((e) => e.kind != 'preroll', next: true);

  return [for (final e in sorted) if (cells[e] != null) cells[e]!];
}
