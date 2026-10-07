// Mosaic screen: the home shown when there's no watched-channel history yet
// (see `_InitialRouteResolver` in main.dart), and the destination of the Home
// key from the player. Visual model: an old satellite-TV waiting screen —
// channel logos around the edge, one "live" channel playing in the middle
// (here decorative: its own VideoPlayerController, without the player's
// crossfade/preload engine).
//
// Layout: a FRAME of up to 12 tiles (`_ringSlots`) surrounds the featured
// video, which occupies the central 2x2 block — 4 tiles on top, 4 on the
// bottom (the corners count in those 4), 2 on each side (left/right, not
// counting the corners). Navigation: `arrowUp`/`arrowDown` toggles focus
// between the center (video) and the frame (it always enters the frame at the
// first cell); `arrowLeft`/`arrowRight`, with focus already in the frame, walk
// along the `_ringSlots` sequence (clockwise, without wrapping around the
// edge). Literal (arrowLeft/Right/Up/Down) + Enter, bypassing
// KeyBindingsService/InputAction — a deliberate decision, a 2D grid is a
// different context from the linear player (see AGENTS.md).
//
// Back (`InputAction.back`, Esc by default) is the one remappable key here: it
// opens the "Exit Playhead TV?" confirmation, the only way to leave the app.
// Cancel has the focus; Back or Cancel closes it; Exit tears down (preview
// controller, display wake lock, syncer) and terminates (services/app_exit.dart).
import 'dart:async';
import 'dart:math';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:video_player/video_player.dart';
import 'package:fvp/fvp.dart'; // extension setSubtitleTracks on VideoPlayerController

import '../cache/offline_runtime.dart';
import '../cache/schedule_sync.dart';
import '../main.dart';
import '../playhead_api.dart';
import '../widgets/app_icons.dart';
import '../widgets/art_image.dart';
import '../input/input_action.dart';
import '../services/app_exit.dart';
import '../services/screen_awake.dart';
import '../services/settings_service.dart';
import '../util/gl_serial.dart';
import 'settings_page.dart';

class _MosaicTile {
  final PlayheadChannel? channel;
  final bool isSettings;

  const _MosaicTile.forChannel(this.channel) : isSettings = false;
  const _MosaicTile.forSettings()
      : channel = null,
        isSettings = true;
}

class MosaicPage extends StatefulWidget {
  const MosaicPage({super.key});

  @override
  State<MosaicPage> createState() => _MosaicPageState();
}

class _MosaicPageState extends State<MosaicPage> {
  late OfflineRuntime _runtime;
  late SettingsService _settings;
  KeyBindingsService? _keyBindings;
  bool _exitDialogOpen = false;

  bool _loading = true;
  String? _error;

  List<PlayheadChannel> _allChannels = [];
  PlayheadChannel? _featured;
  List<_MosaicTile> _tiles = [];
  List<FocusNode> _focusNodes = [];
  int _focusedIndex = 0;

  // (row, column) positions of the frame, in a 4x4 logical grid whose central
  // 2x2 block (rows 1-2, columns 1-2) is the featured video. Order: clockwise
  // from the top-left corner — it is also the fill order (`_tiles.skip(1)`)
  // and the arrowLeft/Right navigation order.
  static const List<({int row, int col})> _ringSlots = [
    (row: 0, col: 0), (row: 0, col: 1), (row: 0, col: 2), (row: 0, col: 3), // top
    (row: 1, col: 3), (row: 2, col: 3), // right side
    (row: 3, col: 3), (row: 3, col: 2), (row: 3, col: 1), (row: 3, col: 0), // bottom
    (row: 2, col: 0), (row: 1, col: 0), // left side
  ];

  VideoPlayerController? _previewController;

  final FocusNode _pageFocusNode = FocusNode();

  @override
  void initState() {
    super.initState();
    _bootstrap();
  }

