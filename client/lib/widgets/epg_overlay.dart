// Full-screen EPG: schedule grid of all channels, with a live thumbnail of the
// channel on air inside the details panel (the slot this widget reserves in its
// own layout, `previewRect`). This widget never draws on top of that slot: the
// preview is the real video surface, drawn underneath by the player page, and
// the guide only keeps a rectangular hole in its bands. The page reads the same
// rectangle to shrink the video into it, so the slot and the hole cannot drift
// apart (the geometry is measured from the slot, not recomputed from constants).
//
// Background: 4 semi-opaque black rectangular bands around that rectangle (top,
// bottom — where the whole grid/details panel lives —, left and right), instead
// of a ClipPath — simpler and enough since the rectangle is always axis-aligned.
//
// Layout: details panel, then a TV-guide grid — a sticky time header (date at
// its left, a tick every 30 min), one fixed-height row per channel (channel
// column pinned on the left) and a vertical "now" line. The visible time span
// is a `GuideMetrics` value (`_m.visibleSpan`, tv or phone); the pixels-per-minute
// scale is derived from
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

import 'package:flutter/foundation.dart' show ValueListenable;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../epg_guide.dart';
import '../cache/schedule_cache.dart';
import '../main.dart' show jellyfinBackdropUrl;
import '../playhead_api.dart';
import '../services/perf_trace.dart';
import '../ui/form_factor.dart';
import '../ui/guide_metrics.dart';
import '../ui/ui_scale.dart';
import 'art_image.dart';
import 'channel_logo.dart';

// How much time the grid shows at once; pixels per minute follow from the
// available width.
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
// How many rows get their cell titles per frame after the guide opens (see `_titleRows`).
const _titleRowsPerFrame = 2;

// Sizes are pixels at 1080p; `EpgOverlayState._u` multiplies them by the shared
// UiScale (lib/ui/ui_scale.dart). The time axis is not part of this: the 3h
// span is fitted to the available width in pixels-per-minute, as before.

const _cellMarginH = 2.0;

class EpgOverlay extends StatefulWidget {
  final List<PlayheadChannel> channels;
  final ScheduleCache cache;
  final String currentChannelId;

  /// Where the live preview belongs, written by this widget on every layout:
  /// the panel's preview slot, in the player Stack's coordinates. The page owns
  /// the notifier and shrinks the video into the rectangle it reads back. Null
  /// means "no preview" (the guide is then opaque over the whole screen).
  final ValueNotifier<Rect?>? previewRect;
  final void Function(String channelId) onSelectLiveChannel;
  final VoidCallback onClose;

  /// The wall clock; tests substitute a fixed one (the golden tests).
  final DateTime Function() clock;

  const EpgOverlay({
    required Key key,
    required this.channels,
    required this.cache,
    required this.currentChannelId,
    this.previewRect,
    required this.onSelectLiveChannel,
    required this.onClose,
    this.clock = DateTime.now,
  }) : super(key: key);

  @override
  State<EpgOverlay> createState() => EpgOverlayState();
}

class EpgOverlayState extends State<EpgOverlay> with SingleTickerProviderStateMixin {
  // The layout family's design units: the visible span, the channel column and
  // the row height. Resolved once — the form factor is decided at boot and never
  // re-derived (docs/ARCHITECTURE.md "Client form factors").
  late final GuideMetrics _m = GuideMetrics.of(ClientFormFactorState.notifier.value);

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

  // The focus lives in notifiers, never in setState: moving it repaints only the
  // cells and rows whose look changes (docs/ARCHITECTURE.md "Guide performance").
  final ValueNotifier<_Pos> _focus = ValueNotifier<_Pos>((row: 0, col: 0));
  final ValueNotifier<bool> _detailsExpandedNotifier = ValueNotifier<bool>(false);

  // Laying out every cell title in the frame that opens the guide is the slowest frame of the
  // whole interaction on a modest device (docs/ARCHITECTURE.md "Guide performance"). The titles
  // come in over the next frames instead, a couple of rows per frame, nearest the focused row
  // first: the first frame draws the cells' boxes and the rest fills in. Only a title is
  // deferred (a leaf), never a row or a cell, so nothing is rebuilt or moves when it appears.
  // A key press shows them all at once.
  final ValueNotifier<int> _titleRows = ValueNotifier<int>(0);
  int _titleOriginRow = 0;
  bool _titleGrowthScheduled = false;

