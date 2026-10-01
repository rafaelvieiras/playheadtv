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

import '../playhead_api.dart';
import 'app_icons.dart';

// How much time the grid shows at once; pixels per minute follow from the
// available width.
const _visibleSpan = Duration(hours: 3);
const _tickInterval = Duration(minutes: 30);
// How much of the past the grid starts with, and how much each lazy fetch
// adds. The materialized schedule only covers a 48h rolling window, so the
// grid never asks beyond that.
const _initialWindowAhead = Duration(hours: 4);
const _extendStep = Duration(hours: 3);
const _horizon = Duration(hours: 48);
const _extendMargin = Duration(hours: 1);
const _nowTickInterval = Duration(seconds: 10);
const _scrollAnimation = Duration(milliseconds: 150);
const _longPressThreshold = Duration(milliseconds: 500);

const _channelColumnWidth = 160.0;
const _rowHeight = 64.0;
const _headerHeight = 32.0;
const _cellMarginH = 2.0;

class EpgOverlay extends StatefulWidget {
  final List<PlayheadChannel> channels;
  final PlayheadApi api;
  final String currentChannelId;
  final Rect videoThumbnailRect; // SAME rectangle as _videoThumbnailRect — never draw anything opaque on top of it
  final void Function(String channelId) onSelectLiveChannel;
  final VoidCallback onClose;

  const EpgOverlay({
    required Key key,
    required this.channels,
    required this.api,
    required this.currentChannelId,
    required this.videoThumbnailRect,
    required this.onSelectLiveChannel,
    required this.onClose,
  }) : super(key: key);

  @override
  State<EpgOverlay> createState() => EpgOverlayState();
}

class EpgOverlayState extends State<EpgOverlay> {
  bool _loading = true;
  String? _error;

  // One list of EpgEntry per channel, in the SAME ORDER as widget.channels —
  // each row index of the grid is the same index into _entriesByChannel.
  List<List<EpgEntry>> _entriesByChannel = [];
  Map<String, CatalogItemInfo> _catalogItemsById = {};
  late DateTime _windowStart;
  late DateTime _windowEnd;
  late DateTime _maxWindowEnd;
  bool _extending = false;

  int _focusedRow = 0;
  int _focusedCol = 0;
  bool _detailsExpanded = false;

  // Time the focus "sits at": moving to another row picks the program there
  // that covers this instant, so Up/Down feels like moving along the time axis.
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

  @override
  void initState() {
    super.initState();
    _now = DateTime.now();
    _nowTimer = Timer.periodic(_nowTickInterval, (_) {
      if (mounted) setState(() => _now = DateTime.now());
    });
    unawaited(_load());
  }

  @override
  void dispose() {
    _nowTimer?.cancel();
    _longPressTimer?.cancel();
    _verticalController.dispose();
    super.dispose();
  }

  // Start of the 30-minute slot (in local time) containing `t`, as UTC.
  DateTime _floorToTick(DateTime t) {
    final local = t.toLocal();
    final floored = DateTime(local.year, local.month, local.day, local.hour, local.minute - local.minute % _tickInterval.inMinutes);
    return floored.toUtc();
  }

  double _minutesFrom(DateTime t) => t.difference(_windowStart).inMilliseconds / 60000.0;

  double get _windowMinutes => _minutesFrom(_windowEnd);

  bool _isLive(EpgEntry e, DateTime now) => !now.isBefore(e.startAt) && now.isBefore(e.endAt);

  Future<List<EpgEntry>> _fetchEpg(PlayheadChannel c, DateTime start, DateTime end) => widget.api.getEpg(c.id, start, end);

  Future<Map<String, CatalogItemInfo>> _fetchCatalogItems(Iterable<List<EpgEntry>> rows) async {
    // Dedupe: the same catalog_item can be in more than one channel/row, and
    // items already known are not fetched again.
    final ids = <String>{};
    for (final entries in rows) {
      for (final entry in entries) {
        if (!_catalogItemsById.containsKey(entry.catalogItemId)) ids.add(entry.catalogItemId);
      }
    }
    final fetched = await Future.wait(ids.map((id) async => MapEntry(id, await widget.api.getCatalogItem(id))));
    return {for (final e in fetched) e.key: e.value};
  }

