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

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import '../video/video_engine.dart';
import '../util/controller_lifecycle.dart';

import '../cache/offline_runtime.dart';
import '../plugin/channel_filter.dart';
import '../cache/schedule_sync.dart';
import '../main.dart';
import '../playhead_api.dart';
import '../ui/form_factor.dart';
import '../ui/ui_scale.dart';
import '../mosaic_nav.dart';
import '../mosaic_tiles.dart';
import '../widgets/exit_dialog.dart';
import '../widgets/mosaic_frame.dart';
import '../widgets/phone_mosaic_view.dart';
import '../input/input_action.dart';
import '../services/app_exit.dart';
import '../services/screen_awake.dart';
import '../services/settings_service.dart';
import 'settings_page.dart';
import '../log/log.dart';

class MosaicPage extends StatefulWidget {
  const MosaicPage({super.key});

  @override
  State<MosaicPage> createState() => _MosaicPageState();
}

/// How long the mosaic waits before asking a plugin for the featured channel's
/// stream. A plugin-backed preview is not free: the host starts an extractor and
/// a remux for it, so a visit that lasts a second (Back, then straight into
/// another channel) must not start one. Long enough to skip a passing look,
/// short enough that a viewer who stays gets the picture.
const mosaicPluginPreviewDwell = Duration(milliseconds: 2500);

class _MosaicPageState extends State<MosaicPage> with WidgetsBindingObserver {
  late OfflineRuntime _runtime;
  late SettingsService _settings;
  KeyBindingsService? _keyBindings;
  bool _exitDialogOpen = false;

  bool _loading = true;
  String? _error;

  List<PlayheadChannel> _allChannels = [];
  PlayheadChannel? _featured;
  List<MosaicTile> _tiles = [];
  List<FocusNode> _focusNodes = [];
  int _focusedIndex = 0;
  // The previous arrow move (where it came from and which way), so the exact
  // opposite arrow returns to the origin (mosaic_nav.dart, `returnTo`).
  ({int from, MosaicDirection direction})? _lastMove;

  VideoPlayback? _previewController;
  // Bumped whenever the preview is torn down because the app left the screen,
  // so a preview still opening at that moment is discarded when it finishes.
  int _previewGeneration = 0;
  bool _appHidden = false;

  final FocusNode _pageFocusNode = FocusNode();

