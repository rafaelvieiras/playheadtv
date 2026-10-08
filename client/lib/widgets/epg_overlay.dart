// Full-screen EPG: schedule grid of all channels, with a thumbnail of the
// video that is already playing reserved in a corner (see
// `_videoThumbnailRect`/`videoThumbnailRect` in main.dart — this widget NEVER
// draws anything on top of that rectangle, it only reserves the "hole" in the
// darkened background; the thumbnail itself is another widget, the real video,
// drawn underneath).
//
// Background: 4 semi-opaque black rectangular bands around the thumbnail's
// rectangle (top, bottom — where the whole grid/details panel lives —, left
// and right), instead of a ClipPath — simpler and enough since the rectangle
// is always axis-aligned.
//
// Layout: details panel, then a TV-guide grid — a sticky time header (date at
// its left, a tick every 30 min), one fixed-height row per channel (channel
// column pinned on the left) and a vertical "now" line. The visible time span
// is a constant (`_visibleSpan`); the pixels-per-minute scale is derived from
// the available width on every build, so the grid adapts to any screen size.
// Horizontal scrolling is a single animated offset in MINUTES from the loaded
// window's start (resize-invariant) shared by the header, the rows and the
// now marker; the vertical list follows the focused row. Moving right past the
// loaded window lazily fetches the next slice of `GET /epg`.
//
// What a row shows (`buildGuideCells`, lib/epg_guide.dart): only programs and
// fallbacks. Preroll, midroll, postroll and tail entries have no cell; the
// time they occupy is attributed to a neighbouring program, whose cell is
// drawn (and focused, and highlighted as live, and described in the details
// panel) over that widened span. Navigation only ever sees the cells, so focus
// never lands on a hidden entry and no cell is skipped.
//
// Navigation: row = channel (`moveRow`, called by the parent on RAW
// arrowUp/arrowDown — Up = previous row — bypassing KeyBindingsService),
// column = program within the row (`moveColumn`, called by the parent on RAW
// arrowLeft/arrowRight — bypassing KeyBindingsService, same decision already
// made in MosaicPage for 2D grid navigation). `select` (Enter) has a
// long-press: the parent relays both the KeyDownEvent and the KeyUpEvent to
// `handleSelectKeyEvent` (the only screen in the app that needs the KeyUp) —
// holding >500ms expands the details panel of the focused program without
// switching channel; a short tap activates (switches channel) only if the
// focused program is live right now.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../epg_guide.dart';
import '../cache/schedule_cache.dart';
import '../main.dart' show jellyfinImageUrl;
import '../playhead_api.dart';
import '../ui/ui_scale.dart';
import 'app_icons.dart';
import 'art_image.dart';

// How much time the grid shows at once; pixels per minute follow from the
// available width.
const _visibleSpan = Duration(hours: 3);
const _tickInterval = Duration(minutes: 30);
// How much of the past the grid starts with, and how much each step of the
// window adds. The offline cache only holds its window (the smaller of the
// backend's and the client's limit, `ScheduleCache.window`), so the grid never
// reaches beyond that.
const _initialWindowAhead = Duration(hours: 4);
const _extendStep = Duration(hours: 3);
const _extendMargin = Duration(hours: 1);
const _nowTickInterval = Duration(seconds: 10);
const _scrollAnimation = Duration(milliseconds: 150);
const _longPressThreshold = Duration(milliseconds: 500);

// Sizes are pixels at 1080p; `EpgOverlayState._u` multiplies them by the shared
// UiScale (lib/ui/ui_scale.dart). The time axis is not part of this: the 3h
// span is fitted to the available width in pixels-per-minute, as before.
const _channelColumnWidth = 220.0;
const _rowHeight = 84.0;
const _headerHeight = 44.0;
const _cellMarginH = 2.0;

class EpgOverlay extends StatefulWidget {
  final List<PlayheadChannel> channels;
  final ScheduleCache cache;
  final String currentChannelId;
  final Rect videoThumbnailRect; // SAME rectangle as _videoThumbnailRect — never draw anything opaque on top of it
  final void Function(String channelId) onSelectLiveChannel;
  final VoidCallback onClose;

  /// The wall clock; tests substitute a fixed one (the golden tests).
  final DateTime Function() clock;