  Future<void> _bootstrap() async {
    try {
      final settings = await SettingsService.create();
      _keyBindings = KeyBindingsService(settings);
      // Channels come from the offline cache, so the home works with the
      // backend down. A first run has nothing cached and waits for the
      // backend; otherwise the startup refresh only gets a moment to bring
      // fresh data.
      _runtime = await OfflineRuntime.ensure();
      await _runtime.syncer.syncNow().timeout(startupSyncWait, onTimeout: () => SyncOutcome.offline);
      var channels = _runtime.cache.channels();
      if (channels.isEmpty) {
        await _runtime.syncer.syncNow();
        channels = _runtime.cache.channels();
      }
      if (!mounted) return;
      if (channels.isEmpty) {
        final status = _runtime.syncer.status.value;
        setState(() {
          _loading = false;
          _error = status.lastSucceeded
              ? 'ERROR: no channels configured on the backend (GET /channels is empty)'
              : 'ERROR: failed to load channels: ${status.lastError}';
        });
        return;
      }
      _settings = settings;
      _allChannels = channels;
      _resolveTiles();
      setState(() => _loading = false);
      unawaited(_startPreview());
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _error = 'ERROR: failed to load channels: $e';
      });
    }
  }

  // Resolves the featured channel (the most recently watched, or the one with
  // the lowest `number` — same fallback `_bootstrapChannels` already uses in
  // the player) and up to 11 surrounding tiles (the whole frame,
  // `_ringSlots.length`): first the rest of the recent history, then random
  // channels not yet shown, never repeating a channel.
  void _resolveTiles() {
    final byId = {for (final c in _allChannels) c.id: c};
    final sortedByNumber = [..._allChannels]..sort((a, b) => a.number.compareTo(b.number));
    final recentIds = _settings.recentChannelIds;

    PlayheadChannel featured;
    Iterable<String> recentRest;
    if (recentIds.isNotEmpty && byId.containsKey(recentIds.first)) {
      featured = byId[recentIds.first]!;
      recentRest = recentIds.skip(1);
    } else {
      featured = sortedByNumber.first;
      recentRest = recentIds;
    }

    // The frame has `_ringSlots.length` cells; one of them is always the fixed
    // Settings tile, so at most `_ringSlots.length - 1` surrounding channels
    // fit.
    final maxSurrounding = _ringSlots.length - 1;
    final shown = <String>{featured.id};
    final surrounding = <PlayheadChannel>[];
    for (final id in recentRest) {
      if (surrounding.length >= maxSurrounding) break;
      final candidate = byId[id];
      if (candidate != null && shown.add(candidate.id)) surrounding.add(candidate);
    }
    if (surrounding.length < maxSurrounding) {
      final remainingPool = _allChannels.where((c) => !shown.contains(c.id)).toList()..shuffle(Random());
      for (final candidate in remainingPool) {
        if (surrounding.length >= maxSurrounding) break;
        if (shown.add(candidate.id)) surrounding.add(candidate);
      }
    }

    _featured = featured;
    _tiles = [
      _MosaicTile.forChannel(featured),
      for (final c in surrounding) _MosaicTile.forChannel(c),
      const _MosaicTile.forSettings(),
    ];
    _focusNodes = List.generate(_tiles.length, (_) => FocusNode());
    _focusedIndex = 0;
  }

  // Decorative preview of the featured tile: a single VideoPlayerController
  // (no preload/crossfade), with audio (default volume), started at the real
  // position of the item that would be "live" right now. Any failure here
  // (network, empty EPG, etc.) just leaves the tile on its placeholder —
  // never blocks the mosaic.
  Future<void> _startPreview() async {
    final featured = _featured;
    if (featured == null) return;
    try {
      final now = DateTime.now().toUtc();
      // From the offline cache: the first program on air or ahead.
      final entries = _runtime.cache.entries(featured.id, now, now.add(const Duration(hours: 1)));
      if (!mounted || entries.isEmpty) return;
      final entry = entries.first;
      // A grouped tail has no source id of its own: preview the clip on air.
      var externalId = entry.externalId;
      var itemStart = entry.startAt;
      var itemEnd = entry.endAt;
      if (entry.isTailGroup) {
        final clip = entry.clips.firstWhere((c) => c.endAt.isAfter(now), orElse: () => entry.clips.first);
        externalId = clip.externalId;
        itemStart = clip.startAt;
        itemEnd = clip.endAt;
      }
      if (!mounted || externalId == null) return;

      final controller = VideoPlayerController.networkUrl(Uri.parse(streamUrl(externalId)));
      await serializeGlOp(() => controller.initialize());
      if (!mounted) {
        unawaited(serializeGlOp(() => controller.dispose()));
        return;
      }

      var seek = now.difference(itemStart);
      final clipDuration = itemEnd.difference(itemStart);
      if (seek < Duration.zero) seek = Duration.zero;
      if (seek > clipDuration) seek = clipDuration;
      if (seek > Duration.zero) await controller.seekTo(seek);

      controller.setSubtitleTracks([]);
      controller.setProperty('avsync.audio', '0');
      await controller.play();

      if (!mounted) {
        unawaited(serializeGlOp(() => controller.dispose()));
        return;
      }
      setState(() => _previewController = controller);
    } catch (_) {
      // decorative — no handling beyond leaving the placeholder in place.
    }
  }

  // Moves within the frame (clockwise/counterclockwise following the order of
  // `_ringSlots`); no effect if the current focus is the center (video).
  void _moveRing(int delta) {
    if (_focusedIndex == 0) return;
    final ringLen = _tiles.length - 1;
    final next = _focusedIndex - 1 + delta;
    if (next < 0 || next >= ringLen) return; // stops at the edge, no wrap-around
    _focusIndex(next + 1);
  }

  // Toggles between the center (video) and the frame: from the center, it
  // always enters at the first cell of the frame; from any cell of the frame,
  // it always goes back to the center — there's no per-cell "above"/"below"
  // position.
  void _toggleCenterRing() {
    if (_focusedIndex == 0) {
      if (_tiles.length > 1) _focusIndex(1);
    } else {
      _focusIndex(0);
    }
  }

  void _focusIndex(int idx) {
    setState(() => _focusedIndex = idx);
    _focusNodes[idx].requestFocus();
  }

  // Async on purpose: entering a channel creates 2 new VideoPlayerControllers
  // (the player's crossfade) — fvp/libmdk's dispose is asynchronous on the
  // native side, not just in the Dart Future, so navigating before it finishes
  // would let the creation of the new GL textures run in parallel with the
  // release of the mosaic preview's texture. Waiting for the dispose to really
  // finish before navigating avoids that overlap as a precaution.
  //
  // This is NOT the cause of the black-screen bug when entering a channel
  // (that was a Stack child without Positioned in main.dart — see the comment
  // on the EPG's ValueListenableBuilder there, isolated by reverse
  // bisection). This await was never confirmed as necessary to reproduce or
  // fix that specific bug.
  Future<void> _activateFocused() async {
    final tile = _tiles[_focusedIndex];
    if (tile.isSettings) {
      Navigator.of(context).push(MaterialPageRoute(builder: (_) => const SettingsPage()));
      return;
    }
    final channelId = tile.channel!.id;
    final preview = _previewController;
    _previewController = null;
    if (preview != null) {
      // Routed through serializeGlOp (see its doc comment): this await
      // alone isn't enough once the channel player's own controllers start
      // being created through the same shared queue — it guarantees this
      // dispose is strictly ordered relative to them, not just relative to
      // this function's own subsequent code.
      await serializeGlOp(() => preview.dispose());
    }
    if (!mounted) return;
    Navigator.of(context)
        .pushReplacement(MaterialPageRoute(builder: (_) => ChannelPlayerPage(initialChannelId: channelId)));
  }

  // "Exit Playhead TV?": Cancel is focused; Back (remappable) or Cancel closes
  // it, Exit returns true. A focus scope of its own, six buttons only.
  Future<bool> _askExit() async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (dialogContext) => Focus(
        onKeyEvent: (node, event) {
          if (event is KeyDownEvent && _keyBindings?.resolve(event.logicalKey) == InputAction.back) {
            Navigator.of(dialogContext).pop(false);
            return KeyEventResult.handled;
          }
          return KeyEventResult.ignored;
        },
        child: AlertDialog(
          key: const Key('exit-dialog'),
          title: const Text('Exit Playhead TV?'),
          actions: [
            TextButton(
              key: const Key('exit-cancel'),
              autofocus: true,
              onPressed: () => Navigator.of(dialogContext).pop(false),
              child: const Text('Cancel'),
            ),
            TextButton(
              key: const Key('exit-confirm'),
              onPressed: () => Navigator.of(dialogContext).pop(true),
              child: const Text('Exit'),
            ),
          ],
        ),
      ),
    );
    return confirmed == true;
  }

  Future<void> _confirmExit() async {
    if (_exitDialogOpen) return;
    _exitDialogOpen = true;
    final confirmed = await _askExit();
    _exitDialogOpen = false;
    if (confirmed && mounted) await _exitApp();
  }

  // Teardown, in order: the preview controller (taken off the tree first, then
  // disposed through the shared GL queue), the display wake lock, the background
  // syncer and cache. The player page is never alive here (Back reaches the
  // mosaic only after the player's own dispose released its controllers).
  Future<void> _exitApp() {
    return AppExitController(steps: [
      () async {
        final preview = _previewController;
        if (preview == null) return;
        if (mounted) setState(() => _previewController = null);
        await WidgetsBinding.instance.endOfFrame;
        await serializeGlOp(() => preview.dispose());
      },
      releaseScreenAwake,
      () async => OfflineRuntime.reset(),
    ]).exit();
  }

  void _handleKeyEvent(KeyEvent event) {
    if (event is! KeyDownEvent) return;
    final key = event.logicalKey;
    if (_keyBindings?.resolve(key) == InputAction.back) {
      unawaited(_confirmExit());
      return;
    }
    if (_tiles.isEmpty) return;
    if (key == LogicalKeyboardKey.arrowLeft) {
      _moveRing(-1);
    } else if (key == LogicalKeyboardKey.arrowRight) {
      _moveRing(1);
    } else if (key == LogicalKeyboardKey.arrowUp || key == LogicalKeyboardKey.arrowDown) {
      _toggleCenterRing();
    } else if (key == LogicalKeyboardKey.enter) {
      unawaited(_activateFocused());
    }
  }

  @override
  void dispose() {
    final preview = _previewController;
    if (preview != null) unawaited(serializeGlOp(() => preview.dispose()));
    for (final node in _focusNodes) {
      node.dispose();
    }
    _pageFocusNode.dispose();
    super.dispose();
  }

  Widget _channelFallback(PlayheadChannel c) => Container(
        color: Colors.grey.shade900,
        alignment: Alignment.center,
        child: Text(
          '${c.number}\n${c.name}',
          textAlign: TextAlign.center,
          style: const TextStyle(color: Colors.white, fontSize: 18),
        ),
      );

  Widget _buildFeaturedTile() {
    final channelData = _tiles[0].channel!;
    final focused = _focusedIndex == 0;
    return Focus(
      focusNode: _focusNodes[0],
      child: Container(
        key: Key('mosaic-tile-${channelData.id}'),
        width: double.infinity,
        height: double.infinity,
        clipBehavior: Clip.antiAlias,
        decoration: BoxDecoration(
          border: Border.all(color: focused ? Colors.white : Colors.white24, width: focused ? 4 : 2),
          borderRadius: BorderRadius.circular(8),
        ),
        child: Stack(
          fit: StackFit.expand,
          children: [
            // Positioned.fill (not the bare widget): a Stack child without
            // Positioned breaks the fvp/libmdk GL texture composition on some
            // GPU/driver combinations (seen with NVIDIA's proprietary driver)
            // — same bug/root cause documented in main.dart on the EPG's
            // ValueListenableBuilder, isolated by reverse bisection. It
            // reproduced here too: the featured preview stayed black even
            // though the audio played normally, until this change.
            Positioned.fill(
              child: _previewController != null && _previewController!.value.isInitialized
                  ? FittedBox(
                      fit: BoxFit.cover,
                      child: SizedBox(
                        width: _previewController!.value.size.width,
                        height: _previewController!.value.size.height,
                        child: VideoPlayer(_previewController!),
                      ),
                    )
                  : channelData.guideArtUrl != null
                      ? ArtImage(
                          channelData.guideArtUrl!,
                          fallback: (context) => _channelFallback(channelData),
                        )
                      : _channelFallback(channelData),
            ),
            Positioned(
              left: 12,
              bottom: 12,
              child: Container(
                padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                color: Colors.black54,
                child: Text(
                  '${channelData.number} — ${channelData.name}',
                  style: const TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildSmallTile(int idx) {
    final tile = _tiles[idx];
    final focused = _focusedIndex == idx;
    final borderColor = focused ? Colors.white : Colors.white24;
    final borderWidth = focused ? 4.0 : 2.0;

    if (tile.isSettings) {
      return Focus(
        focusNode: _focusNodes[idx],
        child: Container(
          key: const Key('mosaic-tile-settings'),
          width: double.infinity,
          height: double.infinity,
          alignment: Alignment.center,
          decoration: BoxDecoration(
            color: Colors.grey.shade900,
            border: Border.all(color: borderColor, width: borderWidth),
            borderRadius: BorderRadius.circular(8),
          ),
          child: const Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(AppIcons.settings, color: Colors.white, size: 28),
              SizedBox(height: 4),
              Text('Settings', style: TextStyle(color: Colors.white, fontSize: 12)),
            ],
          ),
        ),
      );
    }

    final channelData = tile.channel!;
    return Focus(
      focusNode: _focusNodes[idx],
      child: Container(
        key: Key('mosaic-tile-${channelData.id}'),
        width: double.infinity,
        height: double.infinity,
        clipBehavior: Clip.antiAlias,
        decoration: BoxDecoration(
          border: Border.all(color: borderColor, width: borderWidth),
          borderRadius: BorderRadius.circular(8),
        ),
        child: channelData.guideArtUrl != null
            ? ArtImage(
                channelData.guideArtUrl!,
                fallback: (context) => _channelFallback(channelData),
              )
            : _channelFallback(channelData),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    if (_loading) {
      return const Scaffold(
        key: Key('mosaic-page'),
        backgroundColor: Colors.black,
        body: Center(child: CircularProgressIndicator()),
      );
    }
    if (_error != null) {
      // Nothing to navigate, but Back must still be able to leave the app.
      return KeyboardListener(
        focusNode: _pageFocusNode,
        autofocus: true,
        onKeyEvent: _handleKeyEvent,
        child: Scaffold(
          key: const Key('mosaic-page'),
          backgroundColor: Colors.black,
          body: Center(
            child: Text(_error!, style: const TextStyle(color: Colors.white, fontSize: 16)),
          ),
        ),
      );
    }
    return KeyboardListener(
      focusNode: _pageFocusNode,
      autofocus: true,
      onKeyEvent: _handleKeyEvent,
      child: Scaffold(
        key: const Key('mosaic-page'),
        backgroundColor: Colors.black,
        body: SafeArea(
          child: Center(child: _buildFrame()),
        ),
      ),
    );
  }

  // Frame: 4x4 logical grid, cells of `_cellWidth`x`_cellHeight`, with the
  // featured video occupying the central 2x2 block (rows/columns 1-2) and up
  // to `_ringSlots.length` tiles filling the rest, in the order of `_tiles`
  // (skipping the featured one) mapped 1:1 to `_ringSlots`.
  static const _cellWidth = 160.0;
  static const _cellHeight = 90.0;
  static const _gap = 8.0;

  Widget _buildFrame() {
    const totalWidth = _cellWidth * 4 + _gap * 3;
    const totalHeight = _cellHeight * 4 + _gap * 3;
    double left(int col) => col * (_cellWidth + _gap);
    double top(int row) => row * (_cellHeight + _gap);

    final ringTiles = _tiles.skip(1).toList();
    return SizedBox(
      width: totalWidth,
      height: totalHeight,
      child: Stack(
        children: [
          for (var i = 0; i < ringTiles.length && i < _ringSlots.length; i++)
            Positioned(
              left: left(_ringSlots[i].col),
              top: top(_ringSlots[i].row),
              width: _cellWidth,
              height: _cellHeight,
              child: _buildSmallTile(i + 1),
            ),
          Positioned(
            left: left(1),
            top: top(1),
            width: _cellWidth * 2 + _gap,
            height: _cellHeight * 2 + _gap,
            child: _buildFeaturedTile(),
          ),
        ],
      ),
    );
  }
}
