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
// Navigation: row = channel (`moveRow`, called by the parent on
// channelNext/channelPrevious already resolved via KeyBindingsService),
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

const _pxPerMinute = 2.0;
const _longPressThreshold = Duration(milliseconds: 500);

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

  int _focusedRow = 0;
  int _focusedCol = 0;
  bool _detailsExpanded = false;

  Timer? _longPressTimer;
  bool _longPressFired = false;

  // Shared by all rows of the grid. Since navigation is purely programmatic
  // (arrows move the focus, no user dragging), a single ScrollController
  // driving several Scrollables in parallel is enough to keep them aligned.
  final ScrollController _scrollController = ScrollController();

  @override
  void initState() {
    super.initState();
    unawaited(_load());
  }

  @override
  void dispose() {
    _longPressTimer?.cancel();
    _scrollController.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    final now = DateTime.now().toUtc();
    final start = now.subtract(const Duration(minutes: 15));
    final end = now.add(const Duration(hours: 2));
    _windowStart = start;
    _windowEnd = end;

    try {
      final results = await Future.wait(
        widget.channels.map((c) => widget.api.getEpg(c.id, start, end)),
      );
      if (!mounted) return;

      // Dedupe: the same catalog_item can be in more than one channel/row —
      // fetch each one only once.
      final catalogItemIds = <String>{};
      for (final entries in results) {
        for (final entry in entries) {
          catalogItemIds.add(entry.catalogItemId);
        }
      }
      final catalogItems = await Future.wait(
        catalogItemIds.map((id) async {
          final info = await widget.api.getCatalogItem(id);
          return MapEntry(id, info);
        }),
      );
      if (!mounted) return;

      final channelIndex = widget.channels.indexWhere((c) => c.id == widget.currentChannelId);
      final initialRow = channelIndex >= 0 ? channelIndex : 0;
      var initialCol = 0;
      if (initialRow < results.length) {
        final rowEntries = results[initialRow];
        final liveIdx = rowEntries.indexWhere((e) => !now.isBefore(e.startAt) && now.isBefore(e.endAt));
        if (liveIdx >= 0) initialCol = liveIdx;
      }

      setState(() {
        _entriesByChannel = results;
        _catalogItemsById = {for (final e in catalogItems) e.key: e.value};
        _focusedRow = initialRow;
        _focusedCol = initialCol;
        _loading = false;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = 'ERROR: failed to load EPG: $e';
        _loading = false;
      });
    }
  }

  // Called by the parent on KeyDownEvent of `channelNext`/`channelPrevious` —
  // inside the EPG it becomes ROW (channel) navigation, without wrapping
  // around the edges.
  void moveRow(int delta) {
    if (_loading || _error != null || _entriesByChannel.isEmpty) return;
    final newRow = _focusedRow + delta;
    if (newRow < 0 || newRow >= _entriesByChannel.length) return;
    final newRowEntries = _entriesByChannel[newRow];
    final maxCol = newRowEntries.isEmpty ? 0 : newRowEntries.length - 1;
    setState(() {
      _focusedRow = newRow;
      _focusedCol = _focusedCol.clamp(0, maxCol);
      _detailsExpanded = false;
    });
  }

  // Called by the parent on KeyDownEvent of RAW arrowLeft/arrowRight — COLUMN
  // (time) navigation within the focused row, without wrapping around the
  // edges.
  void moveColumn(int delta) {
    if (_loading || _error != null || _entriesByChannel.isEmpty) return;
    final rowEntries = _entriesByChannel[_focusedRow];
    if (rowEntries.isEmpty) return;
    final newCol = _focusedCol + delta;
    if (newCol < 0 || newCol >= rowEntries.length) return;
    setState(() {
      _focusedCol = newCol;
      _detailsExpanded = false;
    });
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
    if (_entriesByChannel.isEmpty) return;
    final rowEntries = _entriesByChannel[_focusedRow];
    if (_focusedCol >= rowEntries.length) return;
    final entry = rowEntries[_focusedCol];
    final now = DateTime.now().toUtc();
    final isLiveNow = !now.isBefore(entry.startAt) && now.isBefore(entry.endAt);
    if (!isLiveNow) return;
    widget.onSelectLiveChannel(widget.channels[_focusedRow].id);
  }

  Widget _darkBand({Widget? child}) => Container(
        color: Colors.black.withValues(alpha: 0.9),
        child: child,
      );

  Widget _buildChannelRow(int rowIndex) {
    final channelObj = widget.channels[rowIndex];
    final entries = _entriesByChannel[rowIndex];
    var cursor = _windowStart;
    final cellWidgets = <Widget>[];

    for (var col = 0; col < entries.length; col++) {
      final entry = entries[col];
      final effectiveStart = entry.startAt.isBefore(_windowStart) ? _windowStart : entry.startAt;
      final gapMinutes = effectiveStart.difference(cursor).inMinutes;
      if (gapMinutes > 0) {
        cellWidgets.add(SizedBox(width: gapMinutes * _pxPerMinute));
      }
      final effectiveEnd = entry.endAt.isAfter(_windowEnd) ? _windowEnd : entry.endAt;
      final widthMinutes = effectiveEnd.difference(effectiveStart).inMinutes;
      final width = (widthMinutes * _pxPerMinute).clamp(48.0, 4000.0);
      final focused = rowIndex == _focusedRow && col == _focusedCol;
      final title = _catalogItemsById[entry.catalogItemId]?.title ?? '';

      cellWidgets.add(
        Container(
          key: Key('epg-cell-${channelObj.id}-${entry.id}'),
          width: width,
          height: 56,
          margin: const EdgeInsets.symmetric(horizontal: 2, vertical: 4),
          padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
          decoration: BoxDecoration(
            color: Colors.grey.shade900,
            border: Border.all(color: focused ? Colors.white : Colors.white24, width: focused ? 3 : 1),
            borderRadius: BorderRadius.circular(4),
          ),
          alignment: Alignment.centerLeft,
          child: Text(
            title,
            maxLines: 2,
            overflow: TextOverflow.ellipsis,
            style: const TextStyle(color: Colors.white, fontSize: 12),
          ),
        ),
      );
      if (entry.endAt.isAfter(cursor)) cursor = entry.endAt;
    }

    return Row(
      crossAxisAlignment: CrossAxisAlignment.center,
      children: [
        SizedBox(
          width: 140,
          child: Text(
            '${channelObj.number} ${channelObj.name}',
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: const TextStyle(color: Colors.white, fontSize: 14, fontWeight: FontWeight.bold),
          ),
        ),
        Expanded(
          child: SingleChildScrollView(
            controller: _scrollController,
            scrollDirection: Axis.horizontal,
            physics: const NeverScrollableScrollPhysics(),
            child: Row(children: cellWidgets),
          ),
        ),
      ],
    );
  }

  String _fmtHHmm(DateTime dt) {
    final local = dt.toLocal();
    return '${local.hour.toString().padLeft(2, '0')}:${local.minute.toString().padLeft(2, '0')}';
  }

  Widget _buildDetailsPanel() {
    if (_entriesByChannel.isEmpty) return const SizedBox.shrink();
    final rowEntries = _entriesByChannel[_focusedRow];
    if (_focusedCol >= rowEntries.length) return const SizedBox.shrink();
    final entry = rowEntries[_focusedCol];
    final title = _catalogItemsById[entry.catalogItemId]?.title ?? '';
    final channelName = widget.channels[_focusedRow].name;
    final timeRange = '${_fmtHHmm(entry.startAt)}–${_fmtHHmm(entry.endAt)}';

    if (_detailsExpanded) {
      return Container(
        key: const Key('epg-details-panel-expanded'),
        width: double.infinity,
        padding: const EdgeInsets.all(24),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(title, style: const TextStyle(color: Colors.white, fontSize: 28, fontWeight: FontWeight.bold)),
            const SizedBox(height: 10),
            Text(channelName, style: const TextStyle(color: Colors.white70, fontSize: 20)),
            const SizedBox(height: 6),
            Text(timeRange, style: const TextStyle(color: Colors.white54, fontSize: 18)),
          ],
        ),
      );
    }
    return Container(
      key: const Key('epg-details-panel'),
      width: double.infinity,
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(title, style: const TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold)),
          const SizedBox(height: 4),
          Text('$channelName · $timeRange', style: const TextStyle(color: Colors.white70, fontSize: 14)),
        ],
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
        Expanded(
          child: ListView.builder(
            padding: const EdgeInsets.symmetric(vertical: 8),
            itemCount: widget.channels.length,
            itemBuilder: (context, rowIndex) => Padding(
              padding: const EdgeInsets.symmetric(vertical: 6, horizontal: 12),
              child: _buildChannelRow(rowIndex),
            ),
          ),
        ),
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
