// Mosaic screen: the home shown when there's no watched-channel history yet
// (see `_InitialRouteResolver` in main.dart), and the destination of the Home
// key from the player. Visual model: an old satellite-TV waiting screen —
// channel logos around the edge, one "live" channel playing in the middle
// (here decorative: its own VideoPlayerController, without the player's
// crossfade/preload engine).
//
// Layout: a FRAME of up to 12 tiles (`mosaicRingSlots`) surrounds the featured
// video, which occupies the central 2x2 block of a 4x4 grid. The frame fills
// the screen vertically (mosaic_layout.dart) and everything in it scales with
// it and with the app-wide UiScale (widgets/mosaic_frame.dart). Navigation is
// spatial (mosaic_nav.dart): each arrow goes to the nearest occupied tile in
// that direction, the featured block being one target. Literal
// (arrowLeft/Right/Up/Down) + Enter, bypassing KeyBindingsService/InputAction —
// a deliberate decision, a 2D grid is a different context from the linear
// player (see AGENTS.md).
//
// Back (`InputAction.back`, Esc by default) is the one remappable key here: it
// opens the "Exit Playhead TV?" confirmation, the only way to leave the app.
// Cancel has the focus; Back or Cancel closes it; Exit tears down (preview
// controller, display wake lock, syncer) and terminates (services/app_exit.dart).
import 'dart:async';
import 'dart:math';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import '../video/video_engine.dart';
import '../util/controller_lifecycle.dart';

import '../cache/offline_runtime.dart';
import '../cache/schedule_sync.dart';
import '../main.dart';
import '../playhead_api.dart';
import '../ui/ui_scale.dart';
import '../mosaic_nav.dart';
import '../widgets/exit_dialog.dart';
import '../widgets/mosaic_frame.dart';
import '../input/input_action.dart';
import '../services/app_exit.dart';
import '../services/screen_awake.dart';
import '../services/settings_service.dart';
import 'settings_page.dart';
import '../log/log.dart';

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
  // The previous arrow move (where it came from and which way), so the exact
  // opposite arrow returns to the origin (mosaic_nav.dart, `returnTo`).
  ({int from, MosaicDirection direction})? _lastMove;

  VideoPlayback? _previewController;

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
  // `mosaicRingSlots.length`): first the rest of the recent history, then random
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

    // The frame has `mosaicRingSlots.length` cells; one of them is always the fixed
    // Settings tile, so at most `_ringSlots.length - 1` surrounding channels
    // fit.
    final maxSurrounding = mosaicRingSlots.length - 1;
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

      final controller = trackController(videoEngine.createNetwork(Uri.parse(streamUrl(externalId))));
      final clock = Stopwatch()..start();
      try {
        await _openPreview(controller, now, itemStart, itemEnd);
        Log.debug('mosaic', 'preview opened in ${clock.elapsedMilliseconds}ms');
      } catch (e) {
        Log.warn('mosaic', 'preview failed to open after ${clock.elapsedMilliseconds}ms', error: e);
        unawaited(disposeController(controller));
        return;
      }
      if (!mounted) {
        // Left the mosaic while the preview was opening or starting: it may
        // already be audible, so it is silenced before its dispose.
        unawaited(discardController(controller));
        return;
      }
      setState(() => _previewController = controller);
    } catch (_) {
      // decorative — no handling beyond leaving the placeholder in place.
    }
  }

  Future<void> _openPreview(VideoPlayback controller, DateTime now, DateTime itemStart, DateTime itemEnd) async {
    await controller.open(label: 'initialize-preview');
    if (!mounted) return;

    var seek = now.difference(itemStart);
    final clipDuration = itemEnd.difference(itemStart);
    if (seek < Duration.zero) seek = Duration.zero;
    if (seek > clipDuration) seek = clipDuration;
    if (seek > Duration.zero) await controller.seekTo(seek);

    controller.selectSubtitles(const []);
    controller.tune(const PlaybackTuning());
    if (!mounted) return;
    await controller.play();
  }

  // Spatial move (see mosaic_nav.dart); the focus stays when nothing lies that
  // way.
  void _move(MosaicDirection direction) {
    final last = _lastMove;
    final target = mosaicNavigate(
      tileCount: _tiles.length,
      focused: _focusedIndex,
      direction: direction,
      returnTo: last != null && last.direction == direction.opposite ? last.from : null,
    );
    if (target == _focusedIndex) return;
    _lastMove = (from: _focusedIndex, direction: direction);
    _focusIndex(target);
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
    Log.info('mosaic', 'opening channel ${tile.channel!.number} from the mosaic');
    final preview = _previewController;
    _previewController = null;
    if (preview != null) {
      // Native teardown is queued by the engine (see gl_serial.dart): this await
      // alone isn't enough once the channel player's own controllers start
      // being created through the same shared queue — it guarantees this
      // dispose is strictly ordered relative to them, not just relative to
      // this function's own subsequent code.
      await disposeController(preview);
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
      builder: (dialogContext) => ExitDialog(
        isBack: (key) => _keyBindings?.resolve(key) == InputAction.back,
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
        await disposeController(preview);
      },
      releaseScreenAwake,
      () async => OfflineRuntime.reset(),
      Log.flush,
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
      _move(MosaicDirection.left);
    } else if (key == LogicalKeyboardKey.arrowRight) {
      _move(MosaicDirection.right);
    } else if (key == LogicalKeyboardKey.arrowUp) {
      _move(MosaicDirection.up);
    } else if (key == LogicalKeyboardKey.arrowDown) {
      _move(MosaicDirection.down);
    } else if (selectKeyAliases.contains(key)) {
      unawaited(_activateFocused());
    }
  }

  @override
  void dispose() {
    final preview = _previewController;
    if (preview != null) unawaited(disposeController(preview));
    for (final node in _focusNodes) {
      node.dispose();
    }
    _pageFocusNode.dispose();
    super.dispose();
  }

  MosaicTileSpec _specFor(_MosaicTile tile) {
    if (tile.isSettings) return const MosaicTileSpec.settings();
    final c = tile.channel!;
    return MosaicTileSpec.channel(
        key: Key('mosaic-tile-${c.id}'), number: c.number, name: c.name, artUrl: c.guideArtUrl);
  }

  // The featured tile's picture: the preview video once it is playing, else
  // the channel's art or a placeholder.
  Widget _featuredBackground() {
    final controller = _previewController;
    final state = controller?.state;
    if (controller != null && state != null && state.isInitialized) {
      return FittedBox(
        fit: BoxFit.cover,
        child: SizedBox(
          width: state.size.width,
          height: state.size.height,
          child: VideoSurface(controller),
        ),
      );
    }
    return MosaicTileArt(spec: _specFor(_tiles[0]));
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
            child: Builder(
                builder: (context) => Text(_error!, style: TextStyle(color: Colors.white, fontSize: context.ui(20)))),
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
        body: MosaicFrameView(
          tiles: [for (final t in _tiles) _specFor(t)],
          focusNodes: _focusNodes,
          focusedIndex: _focusedIndex,
          featuredBackground: _featuredBackground(),
          safeMargin: AppConfig.mosaicSafeMargin,
        ),
      ),
    );
  }
}