  const EpgOverlay({
    required Key key,
    required this.channels,
    required this.cache,
    required this.currentChannelId,
    required this.videoThumbnailRect,
    required this.onSelectLiveChannel,
    required this.onClose,
    this.clock = DateTime.now,
  }) : super(key: key);

  @override
  State<EpgOverlay> createState() => EpgOverlayState();
}

class EpgOverlayState extends State<EpgOverlay> {
  bool _loading = true;
  String? _error;

  // Every entry fetched so far per channel (fillers included), in the SAME
  // ORDER as widget.channels, and the guide cells built from them — each row
  // index of the grid is the same index into both. Only the cells are drawn
  // and navigated.
  List<List<EpgEntry>> _rawByChannel = [];
  List<List<GuideCell>> _entriesByChannel = [];
  late DateTime _windowStart;
  late DateTime _windowEnd;
  late DateTime _maxWindowEnd;
  bool _extending = false;
  StreamSubscription<CacheChange>? _cacheSub;

  int _focusedRow = 0;
  int _focusedCol = 0;
  bool _detailsExpanded = false;

  // Time the focus "sits at": moving to another row picks the program there
  // that covers this instant, so Up/Down feels like moving along the time axis.
  // A vertical move from a live program instead lands on the adjacent row's
  // live program (and the anchor becomes "now"); see guideVerticalTarget.
  late DateTime _anchor;

  // Left edge of the visible span, in minutes from _windowStart.
  double _hOffsetMinutes = 0;

  // Written by the grid's LayoutBuilders (plain fields, no setState needed).
  double _rowsViewportHeight = 0;

  late DateTime _now;
  Timer? _nowTimer;

  Timer? _longPressTimer;
  bool _longPressFired = false;

  final ScrollController _verticalController = ScrollController();