  Future<void> _load() async {
    final now = DateTime.now().toUtc();
    final start = _floorToTick(now.subtract(_tickInterval));
    final end = now.add(_initialWindowAhead);
    _windowStart = start;
    _windowEnd = end;
    _maxWindowEnd = now.add(_horizon);

    try {
      final results = await Future.wait(widget.channels.map((c) => _fetchEpg(c, start, end)));
      if (!mounted) return;
      final catalogItems = await _fetchCatalogItems(results);
      if (!mounted) return;

      final channelIndex = widget.channels.indexWhere((c) => c.id == widget.currentChannelId);
      final initialRow = channelIndex >= 0 ? channelIndex : 0;
      var initialCol = 0;
      if (initialRow < results.length) {
        final liveIdx = results[initialRow].indexWhere((e) => _isLive(e, now));
        if (liveIdx >= 0) initialCol = liveIdx;
      }

      setState(() {
        _entriesByChannel = results;
        _catalogItemsById = {..._catalogItemsById, ...catalogItems};
        _focusedRow = initialRow;
        _focusedCol = initialCol;
        _anchor = now;
        _hOffsetMinutes = 0;
        _loading = false;
        _now = DateTime.now();
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
      final results = await Future.wait(widget.channels.map((c) => _fetchEpg(c, oldEnd, newEnd)));
      if (!mounted) return false;
      final catalogItems = await _fetchCatalogItems(results);
      if (!mounted) return false;
      setState(() {
        for (var i = 0; i < _entriesByChannel.length && i < results.length; i++) {
          final known = _entriesByChannel[i].map((e) => e.id).toSet();
          _entriesByChannel[i].addAll(results[i].where((e) => !known.contains(e.id)));
        }
        _catalogItemsById = {..._catalogItemsById, ...catalogItems};
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
    final visibleRight = _windowStart.add(Duration(milliseconds: ((_hOffsetMinutes + _visibleSpan.inMinutes) * 60000).round()));
    if (_windowEnd.difference(visibleRight) < _extendMargin) unawaited(_extendWindow());
  }

  EpgEntry? get _focusedEntry {
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
    final startMin = _minutesFrom(entry.startAt).clamp(0.0, double.infinity);
    final endMin = _minutesFrom(entry.endAt);
    var h = _hOffsetMinutes;
    if (startMin < h) {
      h = startMin;
    } else if (endMin > h + span) {
      h = endMin - span < startMin ? endMin - span : startMin;
    }
    final maxOffset = (_windowMinutes - span).clamp(0.0, double.infinity);
    _hOffsetMinutes = h.clamp(0.0, maxOffset);
    final visibleLeft = _windowStart.add(Duration(milliseconds: (_hOffsetMinutes * 60000).round()));
    _anchor = entry.startAt.isAfter(visibleLeft) ? entry.startAt : visibleLeft;
  }

  void _ensureRowVisible({bool animate = true}) {
    if (!_verticalController.hasClients || _rowsViewportHeight <= 0) return;
    final top = _focusedRow * _rowHeight;
    final bottom = top + _rowHeight;
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

  // Index of the entry in `entries` covering `t`, else the next one starting
  // after it, else the last one.
  int _indexForTime(List<EpgEntry> entries, DateTime t) {
    if (entries.isEmpty) return 0;
    for (var i = 0; i < entries.length; i++) {
      if (!t.isBefore(entries[i].startAt) && t.isBefore(entries[i].endAt)) return i;
    }
    for (var i = 0; i < entries.length; i++) {
      if (!entries[i].startAt.isBefore(t)) return i;
    }
    return entries.length - 1;
  }

  // Called by the parent on KeyDownEvent of raw arrowDown (delta 1) /
  // arrowUp (delta -1) — ROW (channel) navigation, without wrapping around
  // the edges.
  void moveRow(int delta) {
    if (_loading || _error != null || _entriesByChannel.isEmpty) return;
    final newRow = _focusedRow + delta;
    if (newRow < 0 || newRow >= _entriesByChannel.length) return;
    setState(() {
      _focusedRow = newRow;
      _focusedCol = _indexForTime(_entriesByChannel[newRow], _anchor);
      _detailsExpanded = false;
      _followFocusHorizontally();
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
    unawaited(_extendWindow().then((grew) {
      if (!grew || !mounted || row != _focusedRow) return;
      if (_entriesByChannel[row].length > previousLength) moveColumn(delta);
    }));
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
    if (!_isLive(entry, DateTime.now().toUtc())) return;
    widget.onSelectLiveChannel(widget.channels[_focusedRow].id);
  }

  Widget _darkBand({Widget? child}) => Container(
        color: Colors.black.withValues(alpha: 0.9),
        child: child,
      );

  Widget _buildCell(int rowIndex, int col, EpgEntry entry, double hMin, double ppm) {
    final channelObj = widget.channels[rowIndex];
    final startMin = _minutesFrom(entry.startAt).clamp(0.0, double.infinity);
    final endMin = _minutesFrom(entry.endAt);
    final left = (startMin - hMin) * ppm + _cellMarginH;
    final width = ((endMin - startMin) * ppm - 2 * _cellMarginH).clamp(4.0, double.infinity);
    final focused = rowIndex == _focusedRow && col == _focusedCol;
    final title = _catalogItemsById[entry.catalogItemId]?.title ?? '';
    final nowUtc = _now.toUtc();
    final live = _isLive(entry, nowUtc);
    final progress = live ? nowUtc.difference(entry.startAt).inMilliseconds / entry.endAt.difference(entry.startAt).inMilliseconds : 0.0;

    return Positioned(
      left: left,
      top: 4,
      width: width,
      height: _rowHeight - 8,
      child: Container(
        key: Key('epg-cell-${channelObj.id}-${entry.id}'),
        decoration: BoxDecoration(
          color: Colors.grey.shade900,
          border: Border.all(color: focused ? Colors.white : Colors.white24, width: focused ? 3 : 1),
          borderRadius: BorderRadius.circular(4),
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
              padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
              child: Align(
                alignment: Alignment.centerLeft,
                child: Text(
                  title,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: const TextStyle(color: Colors.white, fontSize: 12),
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
      if (!e.endAt.isAfter(_windowStart)) continue;
      cells.add(_buildCell(rowIndex, col, e, hMin, ppm));
    }
    return Container(
      key: Key('epg-row-${channelObj.id}'),
      height: _rowHeight,
      color: focusedRow ? Colors.white.withValues(alpha: 0.08) : null,
      child: Row(
        children: [
          SizedBox(
            width: _channelColumnWidth,
            child: Padding(
              padding: const EdgeInsets.only(right: 8),
              child: Row(
                children: [
                  SizedBox(
                    width: 24,
                    child: playing ? Icon(AppIcons.play, key: Key('epg-playing-${channelObj.id}'), color: Colors.white, size: 18) : null,
                  ),
                  Expanded(
                    child: Text(
                      '${channelObj.number} ${channelObj.name}',
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(
                        color: focusedRow || playing ? Colors.white : Colors.white70,
                        fontSize: 14,
                        fontWeight: FontWeight.bold,
                      ),
                    ),
                  ),
                ],
              ),
            ),
          ),
          Expanded(child: ClipRect(child: Stack(children: cells))),
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
      ticks.add(Positioned(
        left: x,
        top: 0,
        bottom: 0,
        child: Container(
          key: Key('epg-tick-${tickTime.millisecondsSinceEpoch}'),
          padding: const EdgeInsets.only(left: 6),
          decoration: const BoxDecoration(border: Border(left: BorderSide(color: Colors.white24))),
          alignment: Alignment.centerLeft,
          child: Text(_fmtHHmm(tickTime), style: const TextStyle(color: Colors.white70, fontSize: 13)),
        ),
      ));
    }
    return SizedBox(
      key: const Key('epg-time-header'),
      height: _headerHeight,
      child: Row(
        children: [
          SizedBox(
            width: _channelColumnWidth,
            child: Text(
              key: const Key('epg-date-label'),
              '${_fmtDate(visibleLeft)}  ${_fmtHHmm(_now)}',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(color: Colors.white, fontSize: 13, fontWeight: FontWeight.bold),
            ),
          ),
          Expanded(child: ClipRect(child: Stack(children: ticks))),
        ],
      ),
    );
  }

  String _minutesLabel(Duration d) => '${(d.inSeconds / 60).ceil()} min';

  Widget _buildDetailsPanel() {
    final entry = _focusedEntry;
    if (entry == null) return const SizedBox.shrink();
    final title = _catalogItemsById[entry.catalogItemId]?.title ?? '';
    final channelName = widget.channels[_focusedRow].name;
    final timeRange = '${_fmtHHmm(entry.startAt)}–${_fmtHHmm(entry.endAt)}';
    final nowUtc = _now.toUtc();
    final total = entry.endAt.difference(entry.startAt);
    final live = _isLive(entry, nowUtc);
    final ended = !nowUtc.isBefore(entry.endAt);
    final String durationText;
    if (live) {
      durationText = '${_minutesLabel(entry.endAt.difference(nowUtc))} left of ${_minutesLabel(total)}';
    } else if (ended) {
      durationText = 'Ended · ${_minutesLabel(total)}';
    } else {
      durationText = _minutesLabel(total);
    }
    final progress = live ? nowUtc.difference(entry.startAt).inMilliseconds / total.inMilliseconds : 0.0;

    final expanded = _detailsExpanded;
    return Container(
      key: Key(expanded ? 'epg-details-panel-expanded' : 'epg-details-panel'),
      width: double.infinity,
      padding: expanded ? const EdgeInsets.all(24) : const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(title, style: TextStyle(color: Colors.white, fontSize: expanded ? 28 : 18, fontWeight: FontWeight.bold)),
          SizedBox(height: expanded ? 10 : 4),
          Text(
            expanded ? channelName : '$channelName · $timeRange · $durationText',
            style: TextStyle(color: Colors.white70, fontSize: expanded ? 20 : 14),
          ),
          if (expanded) ...[
            const SizedBox(height: 6),
            Text(timeRange, style: const TextStyle(color: Colors.white54, fontSize: 18)),
            const SizedBox(height: 6),
            Text(durationText, style: const TextStyle(color: Colors.white54, fontSize: 18)),
          ],
          // The slot is always reserved so the panel keeps its height; only
          // the bar itself comes and goes.
          SizedBox(height: expanded ? 12 : 8),
          SizedBox(
            height: 4,
            child: live
                ? LinearProgressIndicator(
                    key: const Key('epg-details-progress'),
                    value: progress.clamp(0.0, 1.0),
                    minHeight: 4,
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
      padding: const EdgeInsets.symmetric(horizontal: 12),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final viewportWidth = (constraints.maxWidth - _channelColumnWidth).clamp(1.0, double.infinity);
          final ppm = viewportWidth / _visibleSpan.inMinutes;
          return TweenAnimationBuilder<double>(
            tween: Tween<double>(end: _hOffsetMinutes),
            duration: _scrollAnimation,
            curve: Curves.easeOut,
            builder: (context, hMin, _) {
              final nowMin = _minutesFrom(_now.toUtc());
              final nowX = _channelColumnWidth + (nowMin - hMin) * ppm;
              final nowVisible = nowX >= _channelColumnWidth && nowX <= _channelColumnWidth + viewportWidth;
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
                              itemExtent: _rowHeight,
                              itemCount: widget.channels.length,
                              itemBuilder: (context, rowIndex) => _buildChannelRow(rowIndex, hMin, ppm),
                            ),
                            if (nowVisible)
                              Positioned(
                                left: nowX - 1,
                                top: 0,
                                bottom: 0,
                                width: 2,
                                child: IgnorePointer(child: Container(key: const Key('epg-now-marker'), color: Colors.redAccent)),
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
        child: Text(_error!, style: const TextStyle(color: Colors.white, fontSize: 14)),
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
        Positioned(left: 0, right: 0, top: rect.bottom, bottom: 0, child: _darkBand(child: _buildBody())),
        // Band to the left of the thumbnail (only its height).
        Positioned(left: 0, top: rect.top, width: rect.left, height: rect.height, child: _darkBand()),
        // Band to the right of the thumbnail (only its height).
        Positioned(left: rect.right, top: rect.top, right: 0, height: rect.height, child: _darkBand()),
      ],
    );
  }
}
