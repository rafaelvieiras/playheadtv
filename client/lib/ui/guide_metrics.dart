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

  /// The channel column: number, plus a logo when the channel has one.
  final double channelColumnWidth;

  /// One channel row.
  final double rowHeight;

  /// Whether the channel column shows the channel's name next to its number. A
  /// phone drops it: the column is 72 px there, and a name clipped to two
  /// characters reads worse than no name at all.
  final bool channelName;

  /// The header above the grid (date label and the time ticks).
  final double headerHeight;

  const GuideMetrics({
    required this.visibleSpan,
    required this.channelColumnWidth,
    required this.rowHeight,
    required this.headerHeight,
    this.channelName = true,
  });

  /// What the overlay used before the phone existed.
  static const tv = GuideMetrics(
    visibleSpan: Duration(hours: 3),
    channelColumnWidth: 220,
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
    channelName: false,
  );

  static GuideMetrics of(ClientFormFactor formFactor) =>
      formFactor == ClientFormFactor.phone ? phone : tv;
}
