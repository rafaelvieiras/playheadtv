// What the player OSD describes while an entry is on air: the guide's program,
// never a filler. A hidden-kind entry (preroll, midroll, postroll, tail) is
// part of the program the guide attributes it to (`buildGuideCells`, the one
// attribution rule shared with the EPG), so the OSD keeps showing that
// program, over its displayed span (the program plus the filler absorbed by
// it). A fallback is a cell of its own and is shown as itself. A tail is
// attributed to the previous program only for the capped time
// (`tailAttributionCap`); past it, or with no program to attribute it to, the
// OSD names the channel and shows no program details. Pure.
import 'epg_guide.dart';
import 'playhead_api.dart';

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

  /// True when nothing describes the airtime but the channel itself (a tail
  /// past its capped attribution): no program details, no progress.
  final bool channelOnly;

  const OsdSubject({required this.program, required this.start, required this.end, this.next, this.attributed = false, this.channelOnly = false});

  /// Identifies what is announced: it only changes when the program does, so
  /// a filler of the same program never re-announces.
  String get key => channelOnly ? 'channel' : program.id;
}

/// The subject for [current], one of [entries] (the known schedule of the
/// channel, any order) at [now] (needed to tell when a partly attributed tail
/// has passed its cap; null counts as inside it). A hidden entry no cell
/// absorbed (nothing to attribute it to) is described as itself, over its own
/// span, except a tail, which is the channel's.
OsdSubject osdSubjectFor(Iterable<EpgEntry> entries, EpgEntry current,
    {Duration tolerance = const Duration(seconds: 1), DateTime? now, Duration? tailCap}) {
  final cells = buildGuideCells(entries, tolerance: tolerance, tailCap: tailCap);
  OsdSubject channelOnly(int nextIndex) => OsdSubject(
      program: current,
      start: current.startAt,
      end: current.endAt,
      next: nextIndex >= 0 && nextIndex < cells.length ? cells[nextIndex].entry : null,
      channelOnly: true);
  for (var i = 0; i < cells.length; i++) {
    final cell = cells[i];
    final owns = cell.entry.id == current.id || cell.absorbed.any((a) => a.id == current.id);
    if (!owns) continue;
    if (current.kind == 'tail' && now != null && !now.isBefore(cell.displayEnd)) return channelOnly(i + 1);
    return OsdSubject(
      program: cell.entry,
      start: cell.displayStart,
      end: cell.displayEnd,
      next: i + 1 < cells.length ? cells[i + 1].entry : null,
      attributed: cell.entry.id != current.id,
    );
  }
  var nextIndex = cells.indexWhere((c) => !c.displayStart.isBefore(current.endAt.subtract(tolerance)));
  if (current.kind == 'tail') return channelOnly(nextIndex);
  final next = nextIndex >= 0 ? cells[nextIndex].entry : null;
  return OsdSubject(program: current, start: current.startAt, end: current.endAt, next: next);
}
