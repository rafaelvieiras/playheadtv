// What the player OSD describes while an entry is on air: the guide's program,
// never a filler. A hidden-kind entry (preroll, midroll, postroll, tail) is
// part of the program the guide attributes it to (`buildGuideCells`, the one
// attribution rule shared with the EPG), so the OSD keeps showing that
// program, over its displayed span (the program plus the filler absorbed by
// it). A fallback is a cell of its own and is shown as itself. Pure.
import 'epg_guide.dart';
import 'playhead_api.dart';

/// How far before the tune-in the client looks for the program a filler that
/// is already on air belongs to (a midroll, postroll or tail attributed to the
/// previous program, which ended before the first fetched entry).
const osdContextLookback = Duration(hours: 6);

class OsdSubject {
  /// The program (or fallback) the OSD describes.
  final EpgEntry program;

  /// The displayed span: the program's own span widened by its filler.
  final DateTime start;
  final DateTime end;

  /// The next visible program after this one, or null when not known yet.
  final EpgEntry? next;

  /// True when the entry on air is filler shown as the program it belongs to.
  final bool attributed;

  const OsdSubject({required this.program, required this.start, required this.end, this.next, this.attributed = false});

  /// Identifies what is announced: it only changes when the program does, so
  /// a filler of the same program never re-announces.
  String get key => program.id;
}

/// The subject for [current], one of [entries] (the known schedule of the
/// channel, any order). A hidden entry no cell absorbed (nothing to attribute
/// it to) is described as itself, over its own span.
OsdSubject osdSubjectFor(Iterable<EpgEntry> entries, EpgEntry current, {Duration tolerance = const Duration(seconds: 1)}) {
  final cells = buildGuideCells(entries, tolerance: tolerance);
  for (var i = 0; i < cells.length; i++) {
    final cell = cells[i];
    final owns = cell.entry.id == current.id || cell.absorbed.any((a) => a.id == current.id);
    if (!owns) continue;
    return OsdSubject(
      program: cell.entry,
      start: cell.displayStart,
      end: cell.displayEnd,
      next: i + 1 < cells.length ? cells[i + 1].entry : null,
      attributed: cell.entry.id != current.id,
    );
  }
  EpgEntry? next;
  for (final c in cells) {
    if (!c.displayStart.isBefore(current.endAt.subtract(tolerance))) {
      next = c.entry;
      break;
    }
  }
  return OsdSubject(program: current, start: current.startAt, end: current.endAt, next: next);
}