  void _growTitles() {
    if (_titleGrowthScheduled || !mounted) return;
    if (_titleRows.value >= widget.channels.length) return;
    _titleGrowthScheduled = true;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _titleGrowthScheduled = false;
      if (!mounted) return;
      _titleRows.value += _titleRowsPerFrame;
      _growTitles();
    });
  }

  void _showAllTitles() {
    if (_titleRows.value < widget.channels.length * 2) _titleRows.value = widget.channels.length * 2;
  }
  int get _focusedRow => _focus.value.row;
  int get _focusedCol => _focus.value.col;
  void _setFocus(int row, int col) => _focus.value = (row: row, col: col);
  bool get _detailsExpanded => _detailsExpandedNotifier.value;
  set _detailsExpanded(bool v) => _detailsExpandedNotifier.value = v;

  // Time the focus "sits at": moving to another row picks the program there
  // that covers this instant, so Up/Down feels like moving along the time axis.
  // A vertical move from a live program instead lands on the adjacent row's
  // live program (and the anchor becomes "now"); see guideVerticalTarget.
  late DateTime _anchor;

  // Left edge of the visible span, in minutes from _windowStart.
  // The scroll target; the animation to it is a controller that only repaints the
  // header and moves the rows' transforms.
  late final AnimationController _hController =
      AnimationController(vsync: this, duration: _scrollAnimation, value: 1.0);
  double _hFrom = 0;
  double _hTo = 0;
  double get _hOffsetMinutes => _hTo;
  set _hOffsetMinutes(double v) {
    if (v == _hTo) return;
    _hFrom = _hCurrent;
    _hTo = v;
    unawaited(_hController.forward(from: 0));
  }

  double get _hCurrent => _hFrom + (_hTo - _hFrom) * Curves.easeOut.transform(_hController.value);

  // Jumps to the target without animating (the first layout after a load).
  void _settleHorizontally() {
    _hFrom = _hTo;
    _hController.value = 1.0;
  }

  // Written by the grid's LayoutBuilders (plain fields, no setState needed).
  double _rowsViewportHeight = 0;

  // The clock the grid shows, a notifier so a tick repaints only what reads it (the
  // progress bars, the "now" marker, the header, the details panel).
  late final ValueNotifier<DateTime> _nowNotifier = ValueNotifier<DateTime>(widget.clock());
  DateTime get _now => _nowNotifier.value;
  set _now(DateTime v) => _nowNotifier.value = v;

  // The next instant a cell starts or ends: the first tick past it rebuilds the grid
  // (a cell went live or ended); ticks before it rebuild nothing.
  DateTime? _nextBoundary;

  void _recomputeBoundary() {
    final nowUtc = _now.toUtc();
    DateTime? next;
    void consider(DateTime t) {
      if (t.isAfter(nowUtc) && (next == null || t.isBefore(next!))) next = t;
    }

    for (final row in _entriesByChannel) {
      for (final c in row) {
        consider(c.displayStart);
        consider(c.displayEnd);
      }
    }
    _nextBoundary = next;
  }

  void _onNowTick() {
    if (!mounted) return;
    final next = widget.clock();
    _now = next;
    final boundary = _nextBoundary;
    if (boundary != null && !next.toUtc().isBefore(boundary)) setState(_recomputeBoundary);
  }
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
    PerfTrace.begin('epg_open');
    _now = widget.clock();
    _nowTimer = Timer.periodic(_nowTickInterval, (_) => _onNowTick());
    unawaited(_load());
    _cacheSub = widget.cache.changes.listen((_) => _refreshFromCache());
  }

  @override
  void dispose() {
    _nowTimer?.cancel();
    unawaited(_cacheSub?.cancel());
    _longPressTimer?.cancel();
    _verticalController.dispose();
    _hController.dispose();
    _focus.dispose();
    _detailsExpandedNotifier.dispose();
    _hole.dispose();
    _titleRows.dispose();
    _nowNotifier.dispose();
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
    _recomputeBoundary();
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
        if (_focusedCol >= len) _setFocus(_focusedRow, len == 0 ? 0 : len - 1);
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
        _setFocus(initialRow, initialCol);
        _titleOriginRow = initialRow;
        _anchor = now;
        _hOffsetMinutes = 0;
        _loading = false;
        _now = widget.clock();
        _followFocusHorizontally();
        _settleHorizontally();
      });
      WidgetsBinding.instance.addPostFrameCallback((_) {
        // Opening the guide scrolls the channel on air to the middle of the
        // list, so a viewer lands on what they were watching instead of at the
        // top of fifty channels. Near either end the target is clamped: the list
        // ends where it ends, and centring there would only show empty space.
        _centerRow(animate: false);
        PerfTrace.end();
        _growTitles();
      });
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
      Duration(milliseconds: ((_hOffsetMinutes + _m.visibleSpan.inMinutes) * 60000).round()),
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
    final span = _m.visibleSpan.inMinutes.toDouble();
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

  // Puts the focused (at open, the playing) row in the middle of the viewport,
  // clamped to the list's own ends. Called once per open, not on every move.
  void _centerRow({bool animate = false}) {
    if (!_verticalController.hasClients || _rowsViewportHeight <= 0) return;
    final rowH = _u(_m.rowHeight);
    final target = (_focusedRow * rowH + rowH / 2 - _rowsViewportHeight / 2)
        .clamp(0.0, _verticalController.position.maxScrollExtent);
    final offset = _verticalController.offset;
    if ((target - offset).abs() < 0.5) return;
    if (animate) {
      unawaited(_verticalController.animateTo(target, duration: _scrollAnimation, curve: Curves.easeOut));
    } else {
      _verticalController.jumpTo(target);
    }
  }

  void _ensureRowVisible({bool animate = true}) {
    if (!_verticalController.hasClients || _rowsViewportHeight <= 0) return;
    final top = _focusedRow * _u(_m.rowHeight);
    final bottom = top + _u(_m.rowHeight);
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
    PerfTrace.burst('epg_navigate');
    _showAllTitles();
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
    _setFocus(newRow, target.index);
    _detailsExpanded = false;
    _followFocusHorizontally();
    // Following the live column: keep tracking it on the next vertical move.
    if (target.landedLive) _anchor = now;
    _ensureRowVisible();
    _maybeExtendAhead();
  }

  // Called by the parent on KeyDownEvent of RAW arrowLeft/arrowRight — COLUMN
  // (time) navigation within the focused row, without wrapping around the
  // left edge. Past the last loaded program it fetches more of GET /epg.
  void moveColumn(int delta) {
    if (_loading || _error != null || _entriesByChannel.isEmpty) return;
    PerfTrace.burst('epg_navigate');
    _showAllTitles();
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
    _setFocus(_focusedRow, newCol);
    _detailsExpanded = false;
    _followFocusHorizontally();
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
        if (mounted) _detailsExpanded = true;
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

  // Touch: a tap on a program focuses it and, when its displayed span covers
  // now, tunes to that channel — the outcome Select on the focused cell has. A
  // drag does not fire a tap, so panning the time axis never tunes by accident.
  void _tapCell(int rowIndex, int col, GuideCell cell) {
    if (_loading || _error != null || rowIndex >= widget.channels.length) return;
    PerfTrace.burst('epg_navigate');
    _showAllTitles();
    _setFocus(rowIndex, col);
    _detailsExpanded = false;
    _followFocusHorizontally();
    _ensureRowVisible(animate: false);
    if (_isLive(cell, widget.clock().toUtc())) {
      widget.onSelectLiveChannel(widget.channels[rowIndex].id);
    }
  }

  // Touch: a horizontal drag moves the time axis one pixel per pixel (no
  // animation while the finger is down, or the grid would trail it). Same
  // bounds as the arrows: never left of the window start, never past its end.
  void panTime(double deltaPx, double ppm) {
    if (_loading || _error != null || ppm <= 0) return;
    _showAllTitles();
    final maxOffset = (_windowMinutes - _m.visibleSpan.inMinutes).clamp(0.0, double.infinity);
    final next = (_hTo - deltaPx / ppm).clamp(0.0, maxOffset);
    if (next == _hTo) return;
    _hFrom = next;
    _hTo = next;
    _hController.value = 1.0;
    _maybeExtendAhead();
  }

  void _activateFocused() {
    final entry = _focusedEntry;
    if (entry == null) return;
    if (!_isLive(entry, widget.clock().toUtc())) return;
    widget.onSelectLiveChannel(widget.channels[_focusedRow].id);
  }

  /// [child] with a translucent black wash over it: what "this is not the one you
  /// are looking at" costs in this guide. A wash and not `Opacity` on purpose —
  /// it paints a rectangle instead of forcing a saveLayer for a whole row of
  /// cells, which is the difference the guide can afford on a modest device.
  Widget _dimmed(Widget child, double alpha, {Key? dimKey}) => Stack(
        children: [
          child,
          Positioned.fill(
            child: IgnorePointer(
              child: ColoredBox(key: dimKey, color: Colors.black.withValues(alpha: alpha)),
            ),
          ),
        ],
      );

  // A program cell, positioned in minutes from the window start: it does not
  // depend on the horizontal scroll (the row moves as one transform) nor on the
  // focus (a selector repaints only its own border).
  Widget _buildCell(int rowIndex, int col, GuideCell cell, double ppm) {
    final entry = cell.entry;
    final channelObj = widget.channels[rowIndex];
    final startMin = _minutesFrom(cell.displayStart).clamp(0.0, double.infinity);
    final endMin = _minutesFrom(cell.displayEnd);
    final margin = _u(_cellMarginH);
    final left = startMin * ppm + margin;
    final width = ((endMin - startMin) * ppm - 2 * margin).clamp(4.0, double.infinity);
    final live = _isLive(cell, _now.toUtc());
    // A program that has aired already (the guide is rebuilt whenever a cell
    // starts or ends, see _nextBoundary).
    final ended = !_now.toUtc().isBefore(cell.displayEnd);
    final content = Stack(
      children: [
        if (live)
          Positioned.fill(
            child: ValueListenableBuilder<DateTime>(
              valueListenable: _nowNotifier,
              builder: (context, now, _) {
                final nowUtc = now.toUtc();
                final progress = nowUtc.difference(cell.displayStart).inMilliseconds /
                    cell.displayEnd.difference(cell.displayStart).inMilliseconds;
                return Align(
                  alignment: Alignment.centerLeft,
                  child: FractionallySizedBox(
                    widthFactor: progress.clamp(0.0, 1.0),
                    child: Container(key: Key('epg-cell-progress-${entry.id}'), color: Colors.white12),
                  ),
                );
              },
            ),
          ),
        Padding(
          padding: EdgeInsets.symmetric(horizontal: _u(8), vertical: _u(4)),
          child: Align(
            alignment: Alignment.centerLeft,
            child: ValueListenableBuilder<int>(
              valueListenable: _titleRows,
              builder: (context, reach, _) => (rowIndex - _titleOriginRow).abs() < reach
                  ? Text(
                      entry.displayTitle,
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(color: Colors.white, fontSize: _u(16)),
                    )
                  : const SizedBox.shrink(),
            ),
          ),
        ),
      ],
    );
    return Positioned(
      left: left,
      top: _u(4),
      width: width,
      height: _u(_m.rowHeight) - _u(8),
      child: GestureDetector(
        onTap: () => _tapCell(rowIndex, col, cell),
        child: _FocusSelector<bool>(
        focus: _focus,
        select: (p) => p.row == rowIndex && p.col == col,
        // A program that already aired sits a step back from the ones still to
        // come, so the live column reads first at a glance. Painted as a dim
        // wash rather than `Opacity`, which would cost a saveLayer per cell.
        child: ended ? _dimmed(content, 0.10, dimKey: Key('epg-cell-dim-${entry.id}')) : content,
        builder: (context, focused, child) => Container(
          key: Key('epg-cell-${channelObj.id}-${entry.id}'),
          decoration: BoxDecoration(
            color: Colors.grey.shade900,
            border: Border.all(color: focused ? Colors.white : Colors.white24, width: focused ? _u(3) : 1),
            borderRadius: BorderRadius.circular(_u(4)),
          ),
          // No clip: the content already sits inside the border (the decoration pads it), so
          // only the 4px corners differ, and any clip costs the raster thread on every repaint.
          clipBehavior: Clip.none,
          child: child,
        ),
        ),
      ),
    );
  }

  Widget _buildChannelRow(int rowIndex, double ppm) {
    final channelObj = widget.channels[rowIndex];
    final entries = _entriesByChannel[rowIndex];
    final playing = channelObj.id == widget.currentChannelId;
    final cells = <Widget>[];
    for (var col = 0; col < entries.length; col++) {
      final e = entries[col];
      if (!e.displayEnd.isAfter(_windowStart)) continue;
      cells.add(_buildCell(rowIndex, col, e, ppm));
    }
    // The cells sit on one layer that the scroll only translates: no rebuild and
    // no repaint while the time axis moves.
    final canvas = ClipRect(
      child: AnimatedBuilder(
        animation: _hController,
        builder: (context, child) => Transform.translate(offset: Offset(-_hCurrent * ppm, 0), child: child),
        child: RepaintBoundary(child: Stack(clipBehavior: Clip.none, children: cells)),
      ),
    );
    return _FocusSelector<bool>(
      focus: _focus,
      select: (p) => p.row == rowIndex,
      child: canvas,
      builder: (context, focusedRow, canvas) {
        final row = Container(
          key: Key('epg-row-${channelObj.id}'),
          height: _u(_m.rowHeight),
          color: focusedRow ? Colors.white.withValues(alpha: 0.08) : null,
          child: Row(
            children: [
              SizedBox(
                width: _u(_m.channelColumnWidth),
                child: _channelCell(channelObj, focused: focusedRow, playing: playing),
              ),
              Expanded(child: canvas!),
            ],
          ),
        );
        // Every row but the one whose details are on screen sits a step back
        // (20%), channel column and programs alike, so the focused channel reads
        // as the subject of the panel above instead of one row among many.
        return focusedRow ? row : _dimmed(row, 0.20, dimKey: Key('epg-row-dim-${channelObj.id}'));
      },
    );
  }

  /// One channel's cell in the pinned column: the logo on the left, the number
  /// small above the name on the right, and a tint plus an accent edge when it
  /// is the channel on air. The layout is the guide's own (see
  /// docs/ARCHITECTURE.md "Guide display"): the logo is what identifies a
  /// channel at a glance, so it leads, and the number stays visible above the
  /// name for a viewer who navigates by number.
  Widget _channelCell(PlayheadChannel channel, {required bool focused, required bool playing}) {
    final tint = playing
        ? Colors.white.withValues(alpha: 0.10)
        : focused
            ? Colors.white.withValues(alpha: 0.06)
            : Colors.transparent;
    return Container(
      // Keyed only on the channel on air: a test (and a reader of the tree)
      // asks "which row is playing?", and an absent key is the answer "not this
      // one".
      key: playing ? Key('epg-playing-${channel.id}') : null,
      decoration: BoxDecoration(
        color: tint,
        border: Border(
          left: BorderSide(color: playing ? _accent : Colors.transparent, width: _u(4)),
        ),
      ),
      padding: EdgeInsets.only(left: _u(8), right: _u(8)),
      alignment: Alignment.centerLeft,
      child: _channelLabel(channel, highlighted: focused || playing),
    );
  }

  static const _accent = Color(0xFF4FC3F7);

  /// The channel's identity: `[logo] number/name`, or the number alone when the
  /// family's column has no room for a name (a phone). The number never moves
  /// and the logo never replaces it, so the guide stays navigable by number and
  /// a row keeps reading as the same channel with or without an upload.
  Widget _channelLabel(PlayheadChannel channel, {required bool highlighted}) {
    final numberStyle = TextStyle(
      color: highlighted ? Colors.white : Colors.white38,
      fontSize: _u(_m.channelName ? 13 : 16),
      fontWeight: FontWeight.w700,
      letterSpacing: 0.5,
      fontFeatures: const [FontFeature.tabularFigures()],
    );
    final style = TextStyle(
      color: highlighted ? Colors.white : Colors.white70,
      fontSize: _u(15),
      fontWeight: FontWeight.bold,
    );
    final number = Text(
      '${channel.number}',
      key: Key('epg-channel-number-${channel.id}'),
      maxLines: 1,
      style: numberStyle,
    );
    final logoUrl = channelLogoUrl(channel);
    if (!_m.channelName) {
      // A phone's column is 72 px: the number identifies the row and the logo
      // stands where the name would be. A clipped two-letter name reads worse
      // than no name at all.
      return Row(
        key: Key('epg-channel-name-${channel.id}'),
        children: [
          number,
          if (logoUrl != null) ...[
            SizedBox(width: _u(6)),
            Flexible(
              child: LogoBox(
                maxHeight: _u(28),
                maxWidth: _u(_m.channelColumnWidth) - _u(24),
                child: ChannelLogo(
                  logoUrl,
                  height: _u(28),
                  imageKey: Key('epg-channel-logo-${channel.id}'),
                  fallback: (_) => const SizedBox.shrink(),
                ),
              ),
            ),
          ],
        ],
      );
    }
    final name = Text(channel.name, maxLines: 2, overflow: TextOverflow.ellipsis, style: style);
    return Row(
      key: Key('epg-channel-name-${channel.id}'),
      children: [
        if (logoUrl != null) ...[
          // A fixed slot, not a box that shrinks to the image: a square logo and
          // a wide one then take the same room, so the number and the name start
          // at the same x on every row and the column reads as a column.
          SizedBox(
            width: _u(96),
            height: _u(48),
            child: Center(
              child: ChannelLogo(
                logoUrl,
                height: _u(44),
                imageKey: Key('epg-channel-logo-${channel.id}'),
                fallback: (_) => const SizedBox.shrink(),
              ),
            ),
          ),
          SizedBox(width: _u(10)),
        ],
        Expanded(
          child: Column(
            mainAxisAlignment: MainAxisAlignment.center,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [number, name],
          ),
        ),
      ],
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
    final span = _m.visibleSpan.inMinutes;
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
      height: _u(_m.headerHeight),
      child: Row(
        children: [
          SizedBox(
            width: _u(_m.channelColumnWidth),
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

  /// "1 hr 52 min", "45 min", "2 hr": a length a viewer can read, not a count of
  /// seconds.
  static String humanDuration(Duration d) {
    final h = d.inHours;
    final m = d.inMinutes.remainder(60);
    if (h > 0 && m > 0) return '$h hr $m min';
    if (h > 0) return '$h hr';
    final secs = d.inSeconds.remainder(60);
    if (m == 0 && h == 0 && secs > 0) return '$secs s';
    return '$m min';
  }

  /// How the file's audio is described: the codec's short name next to its
  /// channel layout ("AAC Stereo", "AC3 5.1"), which is what a viewer compares
  /// between sources. Null when the source did not say.
  static String? audioLabel(String? codec, int? channels) {
    if (codec == null && channels == null) return null;
    final name = switch ((codec ?? '').toLowerCase()) {
      'aac' => 'AAC',
      'ac3' => 'AC3',
      'eac3' => 'EAC3',
      'dts' => 'DTS',
      'truehd' => 'TrueHD',
      'mp3' => 'MP3',
      'mp2' => 'MP2',
      'flac' => 'FLAC',
      'opus' => 'Opus',
      'wmav2' => 'WMA',
      final other => other.isEmpty ? null : other.toUpperCase(),
    };
    final layout = switch (channels) {
      1 => 'Mono',
      2 => 'Stereo',
      6 => '5.1',
      8 => '7.1',
      final n when n != null && n > 0 => '$n ch',
      _ => null,
    };
    return [name, layout].whereType<String>().join(' ').trim().isEmpty
        ? null
        : [name, layout].whereType<String>().join(' ');
  }

  /// The picture's shape in a viewer's terms ("HEVC 720p", "1080p"), or null
  /// when the source did not say.
  static String? videoLabel(String? codec, int? height) {
    final name = switch ((codec ?? '').toLowerCase()) {
      'h264' || 'avc' => 'H.264',
      'hevc' || 'h265' => 'HEVC',
      'mpeg4' => 'MPEG-4',
      'mpeg2video' => 'MPEG-2',
      'vc1' => 'VC-1',
      'av1' => 'AV1',
      'vp9' => 'VP9',
      final other => other.isEmpty ? null : other.toUpperCase(),
    };
    final res = height == null || height <= 0 ? null : '${height}p';
    final parts = [name, res].whereType<String>();
    return parts.isEmpty ? null : parts.join(' ');
  }

  /// One bordered label in the details panel (`2015`, `PG-13`, `AAC Stereo`):
  /// the reference guides put a program's facts in a row of these.
  Widget _detailChip(String text, {double fontSize = 13, Key? key}) => Container(
        key: key,
        margin: EdgeInsets.only(right: _u(8)),
        padding: EdgeInsets.symmetric(horizontal: _u(8), vertical: _u(3)),
        decoration: BoxDecoration(
          color: Colors.black.withValues(alpha: 0.35),
          border: Border.all(color: Colors.white38),
          borderRadius: BorderRadius.circular(_u(3)),
        ),
        child: Text(text, style: TextStyle(color: Colors.white, fontSize: _u(fontSize), fontWeight: FontWeight.w500)),
      );

  /// The program's own progress, drawn where the dash between its two times
  /// used to be: an empty track for a program that has not started yet, filled
  /// to [progress] while it is on air. Its width is a little more than the dash
  /// it replaces, so it reads as punctuation between the times and not as a
  /// bar of its own.
  Widget _detailTimeBar({
    Key? key,
    required bool live,
    required double progress,
    required double width,
    required double height,
  }) {
    final w = _u(width);
    final h = _u(height);
    return ClipRRect(
      key: key,
      borderRadius: BorderRadius.circular(h),
      child: SizedBox(
        width: w,
        height: h,
        child: Stack(
          children: [
            const Positioned.fill(child: ColoredBox(color: Colors.white24)),
            if (live)
              Positioned(
                // The filled part exists only while the program is on air: the
                // track itself is always there (the times always have something
                // between them), and `epg-details-progress` is the answer to
                // "is this program on air right now?".
                key: const Key('epg-details-progress'),
                left: 0,
                top: 0,
                bottom: 0,
                width: w * progress.clamp(0.0, 1.0),
                child: const ColoredBox(color: Colors.white),
              ),
          ],
        ),
      ),
    );
  }

  /// The slot the live preview occupies: the picture itself is the player's own
  /// video surface, drawn underneath this overlay (never a second decoder, see
  /// the GL-texture note in AGENTS.md), so this box only reserves and measures
  /// the space. `_measurePreview` reports it to the page, which shrinks the
  /// video into exactly this rectangle.
  Widget _previewSlot({required double width, required double height}) => KeyedSubtree(
        key: _previewKey,
        child: Container(
          key: const Key('epg-details-preview'),
          width: _u(width),
          height: _u(height),
          foregroundDecoration: BoxDecoration(
            border: Border.all(color: Colors.white24),
            borderRadius: BorderRadius.circular(_u(6)),
          ),
        ),
      );

  Widget _buildDetailsPanel() {
    final cell = _focusedEntry;
    if (cell == null) return const SizedBox.shrink();
    final entry = cell.entry;
    final channel = widget.channels[_focusedRow];
    final expanded = _detailsExpanded;
    final nowUtc = _now.toUtc();
    final total = cell.displayEnd.difference(cell.displayStart);
    final live = _isLive(cell, nowUtc);
    final ended = !nowUtc.isBefore(cell.displayEnd);
    final String stateText;
    if (live) {
      stateText = '${_minutesLabel(cell.displayEnd.difference(nowUtc))} left of ${humanDuration(total)}';
    } else if (ended) {
      stateText = 'Ended \u00b7 ${humanDuration(total)}';
    } else {
      stateText = humanDuration(total);
    }
    final progress = live ? nowUtc.difference(cell.displayStart).inMilliseconds / total.inMilliseconds : 0.0;

    // The panel's own scale, on top of the app-wide UI scale: a phone draws it
    // smaller and gives the synopsis one line less (see GuideMetrics).
    final ds = _m.detailsScale;
    final titleSize = (expanded ? 36.0 : 24.0) * ds;
    final metaSize = (expanded ? 19.0 : 15.0) * ds;
    final chipSize = (expanded ? 16.0 : 13.0) * ds;
    final synopsisSize = (expanded ? 20.0 : 15.0) * ds;
    final synopsisLines = expanded ? 6 : _m.detailsSynopsisLines;
    final synopsisHeight = _u(synopsisSize * 1.35 * synopsisLines);
    final logoHeight = _u((expanded ? 64 : 40) * ds);

    final chips = <Widget>[
      if (entry.productionYear != null) _detailChip('${entry.productionYear}', fontSize: chipSize, key: const Key('epg-details-year')),
      if (entry.contentRating != null && entry.contentRating!.isNotEmpty)
        _detailChip(entry.contentRating!, fontSize: chipSize, key: const Key('epg-details-rating')),
      if (videoLabel(entry.videoCodec, entry.height) != null)
        _detailChip(videoLabel(entry.videoCodec, entry.height)!, fontSize: chipSize, key: const Key('epg-details-video')),
      if (audioLabel(entry.audioCodec, entry.audioChannels) != null)
        _detailChip(audioLabel(entry.audioCodec, entry.audioChannels)!, fontSize: chipSize, key: const Key('epg-details-audio')),
      _detailChip(humanDuration(total), fontSize: chipSize, key: const Key('epg-details-duration')),
      if (entry.communityRating != null && entry.communityRating! > 0)
        _detailChip('\u2605 ${entry.communityRating!.toStringAsFixed(1)}', fontSize: chipSize, key: const Key('epg-details-score')),
    ];

    // The channel's logo leads the panel: the program's own cover is not shown
    // here (a poster above the title reads as a second title, and the panel
    // already names the program). The slot keeps its height even when the
    // channel has no logo, so moving the focus cannot make the panel jump.
    final channelLogo = channelLogoUrl(channel);
    final logoSlot = SizedBox(
      height: logoHeight,
      child: Align(
        alignment: Alignment.centerLeft,
        child: channelLogo == null
            ? Text(
                channel.name.toUpperCase(),
                key: const Key('epg-details-channel-name'),
                style: TextStyle(color: Colors.white70, fontSize: _u(metaSize), letterSpacing: 1),
              )
            : LogoBox(
                maxHeight: logoHeight,
                child: ChannelLogo(
                  channelLogo,
                  height: logoHeight,
                  imageKey: Key('epg-details-channel-logo-${channel.id}'),
                  fallback: (_) => Text(
                    channel.name.toUpperCase(),
                    key: const Key('epg-details-channel-name'),
                    style: TextStyle(color: Colors.white70, fontSize: _u(metaSize), letterSpacing: 1),
                  ),
                ),
              ),
      ),
    );

    final metaStyle = TextStyle(color: Colors.white70, fontSize: _u(metaSize));
    // The panel's own padding comes from the layout family, so the preview slot
    // (measured from here) and the video the page shrinks into it cannot drift
    // apart; the expanded panel just adds a margin around the same numbers.
    final padH = _m.detailsPaddingH * (expanded ? 1.45 : 1) * ds;
    final padV = _m.detailsPaddingV * (expanded ? 1.45 : 1) * ds;
    // No slot when the preview is off (the page passes no notifier): the guide
    // then reserves nothing, paints no hole and covers the whole screen.
    final previewHeight =
        widget.previewRect != null && _m.detailsPreviewHeight > 0 ? _m.detailsPreviewHeight * ds : 0.0;
    final previewWidth = previewHeight * 16 / 9;

    final content = Padding(
      padding: EdgeInsets.symmetric(horizontal: _u(padH), vertical: _u(padV)),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: ConstrainedBox(
              constraints: BoxConstraints(minHeight: _u(previewHeight)),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                mainAxisSize: MainAxisSize.max,
                children: [
                  logoSlot,
                  SizedBox(height: _u(expanded ? 12 : 8)),
                  Text(
                    entry.displayTitle,
                    key: const Key('epg-details-title'),
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(color: Colors.white, fontSize: _u(titleSize), fontWeight: FontWeight.bold),
                  ),
                  SizedBox(height: _u(expanded ? 10 : 5)),
                  // The times, the program's own progress where the dash between
                  // them used to be (the guide has no room for a full-width bar,
                  // and a bar between the two times is what those times describe),
                  // then who and how long.
                  Row(
                    key: const Key('epg-details-meta-row'),
                    children: [
                      Text(_fmtHHmm(cell.displayStart), style: metaStyle),
                      SizedBox(width: _u(6)),
                      _detailTimeBar(
                        key: const Key('epg-details-time-bar'),
                        live: live,
                        progress: progress,
                        width: metaSize * 0.6,
                        height: metaSize * 0.3 < 3 ? 3.0 : metaSize * 0.3,
                      ),
                      SizedBox(width: _u(6)),
                      Text(_fmtHHmm(cell.displayEnd), style: metaStyle),
                      SizedBox(width: _u(10)),
                      Expanded(
                        child: Text(
                          '\u00b7  ${channel.number} ${channel.name}  \u00b7  $stateText',
                          key: const Key('epg-details-meta'),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: metaStyle,
                        ),
                      ),
                    ],
                  ),
                  if (chips.isNotEmpty) ...[
                    SizedBox(height: _u(expanded ? 12 : 8)),
                    Wrap(spacing: _u(8), runSpacing: _u(6), children: chips),
                  ],
                  SizedBox(height: _u(expanded ? 14 : 9)),
                  // The synopsis box holds its height whether or not this
                  // program has one: the panel must not resize as the focus
                  // moves along the guide.
                  SizedBox(
                    height: synopsisHeight,
                    width: double.infinity,
                    child: Text(
                      (entry.overview == null || entry.overview!.isEmpty)
                          ? (entry.genres ?? '')
                          : entry.overview!,
                      key: const Key('epg-details-overview'),
                      maxLines: synopsisLines,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(color: Colors.white70, fontSize: _u(synopsisSize), height: 1.35),
                    ),
                  ),
                ],
              ),
            ),
          ),
          if (previewHeight > 0) ...[
            SizedBox(width: _u(_m.detailsPreviewGap)),
            _previewSlot(width: previewWidth, height: previewHeight),
          ],
        ],
      ),
    );

    final backdrop = entry.backdropArt;
    return LayoutBuilder(
      builder: (context, constraints) {
        // The preview is a window in the panel's own backdrop: the picture is
        // painted by the player underneath, and the panel simply leaves this
        // rectangle unpainted. Computed from the panel's own layout (never from
        // the measured rectangle), so painting cannot depend on measuring.
        final hole = previewHeight > 0
            ? Rect.fromLTWH(
                constraints.maxWidth - _u(padH) - _u(previewWidth),
                _u(padV),
                _u(previewWidth),
                _u(previewHeight),
              ).inflate(1)
            : null;
        return SizedBox(
          key: Key(expanded ? 'epg-details-panel-expanded' : 'epg-details-panel'),
          width: double.infinity,
          child: Stack(
            children: [
              Positioned.fill(
                child: ClipPath(
                  clipper: HoleClipper(hole),
                  child: Stack(
                    children: [
                      const Positioned.fill(child: ColoredBox(color: Color(0x73000000))),
                      if (backdrop != null)
                        Positioned.fill(
                          child: ArtImage(
                            jellyfinBackdropUrl(backdrop.itemId, tag: backdrop.tag),
                            imageKey: Key('epg-details-art-${entry.id}'),
                            fit: BoxFit.cover,
                            fallback: (_) => const SizedBox.shrink(),
                          ),
                        ),
                      if (backdrop != null)
                        Positioned.fill(
                          child: DecoratedBox(
                            decoration: BoxDecoration(
                              gradient: LinearGradient(
                                begin: Alignment.centerLeft,
                                end: Alignment.centerRight,
                                colors: [
                                  Colors.black.withValues(alpha: 0.93),
                                  Colors.black.withValues(alpha: 0.62),
                                ],
                              ),
                            ),
                          ),
                        ),
                    ],
                  ),
                ),
              ),
              content,
            ],
          ),
        );
      },
    );
  }

  Widget _buildGrid() {
    return Padding(
      padding: EdgeInsets.symmetric(horizontal: _u(16)),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final channelColumn = _u(_m.channelColumnWidth);
          final viewportWidth = (constraints.maxWidth - channelColumn).clamp(1.0, double.infinity);
          final ppm = viewportWidth / _m.visibleSpan.inMinutes;
          return GestureDetector(
            // Translucent: it catches a horizontal drag started on empty row
            // space without stealing the taps the cells handle themselves.
            behavior: HitTestBehavior.translucent,
            onHorizontalDragUpdate: (details) => panTime(details.delta.dx, ppm),
            child: Column(
            children: [
              // Only the header follows the scroll and the clock frame by frame.
              AnimatedBuilder(
                animation: Listenable.merge([_hController, _nowNotifier]),
                builder: (context, _) => _buildTimeHeader(_hCurrent, ppm),
              ),
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
                          itemExtent: _u(_m.rowHeight),
                          itemCount: widget.channels.length,
                          itemBuilder: (context, rowIndex) => _buildChannelRow(rowIndex, ppm),
                        ),
                        Positioned(
                          left: channelColumn,
                          top: 0,
                          bottom: 0,
                          width: viewportWidth,
                          child: IgnorePointer(
                            child: AnimatedBuilder(
                              animation: Listenable.merge([_hController, _nowNotifier]),
                              builder: (context, _) {
                                final nowX = (_minutesFrom(_now.toUtc()) - _hCurrent) * ppm;
                                if (nowX < 0 || nowX > viewportWidth) return const SizedBox.shrink();
                                return Stack(
                                  clipBehavior: Clip.none,
                                  children: [
                                    Positioned(
                                      left: nowX - _u(1),
                                      top: 0,
                                      bottom: 0,
                                      width: _u(2),
                                      child: Container(key: const Key('epg-now-marker'), color: Colors.redAccent),
                                    ),
                                  ],
                                );
                              },
                            ),
                          ),
                        ),
                      ],
                    );
                  },
                ),
              ),
            ],
            ),
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
        ListenableBuilder(
          listenable: Listenable.merge([_focus, _detailsExpandedNotifier, _nowNotifier]),
          builder: (context, _) => _buildDetailsPanel(),
        ),
        const Divider(color: Colors.white24, height: 1),
        Expanded(child: _buildGrid()),
      ],
    );
  }

  // The preview slot's box, measured after every frame. The page shrinks the
  // video into the rectangle this widget reports (through `previewRect`), so the
  // hole in the guide and the picture are one measurement, never two copies of
  // the same arithmetic drifting apart.
  final GlobalKey _previewKey = GlobalKey();

  // The preview's window, as measured from the slot the panel reserved. Used
  // only for painting the hole in the overlay's own backdrop — the panel's
  // layout never reads it, so measuring and painting cannot chase each other.
  final ValueNotifier<Rect?> _hole = ValueNotifier<Rect?>(null);

  void _measurePreview() {
    if (!mounted) return;
    final target = widget.previewRect;
    final slot = _previewKey.currentContext?.findRenderObject();
    final overlay = context.findRenderObject();
    if (slot is! RenderBox || overlay is! RenderBox || !slot.hasSize || !overlay.hasSize) {
      if (_hole.value != null) _hole.value = null;
      if (target != null && target.value != null) target.value = null;
      return;
    }
    final origin = overlay.localToGlobal(Offset.zero);
    final rect = (slot.localToGlobal(Offset.zero) - origin) & slot.size;
    if (_hole.value != rect) _hole.value = rect;
    if (target != null && target.value != rect) target.value = rect;
  }

  @override
  Widget build(BuildContext context) {
    // A cutout or a system bar over the guide's header would eat its first row.
    // `viewPadding` and not `padding`: the player is immersive, so the bars are
    // hidden (`padding` reads zero) while the notch is still there.
    final insets = MediaQuery.viewPaddingOf(context);
    final body = Padding(
      padding: EdgeInsets.only(top: insets.top, left: insets.left, right: insets.right),
      child: _buildBody(),
    );
    // Every frame, because the slot moves with the panel (it grows when the
    // details expand) and nothing else would notice.
    WidgetsBinding.instance.addPostFrameCallback((_) => _measurePreview());
    if (widget.previewRect == null) {
      // No preview asked for: one opaque layer, the grid gets the whole screen.
      return Stack(key: const Key('epg-overlay'), children: [
        Positioned.fill(child: Container(color: Colors.black, child: body)),
      ]);
    }
    return Stack(
      key: const Key('epg-overlay'),
      children: [
        // The darkened backdrop, with the preview's window cut out of it: the
        // player's video is underneath and shows through exactly there. The body
        // is laid out independently of it, so nothing here can move the slot it
        // is measuring.
        Positioned.fill(
          child: ValueListenableBuilder<Rect?>(
            valueListenable: _hole,
            builder: (context, hole, _) => ClipPath(
              clipper: HoleClipper(hole),
              child: const ColoredBox(color: Color(0xE6000000)),
            ),
          ),
        ),
        Positioned.fill(child: body),
      ],
    );
  }
}

