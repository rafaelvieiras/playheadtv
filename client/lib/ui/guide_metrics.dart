// The guide's design units, one const object per layout family.
//
// Same seam as `OsdMetrics`: the screen asks for a metrics object instead of
// branching on the form factor, and `GuideMetrics.tv` is the numbers the overlay
// has always used, so the television baselines stay byte-identical.
//
// Why the phone needs its own: the guide's cells are proportional. A fixed 220
// design px channel column and a 3 h span put a half-hour program at 24 dp on a
// 360 dp-wide screen — narrower than any touch target and too narrow for a
// title. Shrinking the column and the span is the whole fix: the grid keeps its
// proportions and the cells come back to ~90 dp in portrait, ~230 dp in
// landscape.
import 'form_factor.dart';

class GuideMetrics {
  /// How much of the time axis is on screen at once.
  final Duration visibleSpan;

  /// The channel column: a small number, the logo when the channel has one, and
  /// the name clipped after them.
  final double channelColumnWidth;

  /// One channel row.
  final double rowHeight;

  /// Whether the channel column shows the channel's name next to its number. A
  /// phone drops it: the column is 72 px there, and a name clipped to two
  /// characters reads worse than no name at all.
  final bool channelName;

  /// The header above the grid (date label and the time ticks).
  final double headerHeight;

  /// How many lines of synopsis the details panel shows in its compact form.
  /// The panel sits above the grid, so every line it keeps costs a row of
  /// programs: a phone has far less height to spare than a television.
  final int detailsSynopsisLines;

  /// A multiplier for the details panel's own type and padding (a phone draws it
  /// smaller, for the same reason).
  final double detailsScale;

  const GuideMetrics({
    required this.visibleSpan,
    required this.channelColumnWidth,
    required this.rowHeight,
    required this.headerHeight,
    this.detailsSynopsisLines = 3,
    this.detailsScale = 1,
    this.channelName = true,
  });

  /// What the overlay used before the phone existed.
  static const tv = GuideMetrics(
    visibleSpan: Duration(hours: 3),
    channelColumnWidth: 260,
    rowHeight: 84,
    headerHeight: 44,
  );

  /// 90 minutes on screen at once, a 72 px channel column (number and logo), the
  /// same row height as a television — a list of channels is no more compact on
  /// a phone, and 84 px is a comfortable row.
  static const phone = GuideMetrics(
    visibleSpan: Duration(minutes: 90),
    channelColumnWidth: 72,
    rowHeight: 84,
    headerHeight: 44,
    detailsSynopsisLines: 2,
    detailsScale: 0.82,
    channelName: false,
  );

  static GuideMetrics of(ClientFormFactor formFactor) =>
      formFactor == ClientFormFactor.phone ? phone : tv;
}