  /// Ring on a television and on the desktop, one column on a phone. Resolved
  /// once, at construction: the form factor is decided at boot and never
  /// re-derived from a resize (docs/ARCHITECTURE.md "Client form factors").
  late final MosaicLayoutMode _layoutMode = ClientFormFactorState.notifier.value == ClientFormFactor.phone
      ? MosaicLayoutMode.list
      : MosaicLayoutMode.ring;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _bootstrap();
  }

  // The preview plays with audio, so it must stop when the app is hidden or
  // paused (Android Home, a minimized window) and come back when it returns.
  // `inactive` (focus lost, window still visible) keeps it running.
  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.hidden || state == AppLifecycleState.paused) {
      if (_appHidden) return;
      _appHidden = true;
      _previewGeneration++;
      unawaited(_stopPreview());
    } else if (state == AppLifecycleState.resumed) {
      if (!_appHidden) return;
      _appHidden = false;
      if (mounted && !_loading && _previewController == null) unawaited(_startPreview());
    }
  }

  Future<void> _stopPreview() async {
    final preview = _previewController;
    if (preview == null) return;
    Log.info('mosaic', 'app hidden: stopping the preview');
    if (mounted) setState(() => _previewController = null);
    // Let the tree drop the surface first. A hidden app draws no frames, so
    // this wait would never end without the bound (and the preview would stay
    // alive, audible, behind the user's back).
    await WidgetsBinding.instance.endOfFrame.timeout(const Duration(milliseconds: 500), onTimeout: () {});
    await disposeController(preview);
  }

  // Settings can change the channel list under this screen: turning a plugin off
  // takes away the channels it feeds, and clearing the cache empties the list.
  // Rebuilding the frame on return is what stops the mosaic from offering — and
  // previewing — a channel that can no longer play. Nothing is touched when the
  // set is the same, so a visit that changed nothing costs no preview restart.
  Future<void> _reloadAfterSettings() async {
    final runtime = _runtime;
    await runtime.refreshAvailability();
    if (!mounted) return;
    final playable = playableChannels(runtime.cache, runtime.cache.channels(), runtime.availability);
    final visible = playable.visible;
    final before = {for (final channel in _allChannels) channel.id};
    final after = {for (final channel in visible) channel.id};
    if (before.length == after.length && before.containsAll(after)) return;
    _previewGeneration++;
    await _stopPreview();
    if (!mounted) return;
    if (visible.isEmpty) {
      final status = runtime.syncer.status.value;
      setState(() {
        _allChannels = const [];
        _tiles = const [];
        _featured = null;
        _error = playable.allHiddenByPlugins
            ? 'No channel can play on this device yet. ${playable.reasons.join(' ')} '
                '${playable.nextStep ?? ''}'
                .trimRight()
            : status.lastSucceeded
                ? 'ERROR: no channels configured on the backend (GET /channels is empty)'
                : 'ERROR: failed to load channels: ${status.lastError}';
      });
      return;
    }
    setState(() {
      _error = null;
      _allChannels = visible;
      _resolveTiles();
    });
    unawaited(_startPreview());
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
      // The same refresh the backend just did may have brought a channel whose
      // source needs a plugin.
      await _runtime.refreshAvailability();
      final playable = playableChannels(_runtime.cache, channels, _runtime.availability);
      channels = playable.visible;
      if (!mounted) return;
      if (channels.isEmpty) {
        final status = _runtime.syncer.status.value;
        setState(() {
          _loading = false;
          _error = playable.allHiddenByPlugins
              ? 'No channel can play on this device yet. ${playable.reasons.join(' ')} '
                  '${playable.nextStep ?? ''}'
                  .trimRight()
              : status.lastSucceeded
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

  // Which tiles this form factor shows, and in what order: the pure decision
  // lives in mosaic_tiles.dart. The ring caps at the frame's cells and shuffles
  // the tail; the phone's list holds every channel in number order.
  void _resolveTiles() {
    final built = buildMosaicTiles(
      channels: _allChannels,
      recentIds: _settings.recentChannelIds,
      mode: _layoutMode,
    );
    _featured = built.tiles.isEmpty ? null : built.tiles[0].channel;
    _tiles = built.tiles;
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
    if (featured == null || _appHidden) return;
    final generation = _previewGeneration;
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
      if (!mounted) return;

      // An item whose source is a plugin has no media-server id at all, exactly
      // as in the player (`_entrySource` in main.dart): the url comes from the
      // backend, which runs the plugin, and it already begins at the position
      // asked for. Resolving costs the host a process, hence the dwell, and a
      // failure (plugin unreachable, item withdrawn) stays a placeholder.
      final requires = entry.requires;
      Uri source;
      Uri? audioSource;
      var alreadyPositioned = false;
      if (requires.isEmpty) {
        if (externalId == null) return;
        source = Uri.parse(streamUrl(externalId));
      } else {
        // Not `entry.externalId`: that is the source's own id (the id a plugin
        // understands), while the play route names the catalog row the guide
        // was built from.
        final catalogItemId = entry.catalogItemId;
        if (catalogItemId.isEmpty) return;
        var position = now.difference(itemStart);
        if (position < Duration.zero) position = Duration.zero;
        if (position > itemEnd.difference(itemStart)) position = itemEnd.difference(itemStart);
        position += Duration(milliseconds: entry.itemOffsetMs);
        await Future<void>.delayed(mosaicPluginPreviewDwell);
        if (!mounted || _appHidden || generation != _previewGeneration) return;
        final stream = await _runtime.api.playUrl(catalogItemId,
            startAt: position, modes: pluginPlaybackModes(videoEngine));
        source = stream.uri;
        audioSource = stream.audioUri;
        // Only a stream the plugin produced begins where it was asked to; a pair
        // or a single file is the item from its own zero and is seeked below.
        alreadyPositioned = stream.startsAtRequestedPosition;
        if (!mounted || _appHidden || generation != _previewGeneration) return;
        Log.debug('mosaic', 'preview resolved by "${requires.first.id}" (start=${position.inSeconds}s)');
      }

      final controller = trackController(videoEngine.createNetwork(source, audioUri: audioSource));
      final clock = Stopwatch()..start();
      try {
        await _openPreview(controller, now, itemStart, itemEnd, alreadyPositioned: alreadyPositioned);
        Log.debug('mosaic', 'preview opened in ${clock.elapsedMilliseconds}ms');
      } catch (e) {
        Log.warn('mosaic', 'preview failed to open after ${clock.elapsedMilliseconds}ms', error: e);
        unawaited(disposeController(controller));
        return;
      }
      if (!mounted || generation != _previewGeneration) {
        // Left the mosaic (or hid the app) while the preview was opening or
        // starting: it may already be audible, so it is silenced before its
        // dispose.
        unawaited(discardController(controller));
        return;
      }
      setState(() => _previewController = controller);
    } catch (_) {
      // decorative — no handling beyond leaving the placeholder in place.
    }
  }

  Future<void> _openPreview(VideoPlayback controller, DateTime now, DateTime itemStart, DateTime itemEnd,
      {bool alreadyPositioned = false}) async {
    await controller.open(label: 'initialize-preview');
    if (!mounted) return;

    // A plugin stream was asked to begin at that position, so seeking again
    // would restart it where it already is (see `_playbackOffset` in main.dart
    // for the same rule in the player).
    var seek = alreadyPositioned ? Duration.zero : now.difference(itemStart);
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
  // way. On a phone the same arrows walk the column, one row at a time.
  void _move(MosaicDirection direction) {
    final last = _lastMove;
    final target = mosaicMove(
      mode: _layoutMode,
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
  Future<void> _activateFocused() => _activate(_focusedIndex);

  /// A tap on a row is the same outcome as Select on it: focus, then open.
  void _tapTile(int index) {
    if (index < 0 || index >= _tiles.length) return;
    _focusIndex(index);
    unawaited(_activate(index));
  }

  Future<void> _activate(int index) async {
    final tile = _tiles[index];
    if (tile.isSettings) {
      await Navigator.of(context).push(MaterialPageRoute(builder: (_) => const SettingsPage()));
      if (!mounted) return;
      await _reloadAfterSettings();
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
    final action = _keyBindings?.resolve(key);
    if (action == InputAction.back) {
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
    } else if (action == InputAction.select) {
      unawaited(_activateFocused());
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    final preview = _previewController;
    if (preview != null) unawaited(disposeController(preview));
    for (final node in _focusNodes) {
      node.dispose();
    }
    _pageFocusNode.dispose();
    super.dispose();
  }

  MosaicTileSpec _specFor(MosaicTile tile) {
    if (tile.isSettings) return const MosaicTileSpec.settings();
    final c = tile.channel!;
    return MosaicTileSpec.channel(
      key: Key('mosaic-tile-${c.id}'),
      number: c.number,
      name: c.name,
      artUrl: c.guideArtUrl,
      // Only the phone's column shows it, and only when the cache knows.
      nowTitle: _layoutMode == MosaicLayoutMode.list ? _nowTitle(c.id) : null,
    );
  }

  /// What [channelId] is showing right now, from the offline cache: the row's
  /// subtitle on a phone. Null when the guide has nothing for it.
  String? _nowTitle(String channelId) {
    final now = DateTime.now().toUtc();
    final entries = _runtime.cache.entries(channelId, now, now.add(const Duration(minutes: 1)));
    if (entries.isEmpty) return null;
    return entries.first.displayTitle;
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
        // The mosaic is the one full-bleed screen with no AppBar: it draws its
        // own black background, so the insets are its job. Zero on a television
        // and on the desktop, so not a pixel of the mosaic goldens moves.
        body: SafeArea(
          child: _layoutMode == MosaicLayoutMode.list
              ? PhoneMosaicView(
                  tiles: [for (final t in _tiles) _specFor(t)],
                  focusNodes: _focusNodes,
                  focusedIndex: _focusedIndex,
                  featuredBackground: _featuredBackground(),
                  onSelect: _tapTile,
                )
              : MosaicFrameView(
                  tiles: [for (final t in _tiles) _specFor(t)],
                  focusNodes: _focusNodes,
                  focusedIndex: _focusedIndex,
                  featuredBackground: _featuredBackground(),
                  safeMargin: AppConfig.mosaicSafeMargin,
                ),
        ),
      ),
    );
  }
}