/// A rectangle with a window cut out of it (`PathOperation.difference`): how the
/// guide lets the picture through instead of covering it.
class HoleClipper extends CustomClipper<Path> {
  final Rect? hole;
  const HoleClipper(this.hole);

  @override
  Path getClip(Size size) {
    final all = Path()..addRect(Offset.zero & size);
    final cut = hole;
    if (cut == null) return all;
    return Path.combine(
      PathOperation.difference,
      all,
      Path()..addRRect(RRect.fromRectAndRadius(cut, const Radius.circular(6))),
    );
  }

  @override
  bool shouldReclip(HoleClipper oldClipper) => oldClipper.hole != hole;
}


typedef _Pos = ({int row, int col});

/// Rebuilds [builder] only when [select] of the focus changes, so moving the
/// focus costs the two or three widgets whose look changes, not the whole grid.
class _FocusSelector<T> extends StatefulWidget {
  const _FocusSelector({required this.focus, required this.select, required this.builder, this.child});

  final ValueListenable<_Pos> focus;
  final T Function(_Pos) select;
  final Widget Function(BuildContext context, T value, Widget? child) builder;
  final Widget? child;

  @override
  State<_FocusSelector<T>> createState() => _FocusSelectorState<T>();
}

class _FocusSelectorState<T> extends State<_FocusSelector<T>> {
  late T _value = widget.select(widget.focus.value);

  @override
  void initState() {
    super.initState();
    widget.focus.addListener(_onFocus);
  }

  @override
  void didUpdateWidget(_FocusSelector<T> old) {
    super.didUpdateWidget(old);
    if (old.focus != widget.focus) {
      old.focus.removeListener(_onFocus);
      widget.focus.addListener(_onFocus);
    }
    _value = widget.select(widget.focus.value);
  }

  @override
  void dispose() {
    widget.focus.removeListener(_onFocus);
    super.dispose();
  }

  void _onFocus() {
    final next = widget.select(widget.focus.value);
    if (next != _value) setState(() => _value = next);
  }

  @override
  Widget build(BuildContext context) => widget.builder(context, _value, widget.child);
}