  // The shared UI scale, refreshed in didChangeDependencies. A plain field so
  // that callbacks outside build can use it; only this widget subscribes, never
  // the player page below it.
  double _scale = 1.0;
  double _u(double px) => px * _scale;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final next = UiScale.of(context);
    if (next != _scale) {
      _scale = next;
      // Rows changed height: keep the focused one in view after the rebuild.
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted) _ensureRowVisible(animate: false);
      });
    }
  }

  @override
  void initState() {
    super.initState();
    _now = widget.clock();
    _nowTimer = Timer.periodic(_nowTickInterval, (_) {
      if (mounted) setState(() => _now = widget.clock());
    });
    unawaited(_load());
    _cacheSub = widget.cache.changes.listen((_) => _refreshFromCache());
  }

  @override
  void dispose() {
    _nowTimer?.cancel();
    unawaited(_cacheSub?.cancel());
    _longPressTimer?.cancel();
    _verticalController.dispose();
    super.dispose();
  }

  // Start of the 30-minute slot (in local time) containing `t`, as UTC.
  DateTime _floorToTick(DateTime t) {
    final local = t.toLocal();
    final floored = DateTime(
      local.year,
      local.month,
      local.day,
      local.hour,
      local.minute - local.minute % _tickInterval.inMinutes,
    );
    return floored.toUtc();
  }

  double _minutesFrom(DateTime t) => t.difference(_windowStart).inMilliseconds / 60000.0;

  double get _windowMinutes => _minutesFrom(_windowEnd);

  bool _isLive(GuideCell c, DateTime now) => c.isLiveAt(now);

  // Cells of one row. `raw` also holds the entries read from before the
  // window (`guideContextLookback`, the same reach the OSD reads), so a tail
  // is attributed to the same program as in the OSD; the cells that end before
  // the window are not part of the grid.
  List<GuideCell> _cellsOf(List<EpgEntry> raw) => [
    for (final c in buildGuideCells(raw))
      if (c.displayEnd.isAfter(_windowStart)) c,
  ];

  void _rebuildCells() {
    _entriesByChannel = [for (final raw in _rawByChannel) _cellsOf(raw)];
  }

  // A row's entries over the window, plus the lookback before it.
  List<EpgEntry> _readWindow(PlayheadChannel c) => _readEpg(c, _windowStart.subtract(guideContextLookback), _windowEnd);

  // The channel's entries in the span, read from the offline cache (the
  // guide never asks the backend): already enriched (titles, art references)
  // with consecutive tail clips grouped into one entry, so a tail shows as one
  // cell.
  List<EpgEntry> _readEpg(PlayheadChannel c, DateTime start, DateTime end) => widget.cache.entries(c.id, start, end);

  // A background sync changed the cache while the guide is open: the rows are
  // read again over the window already shown, keeping the focus where it is
  // (clamped if its row got shorter).
  void _refreshFromCache() {
    if (!mounted || _loading || _error != null) return;
    setState(() {
      _rawByChannel = [for (final c in widget.channels) _readWindow(c)];
      _rebuildCells();
      if (_focusedRow < _entriesByChannel.length) {
        final len = _entriesByChannel[_focusedRow].length;
        if (_focusedCol >= len) _focusedCol = len == 0 ? 0 : len - 1;
      }
    });
  }

  Future<void> _load() async {
    final now = widget.clock().toUtc();
    final start = _floorToTick(now.subtract(_tickInterval));
    final end = now.add(_initialWindowAhead);
    _windowStart = start;
    _windowEnd = end;
    _maxWindowEnd = widget.cache.horizonEnd;

    try {
      final results = [for (final c in widget.channels) _readWindow(c)];
      if (!mounted) return;

      final channelIndex = widget.channels.indexWhere((c) => c.id == widget.currentChannelId);
      final initialRow = channelIndex >= 0 ? channelIndex : 0;
      var initialCol = 0;
      final initialCells = [for (final raw in results) _cellsOf(raw)];
      if (initialRow < initialCells.length) {
        final liveIdx = initialCells[initialRow].indexWhere((c) => _isLive(c, now));
        if (liveIdx >= 0) initialCol = liveIdx;
      }

      setState(() {
        _rawByChannel = results;
        _rebuildCells();
        _focusedRow = initialRow;
        _focusedCol = initialCol;
        _anchor = now;
        _hOffsetMinutes = 0;
        _loading = false;
        _now = widget.clock();
        _followFocusHorizontally();
      });
      WidgetsBinding.instance.addPostFrameCallback((_) => _ensureRowVisible(animate: false));
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = 'ERROR: failed to load EPG: $e';
        _loading = false;
      });
    }
  }

  // Fetches the next slice of GET /epg for every channel and appends it.
  // Returns true when the window actually grew.
  Future<bool> _extendWindow() async {
    if (_extending || _loading || _error != null) return false;
    if (!_windowEnd.isBefore(_maxWindowEnd)) return false;
    _extending = true;
    try {
      final oldEnd = _windowEnd;
      var newEnd = oldEnd.add(_extendStep);
      if (newEnd.isAfter(_maxWindowEnd)) newEnd = _maxWindowEnd;
      final results = [for (final c in widget.channels) _readEpg(c, oldEnd, newEnd)];
      if (!mounted) return false;
      setState(() {
        for (var i = 0; i < _rawByChannel.length && i < results.length; i++) {
          final known = _rawByChannel[i].map((e) => e.id).toSet();
          _rawByChannel[i].addAll(results[i].where((e) => !known.contains(e.id)));
        }
        // Rebuilt from everything fetched: a slice can add the filler that
        // belongs to a program from the previous one.
        _rebuildCells();
        _windowEnd = newEnd;
      });
      return true;
    } catch (_) {
      // Transient failure: the next move toward the edge simply retries.
      return false;
    } finally {
      _extending = false;
    }
  }

  void _maybeExtendAhead() {
    final visibleRight = _windowStart.add(
      Duration(milliseconds: ((_hOffsetMinutes + _visibleSpan.inMinutes) * 60000).round()),
    );
    if (_windowEnd.difference(visibleRight) < _extendMargin) unawaited(_extendWindow());
  }

  GuideCell? get _focusedEntry {
    if (_entriesByChannel.isEmpty) return null;
    final rowEntries = _entriesByChannel[_focusedRow];
    if (_focusedCol < 0 || _focusedCol >= rowEntries.length) return null;
    return rowEntries[_focusedCol];
  }

  // Scrolls the time axis (minimum movement) so the focused program is
  // visible, and records the anchor. Call inside setState.
  void _followFocusHorizontally() {
    final entry = _focusedEntry;
    if (entry == null) return;
    final span = _visibleSpan.inMinutes.toDouble();
    final startMin = _minutesFrom(entry.displayStart).clamp(0.0, double.infinity);
    final endMin = _minutesFrom(entry.displayEnd);
    var h = _hOffsetMinutes;
    if (startMin < h) {
      h = startMin;
    } else if (endMin > h + span) {
      h = endMin - span < startMin ? endMin - span : startMin;
    }
    final maxOffset = (_windowMinutes - span).clamp(0.0, double.infinity);
    _hOffsetMinutes = h.clamp(0.0, maxOffset);
    final visibleLeft = _windowStart.add(Duration(milliseconds: (_hOffsetMinutes * 60000).round()));
    _anchor = entry.displayStart.isAfter(visibleLeft) ? entry.displayStart : visibleLeft;
  }

  void _ensureRowVisible({bool animate = true}) {
    if (!_verticalController.hasClients || _rowsViewportHeight <= 0) return;
    final top = _focusedRow * _u(_rowHeight);
    final bottom = top + _u(_rowHeight);
    final offset = _verticalController.offset;
    double? target;
    if (top < offset) {
      target = top;
    } else if (bottom > offset + _rowsViewportHeight) {
      target = bottom - _rowsViewportHeight;
    }
    if (target == null) return;
    target = target.clamp(0.0, _verticalController.position.maxScrollExtent);
    if (animate) {
      unawaited(_verticalController.animateTo(target, duration: _scrollAnimation, curve: Curves.easeOut));
    } else {
      _verticalController.jumpTo(target);
    }
  }

  // Called by the parent on KeyDownEvent of raw arrowDown (delta 1) /
  // arrowUp (delta -1) — ROW (channel) navigation, without wrapping around
  // the edges.
  void moveRow(int delta) {
    if (_loading || _error != null || _entriesByChannel.isEmpty) return;
    final newRow = _focusedRow + delta;
    if (newRow < 0 || newRow >= _entriesByChannel.length) return;
    final now = widget.clock().toUtc();
    final focused = _focusedEntry;
    final target = guideVerticalTarget(
      _entriesByChannel[newRow],
      anchor: _anchor,
      now: now,
      fromLive: focused != null && _isLive(focused, now),
    );
    setState(() {
      _focusedRow = newRow;
      _focusedCol = target.index;
      _detailsExpanded = false;
      _followFocusHorizontally();
      // Following the live column: keep tracking it on the next vertical move.
      if (target.landedLive) _anchor = now;
    });
    _ensureRowVisible();
    _maybeExtendAhead();
  }

  // Called by the parent on KeyDownEvent of RAW arrowLeft/arrowRight — COLUMN
  // (time) navigation within the focused row, without wrapping around the
  // left edge. Past the last loaded program it fetches more of GET /epg.
  void moveColumn(int delta) {
    if (_loading || _error != null || _entriesByChannel.isEmpty) return;
    final rowEntries = _entriesByChannel[_focusedRow];
    if (rowEntries.isEmpty) {
      if (delta > 0) _extendThenRetry(delta, _focusedRow, 0);
      return;
    }
    final newCol = _focusedCol + delta;
    if (newCol < 0) return;
    if (newCol >= rowEntries.length) {
      if (delta > 0) _extendThenRetry(delta, _focusedRow, rowEntries.length);
      return;
    }
    setState(() {
      _focusedCol = newCol;
      _detailsExpanded = false;
      _followFocusHorizontally();
    });
    _maybeExtendAhead();
  }

  void _extendThenRetry(int delta, int row, int previousLength) {
    unawaited(
      _extendWindow().then((grew) {
        if (!grew || !mounted || row != _focusedRow) return;
        if (_entriesByChannel[row].length > previousLength) moveColumn(delta);
      }),
    );
  }

  // Called by the parent when `select` (Enter) is pressed or released WHILE
  // the EPG is open. KeyDownEvent arms a 500ms Timer (long-press — expands the
  // details panel, without switching channel); if the KeyUpEvent arrives
  // before the Timer fires, it's a short tap (activates: switches channel if
  // the focused program is live right now).
  void handleSelectKeyEvent(KeyEvent event) {
    if (_loading || _error != null) return;
    if (event is KeyDownEvent) {
      _longPressTimer?.cancel();
      _longPressFired = false;
      _longPressTimer = Timer(_longPressThreshold, () {
        _longPressFired = true;
        if (mounted) setState(() => _detailsExpanded = true);
      });
      return;
    }
    if (event is KeyUpEvent) {
      final firedAlready = _longPressFired;
      _longPressTimer?.cancel();
      _longPressTimer = null;
      _longPressFired = false;
      if (!firedAlready) {
        _activateFocused();
      }
      return;
    }
  }

  void _activateFocused() {
    final entry = _focusedEntry;
    if (entry == null) return;
    if (!_isLive(entry, widget.clock().toUtc())) return;
    widget.onSelectLiveChannel(widget.channels[_focusedRow].id);
  }

  Widget _darkBand({Widget? child}) => Container(color: Colors.black.withValues(alpha: 0.9), child: child);

  Widget _buildCell(int rowIndex, int col, GuideCell cell, double hMin, double ppm) {
    final entry = cell.entry;
    final channelObj = widget.channels[rowIndex];
    final startMin = _minutesFrom(cell.displayStart).clamp(0.0, double.infinity);
    final endMin = _minutesFrom(cell.displayEnd);
    final margin = _u(_cellMarginH);
    final left = (startMin - hMin) * ppm + margin;
    final width = ((endMin - startMin) * ppm - 2 * margin).clamp(4.0, double.infinity);
    final focused = rowIndex == _focusedRow && col == _focusedCol;
    final title = entry.displayTitle;
    final nowUtc = _now.toUtc();
    final live = _isLive(cell, nowUtc);
    final progress = live
        ? nowUtc.difference(cell.displayStart).inMilliseconds /
              cell.displayEnd.difference(cell.displayStart).inMilliseconds
        : 0.0;

    return Positioned(
      left: left,
      top: _u(4),
      width: width,
      height: _u(_rowHeight) - _u(8),
      child: Container(
        key: Key('epg-cell-${channelObj.id}-${entry.id}'),
        decoration: BoxDecoration(
          color: Colors.grey.shade900,
          border: Border.all(color: focused ? Colors.white : Colors.white24, width: focused ? _u(3) : 1),
          borderRadius: BorderRadius.circular(_u(4)),
        ),
        clipBehavior: Clip.antiAlias,
        child: Stack(
          children: [
            if (live)
              Positioned.fill(
                child: Align(
                  alignment: Alignment.centerLeft,
                  child: FractionallySizedBox(
                    widthFactor: progress.clamp(0.0, 1.0),
                    child: Container(key: Key('epg-cell-progress-${entry.id}'), color: Colors.white12),
                  ),
                ),
              ),
            Padding(
              padding: EdgeInsets.symmetric(horizontal: _u(8), vertical: _u(4)),
              child: Align(
                alignment: Alignment.centerLeft,
                child: Text(
                  title,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: TextStyle(color: Colors.white, fontSize: _u(16)),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildChannelRow(int rowIndex, double hMin, double ppm) {
    final channelObj = widget.channels[rowIndex];
    final entries = _entriesByChannel[rowIndex];
    final focusedRow = rowIndex == _focusedRow;
    final playing = channelObj.id == widget.currentChannelId;
    final cells = <Widget>[];
    for (var col = 0; col < entries.length; col++) {
      final e = entries[col];
      if (!e.displayEnd.isAfter(_windowStart)) continue;
      cells.add(_buildCell(rowIndex, col, e, hMin, ppm));
    }
    return Container(
      key: Key('epg-row-${channelObj.id}'),
      height: _u(_rowHeight),
      color: focusedRow ? Colors.white.withValues(alpha: 0.08) : null,
      child: Row(
        children: [
          SizedBox(
            width: _u(_channelColumnWidth),
            child: Padding(
              padding: EdgeInsets.only(right: _u(8)),
              child: Row(
                children: [
                  SizedBox(
                    width: _u(32),
                    child: playing
                        ? Icon(
                            AppIcons.play,
                            key: Key('epg-playing-${channelObj.id}'),
                            color: Colors.white,
                            size: _u(24),
                          )
                        : null,
                  ),
                  Expanded(
                    child: Text(
                      '${channelObj.number} ${channelObj.name}',
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(
                        color: focusedRow || playing ? Colors.white : Colors.white70,
                        fontSize: _u(18),
                        fontWeight: FontWeight.bold,
                      ),
                    ),
                  ),
                ],
              ),
            ),
          ),
          Expanded(
            child: ClipRect(child: Stack(children: cells)),
          ),
        ],
      ),
    );
  }

  static const _weekdays = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
  static const _months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

  String _fmtHHmm(DateTime dt) {
    final local = dt.toLocal();
    return '${local.hour.toString().padLeft(2, '0')}:${local.minute.toString().padLeft(2, '0')}';
  }

  String _fmtDate(DateTime dt) {
    final local = dt.toLocal();
    return '${_weekdays[local.weekday - 1]}, ${_months[local.month - 1]} ${local.day}';
  }

  Widget _buildTimeHeader(double hMin, double ppm) {
    final span = _visibleSpan.inMinutes;
    final visibleLeft = _windowStart.add(Duration(milliseconds: (hMin * 60000).round()));
    final tickStep = _tickInterval.inMinutes;
    final firstTick = (hMin / tickStep).floor();
    final lastTick = ((hMin + span) / tickStep).ceil();
    final ticks = <Widget>[];
    for (var i = firstTick; i <= lastTick; i++) {
      final tickTime = _windowStart.add(Duration(minutes: i * tickStep));
      final x = (i * tickStep - hMin) * ppm;
      ticks.add(
        Positioned(
          left: x,
          top: 0,
          bottom: 0,
          child: Container(
            key: Key('epg-tick-${tickTime.millisecondsSinceEpoch}'),
            padding: EdgeInsets.only(left: _u(6)),
            decoration: const BoxDecoration(
              border: Border(left: BorderSide(color: Colors.white24)),
            ),
            alignment: Alignment.centerLeft,
            child: Text(
              _fmtHHmm(tickTime),
              style: TextStyle(color: Colors.white70, fontSize: _u(16)),
            ),
          ),
        ),
      );
    }
    return SizedBox(
      key: const Key('epg-time-header'),
      height: _u(_headerHeight),
      child: Row(
        children: [
          SizedBox(
            width: _u(_channelColumnWidth),
            child: Text(
              key: const Key('epg-date-label'),
              '${_fmtDate(visibleLeft)}  ${_fmtHHmm(_now)}',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(color: Colors.white, fontSize: _u(16), fontWeight: FontWeight.bold),
            ),
          ),
          Expanded(
            child: ClipRect(child: Stack(children: ticks)),
          ),
        ],
      ),
    );
  }

  String _minutesLabel(Duration d) => '${(d.inSeconds / 60).ceil()} min';

  Widget _buildDetailsPanel() {
    final cell = _focusedEntry;
    if (cell == null) return const SizedBox.shrink();
    final entry = cell.entry;
    final title = entry.displayTitle;
    final channelName = widget.channels[_focusedRow].name;
    // The cell's displayed span (the program plus the filler attributed to
    // it), the same one the grid draws.
    final timeRange = '${_fmtHHmm(cell.displayStart)}–${_fmtHHmm(cell.displayEnd)}';
    final nowUtc = _now.toUtc();
    final total = cell.displayEnd.difference(cell.displayStart);
    final live = _isLive(cell, nowUtc);
    final ended = !nowUtc.isBefore(cell.displayEnd);
    final String durationText;
    if (live) {
      durationText = '${_minutesLabel(cell.displayEnd.difference(nowUtc))} left of ${_minutesLabel(total)}';
    } else if (ended) {
      durationText = 'Ended · ${_minutesLabel(total)}';
    } else {
      durationText = _minutesLabel(total);
    }
    final progress = live ? nowUtc.difference(cell.displayStart).inMilliseconds / total.inMilliseconds : 0.0;

    final expanded = _detailsExpanded;
    final art = entry.primaryArt;
    final details = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        Text(
          title,
          style: TextStyle(color: Colors.white, fontSize: _u(expanded ? 38 : 26), fontWeight: FontWeight.bold),
        ),
        SizedBox(height: _u(expanded ? 12 : 6)),
        Text(
          expanded ? channelName : '$channelName · $timeRange · $durationText',
          style: TextStyle(color: Colors.white70, fontSize: _u(expanded ? 26 : 19)),
        ),
        if (expanded) ...[
          SizedBox(height: _u(8)),
          Text(
            timeRange,
            style: TextStyle(color: Colors.white54, fontSize: _u(24)),
          ),
          SizedBox(height: _u(8)),
          Text(
            durationText,
            style: TextStyle(color: Colors.white54, fontSize: _u(24)),
          ),
        ],
      ],
    );
    return Container(
      key: Key(expanded ? 'epg-details-panel-expanded' : 'epg-details-panel'),
      width: double.infinity,
      padding: expanded ? EdgeInsets.all(_u(32)) : EdgeInsets.symmetric(horizontal: _u(24), vertical: _u(16)),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
          // The poster (built from the art reference the backend sent, never
          // fetched through it) only shows in the expanded panel, so the
          // compact one keeps its height.
          if (expanded && art != null)
            Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Padding(
                  padding: EdgeInsets.only(right: _u(28)),
                  child: SizedBox(
                    width: _u(160),
                    height: _u(240),
                    child: ArtImage(
                      jellyfinImageUrl(art.itemId, art.tag),
                      imageKey: Key('epg-details-art-${entry.id}'),
                      fit: BoxFit.cover,
                      fallback: (context) => const ColoredBox(color: Colors.white12),
                    ),
                  ),
                ),
                Expanded(child: details),
              ],
            )
          else
            details,
          // The slot is always reserved so the panel keeps its height; only
          // the bar itself comes and goes.
          SizedBox(height: _u(expanded ? 16 : 10)),
          SizedBox(
            height: _u(6),
            child: live
                ? LinearProgressIndicator(
                    key: const Key('epg-details-progress'),
                    value: progress.clamp(0.0, 1.0),
                    minHeight: _u(6),
                    color: Colors.white,
                    backgroundColor: Colors.white24,
                  )
                : null,
          ),
        ],
      ),
    );
  }

  Widget _buildGrid() {
    return Padding(
      padding: EdgeInsets.symmetric(horizontal: _u(16)),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final channelColumn = _u(_channelColumnWidth);
          final viewportWidth = (constraints.maxWidth - channelColumn).clamp(1.0, double.infinity);
          final ppm = viewportWidth / _visibleSpan.inMinutes;
          return TweenAnimationBuilder<double>(
            tween: Tween<double>(end: _hOffsetMinutes),
            duration: _scrollAnimation,
            curve: Curves.easeOut,
            builder: (context, hMin, _) {
              final nowMin = _minutesFrom(_now.toUtc());
              final nowX = channelColumn + (nowMin - hMin) * ppm;
              final nowVisible = nowX >= channelColumn && nowX <= channelColumn + viewportWidth;
              return Column(
                children: [
                  _buildTimeHeader(hMin, ppm),
                  const Divider(color: Colors.white24, height: 1),
                  Expanded(
                    child: LayoutBuilder(
                      builder: (context, rowsConstraints) {
                        _rowsViewportHeight = rowsConstraints.maxHeight;
                        return Stack(
                          children: [
                            ListView.builder(
                              key: const Key('epg-rows'),
                              controller: _verticalController,
                              itemExtent: _u(_rowHeight),
                              itemCount: widget.channels.length,
                              itemBuilder: (context, rowIndex) => _buildChannelRow(rowIndex, hMin, ppm),
                            ),
                            if (nowVisible)
                              Positioned(
                                left: nowX - _u(1),
                                top: 0,
                                bottom: 0,
                                width: _u(2),
                                child: IgnorePointer(
                                  child: Container(key: const Key('epg-now-marker'), color: Colors.redAccent),
                                ),
                              ),
                          ],
                        );
                      },
                    ),
                  ),
                ],
              );
            },
          );
        },
      ),
    );
  }

  Widget _buildBody() {
    if (_loading) {
      return const Center(child: CircularProgressIndicator());
    }
    if (_error != null) {
      return Center(
        child: Text(
          _error!,
          style: TextStyle(color: Colors.white, fontSize: _u(18)),
        ),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _buildDetailsPanel(),
        const Divider(color: Colors.white24, height: 1),
        Expanded(child: _buildGrid()),
      ],
    );
  }

  @override
  Widget build(BuildContext context) {
    final rect = widget.videoThumbnailRect;
    return Stack(
      key: const Key('epg-overlay'),
      children: [
        // Band above the thumbnail.
        Positioned(left: 0, right: 0, top: 0, height: rect.top, child: _darkBand()),
        // Band below the thumbnail — covers the rest of the screen downward,
        // this is where the whole grid + details panel lives.
        Positioned(
          left: 0,
          right: 0,
          top: rect.bottom,
          bottom: 0,
          child: _darkBand(child: _buildBody()),
        ),
        // Band to the left of the thumbnail (only its height).
        Positioned(left: 0, top: rect.top, width: rect.left, height: rect.height, child: _darkBand()),
        // Band to the right of the thumbnail (only its height).
        Positioned(left: rect.right, top: rect.top, right: 0, height: rect.height, child: _darkBand()),
      ],
    );
  }
}
