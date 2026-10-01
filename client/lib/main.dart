// Playhead TV client: assembles a "channel" (program -> commercial -> program)
// on the client with preload + crossfade, using fvp (libmdk) through
// video_player, with no server-side transcoding. The Jellyfin server and API
// key are never hardcoded here — they come from --dart-define (see README).
//
// Video engine: fvp (libmdk) replaced media_kit (libmpv) after media_kit was
// found to stall / fall back to software rendering on Linux (upstream bug
// media-kit/media-kit#1404).
//
// The item list is resolved at runtime from the Go backend (GET /channels +
// GET /epg + GET /catalog-items/{id}), which decides what plays and when —
// this client only resolves and plays it (see AGENTS.md).
//
// Channel switching: arrow up/down calls _switchChannel, which cancels
// everything belonging to the current channel and reloads the schedule of the
// neighboring channel (by `number`, wrapping around the list). A
// `_loadGeneration` counter invalidates any preload/crossfade/schedule load
// still in flight from a previous switch — without it, a fast switch (two
// arrow presses before the first load finished) could apply the result of the
// wrong channel on top of the new channel's state.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:video_player/video_player.dart';
import 'package:fvp/fvp.dart' as fvp;

import 'playhead_api.dart';
import 'services/settings_service.dart';
import 'services/stream_preferences_service.dart';
import 'services/stream_selection_resolver.dart';
import 'input/input_action.dart';
import 'pages/mosaic_page.dart';
import 'util/gl_serial.dart';
import 'widgets/epg_overlay.dart';
import 'widgets/osd_overlay.dart';
import 'widgets/debug_overlay.dart';
import 'widgets/track_selector_overlay.dart';

// AppConfig: in production, every field below is just the --dart-define of the
// same name (a compile-time constant). The integration tests
// (client/integration_test) can't use --dart-define for this because the real
// values (the fixture backend's port, the fixture video server's port) only
// exist at runtime, after the processes have already started — they can't be
// known at compile time. Instead of changing ChannelPlayerPage's signature
// (which would force every config read to become a parameter threaded through
// several layers), AppConfig exposes a static override that the test calls
// before pumpWidget; without an override (the production case), each getter
// falls back to the usual --dart-define — the smallest possible behavior
// change for the real app.
class AppConfig {
  static String? _jellyfinUrl;
  static String? _jellyfinApiKey;
  static String? _playheadBaseUrl;
  static String? _playheadDeviceToken;
  static String? _playheadChannelId;

  static String get jellyfinUrl => _jellyfinUrl ?? const String.fromEnvironment('JELLYFIN_URL');
  static String get jellyfinApiKey => _jellyfinApiKey ?? const String.fromEnvironment('JELLYFIN_API_KEY');
  // Playhead TV backend (Go) that materializes the schedule — see docs/ARCHITECTURE.md.
  static String get playheadBaseUrl => _playheadBaseUrl ?? const String.fromEnvironment('PLAYHEADTV_BASE_URL');
  // Optional: device token for the Auth flow. Empty = no Authorization header
  // is sent, which matches a backend running without auth.
  static String get playheadDeviceToken =>
      _playheadDeviceToken ?? const String.fromEnvironment('PLAYHEADTV_DEVICE_TOKEN');
  // Optional: if empty, automatically resolves the channel with the lowest
  // `number` via GET /channels instead of requiring the operator to know an ID
  // up front.
  static String get playheadChannelIdDefine =>
      _playheadChannelId ?? const String.fromEnvironment('PLAYHEADTV_CHANNEL_ID');

  /// Only used by `integration_test/` — never called in the real app (`main()`
  /// doesn't call this, so production never sees anything but the
  /// --dart-define values).
  static void overrideForTest({
    required String jellyfinUrl,
    required String jellyfinApiKey,
    required String playheadBaseUrl,
    String playheadDeviceToken = '',
    String playheadChannelId = '',
  }) {
    _jellyfinUrl = jellyfinUrl;
    _jellyfinApiKey = jellyfinApiKey;
    _playheadBaseUrl = playheadBaseUrl;
    _playheadDeviceToken = playheadDeviceToken;
    _playheadChannelId = playheadChannelId;
  }
}

// Schedule window fetched at a time via GET /epg. Rolling window: the client
// doesn't stop after the first window — it keeps pulling subsequent windows
// ahead of "now" whenever the current channel has little loaded runway left,
// see _epgLoadedUntil/_maybeExtendGrid below.
const epgWindow = Duration(hours: 6);

// When the sum of clipDuration of the not-yet-played items (from the current
// item onward) falls below this, the fetch of the next window is triggered in
// the background. 2h of remaining runway is enough slack for a network fetch
// to finish well before the end of the loaded schedule.
const epgExtendThreshold = Duration(hours: 2);

// Retry interval when the materialized schedule has really run out (a real
// gap in the schedule, not just the end of the fetched window) — repeats until
// something materialized is found and playback resumes on its own.
const epgGapRetryInterval = Duration(seconds: 20);

// Direct jump by channel number (real remote-control style navigation): time
// without a new digit before the typed buffer resolves on its own, and the
// limit of accepted digits — no 4-digit channels are planned, so reaching the
// limit resolves immediately without waiting for the timeout.
const channelInputCommitDelay = Duration(milliseconds: 1500);
const channelInputMaxDigits = 3;

class ChannelEntry {
  final String id;
  final String label;
  final Duration clipDuration; // item duration in the schedule (epg_entry end_at - start_at)
  // The EPG's real, wall-clock scheduled start (epg_entry.start_at) — see
  // issue #9. `clipDuration` alone only says how long to play the item once
  // started; `startAt` is what lets _crossfadeTo tell an interior schedule
  // gap (startAt still in the future when the previous item ends) from the
  // client simply running behind (startAt already in the past).
  final DateTime startAt;
  const ChannelEntry(this.id, this.label, this.clipDuration, this.startAt);
}

// Populated at runtime by _loadAndPlayChannel from the GET /epg materialized
// by the backend — never a hardcoded constant.
List<ChannelEntry> channel = <ChannelEntry>[];

const crossfadeDuration = Duration(milliseconds: 800);
const preloadLead = Duration(seconds: 2);

// Last-resort floor under _scheduleTransitions' `effective` duration. The
// GET /epg filters in _loadAndPlayChannel/_doFetchNextEpgSlice (and
// pullsync's own sync-time guard) should already keep a non-positive-duration
// item out of `channel` entirely, but if one ever slips through anyway,
// _scheduleTransitions would otherwise fire its next-item Timer immediately,
// churning real controller create/dispose cycles fast enough to exhaust file
// descriptors (reproduced 2026-09-30 — see AGENTS.md). This never changes
// normal playback: every real clip duration is comfortably above it.
const minTransitionInterval = Duration(seconds: 5);

// How far `now` may drift from a ChannelEntry's real `startAt` (either way)
// before _crossfadeTo treats it as something to correct, rather than normal
// jitter (network latency, the ~30-step crossfade ramp itself, preload
// timing slop). Below this, items keep playing back to back exactly as
// before issue #9.
const epgSyncTolerance = Duration(seconds: 2);

// Looping local slate shown while holding for an interior EPG gap (see
// _holdForGap) — a schedule hole wide enough that honoring the next item's
// real startAt means waiting, not starting it early. See
// assets/slate/NOTICE.md for the asset's attribution.
const slateAssetPath = 'assets/slate/gap_slate.mp4';

// Video thumbnail inside the EPG — the same instance of
// _controllerA/_controllerB, just repositioned/resized into a corner (see
// _videoThumbnailRect and the long comment about the fvp/libmdk GL texture bug
// above _osdVisibleNotifier).
const epgThumbnailMargin = 24.0;
const epgThumbnailWidth = 320.0;
const epgThumbnailHeight = 180.0;

// Playback buffer window handed to mdk (see _createController).
const playbackBufferMinMs = 2000;
const playbackBufferMaxMs = 30000;

String streamUrl(String itemId) =>
    '${AppConfig.jellyfinUrl}/Videos/$itemId/stream?Static=true&api_key=${AppConfig.jellyfinApiKey}';

void main() {
  // video.decoders: fvp's default decoder order on Linux tries VAAPI before
  // CUDA/VDPAU. On some setups (e.g. NVIDIA without VAAPI configured) that
  // stalls decoding of HEVC videos (audio plays, "initialized" never fires):
  // the same Jellyfin URL plays smoothly with CUDA or FFmpeg but stalls with
  // the default order. Putting CUDA first avoids that and keeps VAAPI/VDPAU as
  // fallbacks for AMD/Intel machines (where CUDA simply fails fast and falls
  // through to the next decoder).
  fvp.registerWith(options: {
    'video.decoders': ['CUDA', 'VAAPI', 'VDPAU', 'FFmpeg'],
  });
  runApp(const PlayheadSpikeApp());
}

class PlayheadSpikeApp extends StatelessWidget {
  const PlayheadSpikeApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Playhead TV',
      theme: ThemeData.dark(),
      home: const _InitialRouteResolver(),
    );
  }
}

// Resolves, at runtime, whether boot jumps straight to the player (a last
// watched channel is already persisted) or shows the mosaic (first run / no
// history). SharedPreferences is local and fast — the waiting screen below
// should last only an instant. It doesn't validate here whether `lastWatched`
// still exists in GET /channels: `_bootstrapChannels` already falls back to
// the channel with the lowest number if the id doesn't match anything, so that
// check would be redundant.
class _InitialRouteResolver extends StatefulWidget {
  const _InitialRouteResolver();

  @override
  State<_InitialRouteResolver> createState() => _InitialRouteResolverState();
}

class _InitialRouteResolverState extends State<_InitialRouteResolver> {
  @override
  void initState() {
    super.initState();
    _resolve();
  }

  Future<void> _resolve() async {
    final settings = await SettingsService.create();
    if (!mounted) return;
    final lastWatched = settings.lastWatchedChannelId;
    final page = lastWatched != null
        ? ChannelPlayerPage(initialChannelId: lastWatched)
        : const MosaicPage();
    Navigator.of(context).pushReplacement(MaterialPageRoute(builder: (_) => page));
  }

  @override
  Widget build(BuildContext context) {
    return const Scaffold(body: Center(child: CircularProgressIndicator()));
  }
}

class ChannelPlayerPage extends StatefulWidget {
  final String? initialChannelId;
  const ChannelPlayerPage({super.key, this.initialChannelId});

  @override
  State<ChannelPlayerPage> createState() => _ChannelPlayerPageState();
}

class _ChannelPlayerPageState extends State<ChannelPlayerPage> {
  // Each slot holds the VideoPlayerController of the item that is playing
  // (active) or being preloaded (inactive). Unlike media_kit, video_player/fvp
  // doesn't reuse the same instance to switch media — every new item is a new
  // VideoPlayerController, disposed when it leaves the scene.
  VideoPlayerController? _controllerA;
  VideoPlayerController? _controllerB;

  bool _aIsActive = true;
  double _opacityA = 1;
  double _opacityB = 0;
  int _currentIndex = -1;
  String _status = 'starting…';
  Timer? _advanceTimer;
  Timer? _volumeRampTimer;
  final FocusNode _focusNode = FocusNode();

  // OSD (on-screen channel guide): shown on every item change (channel start
  // or end of a crossfade), stays visible for ~5s and fades out. The ~2x/s
  // ticker that re-reads the video's real position for the progress bar lives
  // inside `ChannelOsd` (its own widget, see below) — isolated there so its
  // rebuild doesn't force a rebuild of the whole page (and, in turn, of the
  // two `VideoPlayer`/`_videoFor` widgets in the Stack). Showing/hiding the
  // OSD (`_osdVisibleNotifier` below, not a State `bool`) also never calls
  // `setState` on the parent for the same reason: a `ValueNotifier` observed
  // only by the overlay's own `ValueListenableBuilder` (see `build`)
  // COMPLETELY isolates any OSD visibility/tick change from the rest of the
  // tree — not even a parent rebuild happens anymore. This really matters:
  // with 1080p+ video, a single parent rebuild was enough to re-expose a real
  // artifact in `fvp`/libmdk (the textures of the two `VideoPlayerController`s
  // — active and preloading — showing side by side for an instant, each
  // taking half the screen, instead of one overlaying the other via opacity),
  // reproduced just by pressing the Info key (`I`) with no channel switch
  // involved.
  final ValueNotifier<bool> _osdVisibleNotifier = ValueNotifier(false);
  Timer? _osdHideTimer;

  // Focus mode of the OSD (see widgets/osd_overlay.dart): off while the OSD is
  // just displayed (auto-show), on once the user pressed Select and then an
  // arrow (Select, Select opens the EPG instead). Always turned off when the
  // OSD hides.
  final ValueNotifier<bool> _osdFocusNotifier = ValueNotifier(false);
  // True while the OSD was opened by Select and no key has followed yet: the
  // next arrow enters focus mode instead of switching channel, the next
  // Select opens the EPG. Reset whenever the OSD hides.
  bool _osdArmed = false;
  final GlobalKey<ChannelOsdState> _osdKey = GlobalKey<ChannelOsdState>();

  // Full-screen EPG: state plus the open/close flow. Same pattern as
  // `_osdVisibleNotifier` above: never a State `bool`/setState, only
  // ValueNotifier + ValueListenableBuilder, for the same GL texture artifact
  // reason.
  final ValueNotifier<bool> _epgOpenNotifier = ValueNotifier(false);
  final GlobalKey<EpgOverlayState> _epgOverlayKey = GlobalKey<EpgOverlayState>();

  // Debug overlay (D key — InputAction.debugOverlay): same isolated
  // ValueNotifier pattern as above, for the same reason (never cause a rebuild
  // of the parent/the video tree).
  final ValueNotifier<bool> _debugOverlayVisibleNotifier = ValueNotifier(false);

  // Tier 1 track selector overlay (arrowRight — InputAction.trackSelector):
  // same isolated ValueNotifier pattern as above, for the same GL-texture
  // reason. See widgets/track_selector_overlay.dart.
  final ValueNotifier<bool> _trackPickerOpenNotifier = ValueNotifier(false);
  final GlobalKey<TrackSelectorOverlayState> _trackSelectorKey = GlobalKey<TrackSelectorOverlayState>();

  // The Enter KeyDownEvent that OPENS the EPG is handled outside the overlay
  // (by the switch below, with `_epgOpenNotifier.value` still false); the
  // overlay only exists/mounts AFTER that. The KeyUpEvent of that SAME key
  // press, however, arrives with `_epgOpenNotifier.value` already true — and
  // without a flag it would be forwarded to the overlay's
  // `handleSelectKeyEvent` as if it were a complete tap (down+up) on it,
  // without the overlay ever having seen the down. `_longPressFired` is false
  // by default => it becomes a "short tap" => it selects the focused item (the
  // channel already live) => it closes the EPG right as it opens (the EPG
  // "flashes" and closes). This flag makes sure that first orphan KeyUp is
  // swallowed, not forwarded.
  bool _suppressNextEpgSelectUp = false;

  // Rolling EPG window: cursor of the `endAt` of the last EpgEntry already
  // incorporated into `channel` in this load generation — where to continue
  // fetching the next window from. Reset on every new _loadAndPlayChannel.
  DateTime? _epgLoadedUntil;
  // Coalesces concurrent extension fetches: the proactive trigger
  // (_maybeExtendGrid) and the immediate attempt/retry for the dead end in
  // _crossfadeTo can fire almost together. Without this, two simultaneous
  // fetches would read the same _epgLoadedUntil and each would append the same
  // items to `channel` again — see _fetchNextEpgSlice.
  Future<bool>? _pendingExtend;
  // Only exists while the materialized schedule has really run out (a real
  // gap, not just the end of the fetched window) — repeats the fetch until
  // something turns up in that gap and playback resumes on its own.
  Timer? _gapRetryTimer;
  // Only exists while holding on the slate for a KNOWN interior schedule gap
  // (the next item is already materialized, just not due yet — see
  // _holdForGap). Distinct from _gapRetryTimer above, which is for when
  // nothing at all is materialized yet.
  Timer? _gapHoldTimer;
  // The real next item's controller, preloaded and waiting out a gap hold,
  // between _holdForGap's first crossfade (into the slate) and its second
  // one (_gapHoldTimer firing). Deliberately NOT in _controllerA/_controllerB
  // during that window (the slate occupies the only free slot) — so a
  // channel switch's generic staleA/staleB teardown can't reach it. Tracked
  // here instead purely so that teardown can dispose it too; cleared as soon
  // as the second crossfade picks it back up.
  VideoPlayerController? _gapHoldPending;

  late final PlayheadApi _api;
  List<PlayheadChannel> _channels = [];
  int _channelIndex = 0;
  // Incremented on every channel switch; captured by closure in every
  // scheduled preload/crossfade/schedule load. If the value changed by the
  // time one of those callbacks finally runs, it's left over from a channel
  // that already left the scene — abort instead of applying the result on top
  // of the new channel's state.
  int _loadGeneration = 0;
  // Whether _loadAndPlayChannel is currently doing real work (network fetch
  // + controller create) for the current generation. See _switchToChannelIndex.
  bool _channelLoadInFlight = false;
  // The most recent target a channel switch asked for while a real load was
  // already in flight — resolved by _runChannelLoad right after that load
  // finishes (or self-aborts via _loadGeneration). See _switchToChannelIndex.
  int? _pendingChannelSwitchTarget;

  // Direct jump by number: digits accumulated while the user types a channel
  // number. While non-null/non-empty, the "Channel: NN_" overlay appears in
  // the top-right corner. `_channelInputCommitTimer` resolves the buffer on
  // its own ~1.5s after the last digit (real remote-control debounce);
  // reaching `channelInputMaxDigits` resolves immediately. Any successful
  // channel switch clears both — see `_switchToChannelIndex`.
  String? _channelInputBuffer;
  Timer? _channelInputCommitTimer;

  late final SettingsService _settings;
  late final KeyBindingsService _keyBindings;
  late final StreamPreferencesService _streamPrefs;
  // Guards dispose() against the early-return error path in initState (bad
  // --dart-define config): _streamPrefs is only ever assigned inside
  // _bootstrapChannels, which that path never reaches.
  bool _streamPrefsReady = false;
  // Tier 3 (channel admin) rules for the currently loaded channel, resolved
  // by _loadAndPlayChannel (already async / already doing network I/O)
  // ahead of _createController, which only ever reads this field
  // synchronously — see StreamPreferencesService's doc comment for why no
  // network call happens on the playback hot path.
  StreamRuleCascade _currentChannelRules = StreamRuleCascade.empty;

  @override
  void initState() {
    super.initState();
    _osdVisibleNotifier.addListener(() {
      if (!_osdVisibleNotifier.value) {
        _osdFocusNotifier.value = false;
        _osdArmed = false;
      }
    });
    if (AppConfig.jellyfinUrl.isEmpty || AppConfig.jellyfinApiKey.isEmpty || AppConfig.playheadBaseUrl.isEmpty) {
      _status = 'ERROR: run with --dart-define=JELLYFIN_URL=... --dart-define=JELLYFIN_API_KEY=... '
          '--dart-define=PLAYHEADTV_BASE_URL=...';
      return;
    }
    _api = PlayheadApi(
      baseUrl: AppConfig.playheadBaseUrl,
      deviceToken: AppConfig.playheadDeviceToken.isEmpty ? null : AppConfig.playheadDeviceToken,
    );
    _bootstrapChannels();
  }

  // Fetches the channel list exactly once (it needs the whole list, not just
  // the initial channel, to know what to switch to with arrow up/down),
  // resolves the initial channel (dart-define or the one with the lowest
  // `number`) and loads its schedule.
  Future<void> _bootstrapChannels() async {
    setState(() => _status = 'loading channels…');
    try {
      _settings = await SettingsService.create();
      _keyBindings = KeyBindingsService(_settings);
      _streamPrefs = await StreamPreferencesService.create(_api);
      _streamPrefsReady = true;
      // Initial fetch at boot, concurrently with the channel list (both are
      // independent network calls) — awaited before the first
      // _loadAndPlayChannel below, so the very first playback decision
      // already has a populated cache instead of racing it.
      final streamPrefsRefresh = _streamPrefs.refreshAll();
      final channels = await _api.listChannels();
      await streamPrefsRefresh;
      _streamPrefs.startPolling();
      if (channels.isEmpty) {
        if (!mounted) return;
        setState(() => _status = 'ERROR: no channels configured on the backend (GET /channels is empty)');
        return;
      }
      final sorted = [...channels]..sort((a, b) => a.number.compareTo(b.number));
      var index = 0;
      final wantedId = widget.initialChannelId ??
          (AppConfig.playheadChannelIdDefine.isNotEmpty ? AppConfig.playheadChannelIdDefine : null);
      if (wantedId != null) {
        final found = sorted.indexWhere((c) => c.id == wantedId);
        if (found >= 0) index = found;
      }
      if (!mounted) return;
      setState(() {
        _channels = sorted;
        _channelIndex = index;
      });
      await _loadAndPlayChannel(sorted[index], generation: _loadGeneration);
      unawaited(_settings.recordWatched(sorted[index].id));
    } catch (e) {
      if (!mounted) return;
      setState(() => _status = 'ERROR: failed to load channel list: $e');
    }
  }

  // Arrow up = next `number`, arrow down = previous, wrapping around the list.
  // Only resolves the relative target index — the core (cancel everything of
  // the current channel + load the target index) is shared with the direct
  // jump by typed number, see `_switchToChannelIndex`.
  Future<void> _switchChannel(int delta) async {
    if (_channels.length < 2) return;
    final next = (_channelIndex + delta) % _channels.length;
    final normalized = next < 0 ? next + _channels.length : next;
    await _switchToChannelIndex(normalized);
  }

  // Common core of any channel switch, whether by arrow (relative index
  // already resolved by `_switchChannel`) or by typed number (index already
  // resolved by `_commitChannelInput`): cancels everything of the current
  // channel (timers + both controllers, active and preloading), clears the
  // digit buffer (no typing should carry over into the new channel) and
  // fetches the target channel's schedule.
  Future<void> _switchToChannelIndex(int targetIndex) async {
    final target = _channels[targetIndex];
    unawaited(_settings.recordWatched(target.id));

    final generation = ++_loadGeneration;
    _advanceTimer?.cancel();
    _volumeRampTimer?.cancel();
    _osdHideTimer?.cancel();
    _osdHideTimer = null;
    _gapRetryTimer?.cancel();
    _gapRetryTimer = null;
    _gapHoldTimer?.cancel();
    _gapHoldTimer = null;
    // See _gapHoldPending's doc comment: a gap hold in progress parks the
    // real next item's controller outside _controllerA/_controllerB, so
    // cancelling the timer above would otherwise leak it.
    if (_gapHoldPending != null) {
      unawaited(serializeGlOp(() => _gapHoldPending!.dispose()));
      _gapHoldPending = null;
    }
    _pendingExtend = null;
    _channelInputCommitTimer?.cancel();
    _channelInputCommitTimer = null;
    // Fire-and-forget, but routed through serializeGlOp (see its doc
    // comment): this generation's own execution doesn't stop here to wait
    // for the native teardown, and _loadAndPlayChannel below can create a
    // brand new controller before it's done — without the shared queue,
    // that create() could run on fvp/libmdk's raster thread while this
    // dispose() is still freeing GL resources on its dispose thread.
    final staleA = _controllerA;
    final staleB = _controllerB;
    if (staleA != null) unawaited(serializeGlOp(() => staleA.dispose()));
    if (staleB != null) unawaited(serializeGlOp(() => staleB.dispose()));
    _osdVisibleNotifier.value = false;
    setState(() {
      _channelIndex = targetIndex;
      _controllerA = null;
      _controllerB = null;
      _aIsActive = true;
      _opacityA = 1;
      _opacityB = 0;
      _currentIndex = -1;
      channel = <ChannelEntry>[];
      _epgLoadedUntil = null;
      _channelInputBuffer = null;
      _status = 'switching channel…';
    });

    if (_channelLoadInFlight) {
      // A previous switch's real load (network + controller create) hasn't
      // finished yet — starting a second one now is exactly the
      // rapid-succession case that wastes a full GET /epg + controller
      // create per keypress (immediately discarded by the next press's own
      // generation bump) and made the GL-serialization race easy to hit in
      // the first place (see gl_serial.dart). The visual state above
      // already updated instantly regardless — only the real load is
      // deferred, to whichever target the user last asked for once the
      // in-flight one finishes or self-aborts.
      _pendingChannelSwitchTarget = targetIndex;
      return;
    }
    await _runChannelLoad(target, generation);
  }

  // Runs the real (network + controller) load for a channel switch, then
  // immediately re-dispatches to the last target requested while it was
  // running, if any — see the `_channelLoadInFlight` branch above. No
  // `await` sits between clearing `_channelLoadInFlight` and reading/
  // clearing `_pendingChannelSwitchTarget`, so a press that arrives in that
  // gap can only ever be the one this function itself is about to act on
  // (Dart's single-threaded event loop doesn't interleave in the middle of
  // synchronous code) — nothing gets lost or double-fired.
  Future<void> _runChannelLoad(PlayheadChannel target, int generation) async {
    _channelLoadInFlight = true;
    await _loadAndPlayChannel(target, generation: generation);
    _channelLoadInFlight = false;
    final pending = _pendingChannelSwitchTarget;
    _pendingChannelSwitchTarget = null;
    if (pending != null) {
      unawaited(_switchToChannelIndex(pending));
    }
  }

  // Fetches the materialized schedule (GET /epg) in an epgWindow-sized window
  // ahead of "now" for the given channel, resolves each catalog_item to its
  // native Jellyfin ID, and only then starts playback. The client never
  // decides WHAT plays — it only fetches what the backend already decided and
  // resolves/plays it (see AGENTS.md). `generation` is checked before every
  // `setState` that follows an `await`, so it doesn't trample a channel that
  // was switched again in the meantime.
  Future<void> _loadAndPlayChannel(PlayheadChannel target, {required int generation}) async {
    try {
      final now = DateTime.now().toUtc();
      // Kicked off in parallel with GET /epg + GET /catalog-items/{id}
      // below (never awaited until just before it's needed) so resolving
      // tier 3 (channel admin) rules never adds latency to the channel-load
      // path on top of the network calls it already has to make — it just
      // rides alongside them. _createController itself never awaits
      // anything for this; see _currentChannelRules' doc comment.
      final channelRulesFuture = _streamPrefs.ensureChannelRules(target.id);
      final entries = await _api.getEpg(target.id, now, now.add(epgWindow));
      if (generation != _loadGeneration) return;

      // Drops whatever already ended (can only happen because of some
      // clock/materialization slack, but safer than assuming it never
      // occurs) and anything with a non-positive duration (a corrupt/unprobed
      // source item — pullsync's own guard should already reject these at
      // sync time, but _scheduleTransitions fires its next-item timer
      // immediately for a zero-length clip, so a single one that slips
      // through would churn real controller create/dispose cycles until the
      // process runs out of file descriptors — reproduced 2026-09-30, see
      // AGENTS.md).
      final upcoming = entries.where((e) => e.endAt.isAfter(now) && e.endAt.isAfter(e.startAt)).toList();
      if (upcoming.isEmpty) {
        if (!mounted) return;
        setState(() => _status = 'ERROR: nothing scheduled in the next ${epgWindow.inHours}h for channel ${target.number}');
        return;
      }

      final catalogItems = await Future.wait(upcoming.map((e) => _api.getCatalogItem(e.catalogItemId)));
      if (generation != _loadGeneration) return;

      _currentChannelRules = await channelRulesFuture;
      if (generation != _loadGeneration) return;

      final loaded = <ChannelEntry>[
        for (var i = 0; i < upcoming.length; i++)
          ChannelEntry(
            catalogItems[i].externalId,
            catalogItems[i].title,
            upcoming[i].endAt.difference(upcoming[i].startAt),
            upcoming[i].startAt,
          ),
      ];

      // "TV turned on" joins the middle of the first program if it already
      // started, rather than going back to its beginning — only the first
      // item gets this seek.
      var initialSeek = now.difference(upcoming.first.startAt);
      if (initialSeek < Duration.zero) initialSeek = Duration.zero;
      if (initialSeek > loaded.first.clipDuration) initialSeek = loaded.first.clipDuration;

      // Initial rolling-window cursor: the largest endAt among what was just
      // loaded — the next extension fetches from there onward.
      var loadedUntil = now;
      for (final e in upcoming) {
        if (e.endAt.isAfter(loadedUntil)) loadedUntil = e.endAt;
      }

      if (!mounted || generation != _loadGeneration) return;
      setState(() {
        channel = loaded;
        _epgLoadedUntil = loadedUntil;
      });
      // Separate try/catch from the backend fetch above: _startChannel opens
      // the actual video stream against Jellyfin (controller.initialize()),
      // a completely different failure mode (Jellyfin unreachable, item
      // deleted, unsupported codec) from "the backend didn't answer" —
      // reporting both under one generic "failed to load schedule from
      // backend" message sent whoever's reading it chasing the wrong system
      // (reproduced 2026-09-30: Jellyfin was down, and the error read as a
      // backend/schedule problem instead).
      try {
        await _startChannel(initialSeek: initialSeek, generation: generation);
      } catch (e) {
        if (!mounted || generation != _loadGeneration) return;
        setState(() => _status = 'ERROR: could not start playback — is Jellyfin reachable? ($e)');
      }
    } catch (e) {
      if (!mounted || generation != _loadGeneration) return;
      setState(() => _status = 'ERROR: failed to load schedule from backend: $e');
    }
  }

  Future<VideoPlayerController> _createController(ChannelEntry entry, {Duration? seekTo}) async {
    final controller = VideoPlayerController.networkUrl(Uri.parse(streamUrl(entry.id)));
    await serializeGlOp(() => controller.initialize());
    if (seekTo != null && seekTo > Duration.zero) {
      // Only the first item of the schedule (the "TV turned on" in the middle
      // of a program) uses this — preload/crossfade of the following items
      // always starts from zero.
      await controller.seekTo(seekTo);
    }
    // Resolve the stream (audio/subtitle) selection cascade (tiers 2-4 —
    // see services/stream_selection_resolver.dart and AGENTS.md "Client app
    // structure") against this item's real tracks. Track candidates are
    // built from the SAME list position setAudioTracks/setSubtitleTracks
    // expect (the mdk API takes a 0-based index within that media type, not
    // StreamInfo.index, which is the demuxer's absolute stream number) —
    // see debug_overlay.dart for another read of this same getMediaInfo()
    // data. `selection.audioTracks == null` (no cascade step matched
    // anything, across every tier) leaves the player's own default alone;
    // `selection.subtitleTracks` is never null — an empty list is what
    // disables fvp/libmdk's default-enabled first MKV subtitle track when
    // no step matched, same as the unconditional setSubtitleTracks([]) this
    // replaced.
    final info = controller.getMediaInfo();
    final audioStreams = info?.audio ?? const [];
    final subtitleStreams = info?.subtitle ?? const [];
    // Tier 1 (client-local override, see SettingsService's doc comment and
    // TierOneOverride): looked up by the CURRENT channel id — safe to read
    // here because _createController is only ever called for items of the
    // channel already loaded into _channelIndex (preload/crossfade never
    // spans a channel switch).
    final channelId = (_channelIndex >= 0 && _channelIndex < _channels.length) ? _channels[_channelIndex].id : null;
    final tierOneOverride = channelId == null
        ? null
        : TierOneOverride(
            audioIndex: _settings.getAudioTrackOverride(channelId),
            subtitleIndex: _settings.getSubtitleTrackOverride(channelId),
          );
    final selection = StreamSelectionResolver.resolve(
      audioTracks: [
        for (var i = 0; i < audioStreams.length; i++)
          TrackCandidate(
            index: i,
            language: _metadataValue(audioStreams[i].metadata, 'language'),
            title: _metadataValue(audioStreams[i].metadata, 'title'),
          ),
      ],
      subtitleTracks: [
        for (var i = 0; i < subtitleStreams.length; i++)
          TrackCandidate(
            index: i,
            language: _metadataValue(subtitleStreams[i].metadata, 'language'),
            title: _metadataValue(subtitleStreams[i].metadata, 'title'),
          ),
      ],
      tierOneOverride: tierOneOverride,
      userRules: _streamPrefs.userRules,
      channelRules: _currentChannelRules,
      globalRules: _streamPrefs.globalRules,
    );
    if (selection.audioTracks != null) {
      controller.setAudioTracks(selection.audioTracks!);
    }
    controller.setSubtitleTracks(selection.subtitleTracks);
    // By default mdk syncs video frame presentation to the AUDIO clock — when
    // that sync stalls (same mechanism reported in wang-bin/mdk-sdk#179, root
    // cause confirmed by the maintainer: "bug comes from the audio
    // renderer"), the video freezes on the last frame while the audio, already
    // on its own queue, keeps playing normally. Confirmed by the debug overlay
    // showing a high, stable Flutter FPS during the freeze — it isn't
    // compositor jank, it's video presentation stuck in that sync.
    // 'avsync.audio'=0 decouples it and syncs to a stable clock instead of the
    // audio; the value must be "0"/"1" (not "false"/"true" — mdk calls stoi()
    // on the value and "false" kills the process with
    // std::invalid_argument).
    controller.setProperty('avsync.audio', '0');
    // Read-ahead window ("minMs+maxMs", mdk's setBufferRange): playback only
    // starts/resumes once `min` is buffered and the demuxer keeps reading up
    // to `max` — mdk's default is 1s..4s, too little for a source with
    // bursty latency (e.g. a network-mounted library), where each hiccup
    // shows as a stutter.
    controller.setProperty('buffer', '$playbackBufferMinMs+$playbackBufferMaxMs');
    return controller;
  }

  Future<void> _startChannel({Duration initialSeek = Duration.zero, required int generation}) async {
    final entry = channel[0];
    final controller = await _createController(entry, seekTo: initialSeek);
    if (generation != _loadGeneration) {
      // Switched channel again while the controller was initializing/seeking.
      // See serializeGlOp's doc comment for why this fire-and-forget dispose
      // still has to go through the shared queue.
      unawaited(serializeGlOp(() => controller.dispose()));
      return;
    }
    await controller.setVolume(1);
    await controller.play();
    if (!mounted) return;
    setState(() {
      _controllerA = controller;
      _currentIndex = 0;
      _status = 'playing: ${entry.label}';
    });
    _showOsd();
    // If we joined in the middle of the program (initialSeek > 0), only
    // clipDuration - initialSeek of real playback remains — scheduling by the
    // full clipDuration would leave the video parked on the last frame for
    // initialSeek before the crossfade fires.
    _scheduleTransitions(0, remaining: entry.clipDuration - initialSeek, generation: generation);
    unawaited(_maybeExtendGrid(0, generation));
  }

  // Schedules the preload of the next item BEFORE the end of the current clip
  // (the heart of the design: the next one can be ready/buffered without
  // interrupting what is already playing) and the crossfade trigger at the end
  // of the clip. `remaining` overrides the duration used for scheduling when
  // the item started playing from a non-zero point (see _startChannel); all
  // other items always play from zero, so they use clipDuration.
  // `generation` travels with the Timer so _preloadNext/_crossfadeTo can
  // detect whether the channel changed again before firing.
  void _scheduleTransitions(int index, {Duration? remaining, required int generation}) {
    final entry = channel[index];
    var effective = remaining ?? entry.clipDuration;
    if (effective < minTransitionInterval) effective = minTransitionInterval;
    final preloadAt = effective - preloadLead;
    Timer(preloadAt.isNegative ? Duration.zero : preloadAt, () => _preloadNext(index + 1, generation));

    _advanceTimer?.cancel();
    _advanceTimer = Timer(effective, () => _crossfadeTo(index + 1, generation));
  }

  Future<void> _preloadNext(int nextIndex, int generation) async {
    if (generation != _loadGeneration) return;
    if (nextIndex >= channel.length) return;
    final nextEntry = channel[nextIndex];
    setState(() => _status = 'preload: ${nextEntry.label}');

    final controller = await _createController(nextEntry);
    await controller.setVolume(0);
    if (!mounted || generation != _loadGeneration) {
      unawaited(serializeGlOp(() => controller.dispose()));
      return;
    }
    // Captured before setState so the old controller's dispose (fire-and
    // forget) is routed through serializeGlOp instead of racing this
    // function's own already-created controller.
    final replaced = _aIsActive ? _controllerB : _controllerA;
    setState(() {
      if (_aIsActive) {
        _controllerB = controller;
      } else {
        _controllerA = controller;
      }
    });
    if (replaced != null) unawaited(serializeGlOp(() => replaced.dispose()));
  }

  Future<void> _crossfadeTo(int nextIndex, int generation) async {
    if (generation != _loadGeneration) return;
    if (nextIndex >= channel.length) {
      // The fetched window ran out before the extension trigger
      // (_maybeExtendGrid) could finish in time — try an immediate fetch of
      // the next slice right here before giving up.
      final extended = await _fetchNextEpgSlice(generation);
      if (generation != _loadGeneration) return;
      if (extended && nextIndex < channel.length) {
        // There's no preload ready for the newly arrived item (there wasn't
        // time) — resume with a hard cut instead of trying a crossfade with
        // no incoming controller.
        await _resumeAt(nextIndex, generation);
        return;
      }

      // Still empty: either a real gap in the materialized schedule, or the
      // fetch failed. Don't hang forever — show a status and retry
      // periodically until something shows up, resuming on its own.
      if (!mounted) return;
      setState(() => _status = 'ERROR: nothing more scheduled right now — retrying…');
      _gapRetryTimer?.cancel();
      _gapRetryTimer = Timer.periodic(epgGapRetryInterval, (t) async {
        if (generation != _loadGeneration) {
          t.cancel();
          return;
        }
        final ok = await _fetchNextEpgSlice(generation);
        if (generation != _loadGeneration) {
          t.cancel();
          return;
        }
        if (ok && nextIndex < channel.length) {
          t.cancel();
          _gapRetryTimer = null;
          await _resumeAt(nextIndex, generation);
        }
      });
      return;
    }
    final nextEntry = channel[nextIndex];
    final outgoing = _aIsActive ? _controllerA : _controllerB;
    final incoming = _aIsActive ? _controllerB : _controllerA;
    if (incoming == null) {
      setState(() => _status = 'ERROR: preload of ${nextEntry.label} did not finish in time');
      return;
    }

    // Honor the EPG clock (issue #9) instead of always playing back to back:
    // compare the next item's real, materialized startAt with wall-clock now.
    final gap = nextEntry.startAt.difference(DateTime.now().toUtc());
    if (gap > epgSyncTolerance) {
      // Interior schedule gap — hold on the slate instead of starting
      // `incoming` early and running ahead of the EPG clock for the rest of
      // the session.
      await _holdForGap(nextIndex: nextIndex, outgoing: outgoing, pending: incoming, gap: gap, generation: generation);
      return;
    }
    if (gap < -epgSyncTolerance) {
      // Running behind the EPG clock (e.g. a slow preload) — resync by
      // seeking into the item instead of starting it from zero, which would
      // only let the drift accumulate further on every following item.
      var seek = -gap;
      if (seek > nextEntry.clipDuration) seek = nextEntry.clipDuration;
      await incoming.seekTo(seek);
      if (generation != _loadGeneration) return;
    }

    setState(() => _status = 'CROSSFADE -> ${nextEntry.label}');
    await _rampCrossfade(outgoing: outgoing, incoming: incoming, generation: generation);
    if (generation != _loadGeneration) return;
    if (!mounted) return;
    setState(() {
      _currentIndex = nextIndex;
      _status = 'playing: ${nextEntry.label}';
    });
    _showOsd();
    _scheduleTransitions(nextIndex, generation: generation);
    unawaited(_maybeExtendGrid(nextIndex, generation));
  }

  // Manual crossfade of video (opacity) + audio (volume, 0.0-1.0) between
  // `outgoing` (disposed once fully faded out) and `incoming` (already
  // sitting in whichever of _controllerA/_controllerB is NOT currently
  // active, per the same convention _preloadNext uses), in ~30 steps over
  // crossfadeDuration. Swaps _aIsActive once done. Shared by the normal
  // item-to-item crossfade in _crossfadeTo and both legs of a gap hold
  // (_holdForGap) — same ramp, different "what did we just fade into".
  // Doesn't touch _currentIndex/_status/scheduling: what "arriving" means
  // differs between a real item and the gap slate, so the caller handles
  // that itself once this completes.
  Future<void> _rampCrossfade({
    required VideoPlayerController? outgoing,
    required VideoPlayerController incoming,
    required int generation,
  }) async {
    await incoming.play();
    const steps = 30;
    final stepDuration = crossfadeDuration ~/ steps;
    final completer = Completer<void>();
    _volumeRampTimer?.cancel();
    var step = 0;
    _volumeRampTimer = Timer.periodic(stepDuration, (t) async {
      if (generation != _loadGeneration) {
        t.cancel();
        if (!completer.isCompleted) completer.complete();
        return;
      }
      step++;
      final progress = (step / steps).clamp(0.0, 1.0);
      setState(() {
        if (_aIsActive) {
          _opacityA = 1 - progress;
          _opacityB = progress;
        } else {
          _opacityB = 1 - progress;
          _opacityA = progress;
        }
      });
      await outgoing?.setVolume(1 - progress);
      await incoming.setVolume(progress);
      if (step >= steps) {
        t.cancel();
        await outgoing?.pause();
        if (outgoing != null) await serializeGlOp(() => outgoing.dispose());
        if (_aIsActive) {
          _controllerA = null;
        } else {
          _controllerB = null;
        }
        _aIsActive = !_aIsActive;
        if (!completer.isCompleted) completer.complete();
      }
    });
    return completer.future;
  }

  Future<VideoPlayerController> _createSlateController() async {
    final controller = VideoPlayerController.asset(slateAssetPath);
    await serializeGlOp(() => controller.initialize());
    await controller.setLooping(true);
    return controller;
  }

  // Interior EPG gap (issue #9): `outgoing` (the item that just finished)
  // crossfades into a looping local slate instead of straight into `pending`
  // (the real next item, already preloaded by the normal preload timer —
  // which fires a fixed `preloadLead` before the CURRENT item ends,
  // independent of the next one's real startAt, so it's always ready here
  // even though it won't be shown yet). Once `gap` elapses, a second
  // crossfade brings `pending` on screen and resumes the normal
  // schedule/preload/extend machinery from `nextIndex`, as if there had been
  // no gap at all.
  //
  // Known trade-off, not addressed here: `pending`'s own network connection
  // to Jellyfin stays open and idle for the whole hold, however long the gap
  // is — fine for the gap widths seen in practice (materializer tail
  // padding/dynamic pool misses, see issue #6), but a very long schedule
  // hole could in principle let the connection stall or time out server-side
  // before playback resumes. Revisit (defer `pending`'s creation until near
  // the end of the hold) if that turns out to matter.
  Future<void> _holdForGap({
    required int nextIndex,
    required VideoPlayerController? outgoing,
    required VideoPlayerController pending,
    required Duration gap,
    required int generation,
  }) async {
    final slate = await _createSlateController();
    if (generation != _loadGeneration) {
      unawaited(serializeGlOp(() => slate.dispose()));
      return;
    }
    setState(() {
      if (_aIsActive) {
        _controllerB = slate;
      } else {
        _controllerA = slate;
      }
      _status = 'HOLD — next program starts shortly';
    });
    await _rampCrossfade(outgoing: outgoing, incoming: slate, generation: generation);
    if (generation != _loadGeneration) {
      unawaited(serializeGlOp(() => pending.dispose()));
      return;
    }

    _gapHoldTimer?.cancel();
    _gapHoldPending = pending;
    _gapHoldTimer = Timer(gap, () async {
      _gapHoldPending = null;
      if (generation != _loadGeneration) {
        unawaited(serializeGlOp(() => pending.dispose()));
        return;
      }
      final nextEntry = channel[nextIndex];
      final slateOutgoing = _aIsActive ? _controllerA : _controllerB;
      setState(() {
        if (_aIsActive) {
          _controllerB = pending;
        } else {
          _controllerA = pending;
        }
        _status = 'CROSSFADE -> ${nextEntry.label}';
      });
      await _rampCrossfade(outgoing: slateOutgoing, incoming: pending, generation: generation);
      if (generation != _loadGeneration) return;
      if (!mounted) return;
      setState(() {
        _currentIndex = nextIndex;
        _status = 'playing: ${nextEntry.label}';
      });
      _showOsd();
      _scheduleTransitions(nextIndex, generation: generation);
      unawaited(_maybeExtendGrid(nextIndex, generation));
    });
  }

  // Resumes playback from `index` with a hard cut (no crossfade — there's no
  // preloaded controller to fade from, since we got here from a gap/end of
  // window). Used both by the immediate extension and by the periodic retry in
  // _crossfadeTo.
  Future<void> _resumeAt(int index, int generation) async {
    if (generation != _loadGeneration) return;
    if (index >= channel.length) return;
    final entry = channel[index];
    final controller = await _createController(entry);
    if (generation != _loadGeneration) {
      unawaited(serializeGlOp(() => controller.dispose()));
      return;
    }
    await controller.setVolume(1);
    await controller.play();
    if (!mounted) return;
    final staleA = _controllerA;
    final staleB = _controllerB;
    if (staleA != null) unawaited(serializeGlOp(() => staleA.dispose()));
    if (staleB != null) unawaited(serializeGlOp(() => staleB.dispose()));
    setState(() {
      _controllerA = controller;
      _controllerB = null;
      _aIsActive = true;
      _opacityA = 1;
      _opacityB = 0;
      _currentIndex = index;
      _status = 'playing: ${entry.label}';
    });
    _showOsd();
    _scheduleTransitions(index, generation: generation);
    unawaited(_maybeExtendGrid(index, generation));
  }

  // Rolling-window trigger: if the loaded "runway" ahead of the current item
  // (sum of clipDuration from `fromIndex` onward) falls below
  // epgExtendThreshold, fetches the next window in the background and appends
  // it to the end of `channel` — never replaces the list nor touches the
  // indexes already in use by scheduled timers/controllers.
  Future<void> _maybeExtendGrid(int fromIndex, int generation) async {
    if (generation != _loadGeneration) return;
    var remaining = Duration.zero;
    for (var i = fromIndex; i < channel.length; i++) {
      remaining += channel[i].clipDuration;
    }
    if (remaining >= epgExtendThreshold) return;
    await _fetchNextEpgSlice(generation);
  }

  // Fetches `GET /epg` from `_epgLoadedUntil` to `_epgLoadedUntil +
  // epgWindow`, resolves the catalog items and appends them to the end of
  // `channel`. Returns true only when new items actually entered the list. If
  // a fetch is already in flight (triggered by the proactive trigger or by a
  // concurrent call), returns the SAME Future instead of starting another —
  // two simultaneous fetches would read the same cursor and each would append
  // the same items to `channel`, duplicating content in the schedule.
  Future<bool> _fetchNextEpgSlice(int generation) {
    final pending = _pendingExtend;
    if (pending != null) return pending;
    final future = _doFetchNextEpgSlice(generation);
    _pendingExtend = future;
    future.whenComplete(() {
      if (identical(_pendingExtend, future)) _pendingExtend = null;
    });
    return future;
  }

  // Defensively filters out any EpgEntry with a startAt before the cursor
  // (avoids duplicating the last item already incorporated because of the
  // backend filter's inclusive overlap) and advances the cursor to the largest
  // endAt among the new items.
  Future<bool> _doFetchNextEpgSlice(int generation) async {
    if (generation != _loadGeneration) return false;
    final cursor = _epgLoadedUntil;
    if (cursor == null) return false;
    if (_channelIndex >= _channels.length) return false;
    final channelId = _channels[_channelIndex].id;

    try {
      final entries = await _api.getEpg(channelId, cursor, cursor.add(epgWindow));
      if (generation != _loadGeneration) return false;

      // See the matching filter in _loadAndPlayChannel for why non-positive
      // duration entries are dropped here too.
      final fresh = entries.where((e) => !e.startAt.isBefore(cursor) && e.endAt.isAfter(e.startAt)).toList();
      if (fresh.isEmpty) return false;

      final catalogItems = await Future.wait(fresh.map((e) => _api.getCatalogItem(e.catalogItemId)));
      if (generation != _loadGeneration) return false;

      final newItems = <ChannelEntry>[
        for (var i = 0; i < fresh.length; i++)
          ChannelEntry(
            catalogItems[i].externalId,
            catalogItems[i].title,
            fresh[i].endAt.difference(fresh[i].startAt),
            fresh[i].startAt,
          ),
      ];
      var newLoadedUntil = cursor;
      for (final e in fresh) {
        if (e.endAt.isAfter(newLoadedUntil)) newLoadedUntil = e.endAt;
      }

      if (!mounted || generation != _loadGeneration) return false;
      setState(() {
        channel = [...channel, ...newItems];
        _epgLoadedUntil = newLoadedUntil;
      });
      return true;
    } catch (_) {
      // A network/parsing failure in a background extension must not bring
      // down the playback in progress — the caller (_maybeExtendGrid or the
      // retry in _crossfadeTo) tries again later.
      return false;
    }
  }

  // Shows the OSD (or restarts the countdown if it was already visible). The
  // progress bar ticker lives inside `ChannelOsd` — this only controls
  // visibility/auto-hide. Called at channel start (_startChannel) and at the
  // end of each crossfade (_crossfadeTo) — never at the start of the
  // crossfade, only once the new item is officially the current one.
  void _showOsd() {
    _osdHideTimer?.cancel();
    _osdVisibleNotifier.value = true;
    _osdHideTimer = Timer(const Duration(seconds: 5), () {
      if (mounted) _osdVisibleNotifier.value = false;
    });
  }

  // Keys `0`-`9`: accumulate into the channel typing buffer. Each new digit
  // restarts the ~1.5s debounce (`channelInputCommitDelay`); reaching
  // `channelInputMaxDigits` resolves immediately without waiting for the
  // timeout — same pattern as a real remote control (a commercial TV also
  // commits early on hitting the digit limit of the highest configured
  // channel).
  void _onDigitKey(String digit) {
    _channelInputCommitTimer?.cancel();
    final buffer = (_channelInputBuffer ?? '') + digit;
    setState(() => _channelInputBuffer = buffer);
    if (buffer.length >= channelInputMaxDigits) {
      _commitChannelInput();
    } else {
      _channelInputCommitTimer = Timer(channelInputCommitDelay, _commitChannelInput);
    }
  }

  // Resolves the accumulated buffer: looks in `_channels` for a channel whose
  // `number` matches the typed integer exactly. If found (and it isn't already
  // the current channel — reloading the channel already on screen would just
  // be pointless flicker, same reason `_switchChannel` already refuses to
  // switch with a single configured channel), switches channel via
  // `_switchToChannelIndex` (which in turn clears the buffer again — see
  // there). If not found, clears the buffer right here and shows the error
  // feedback by reusing the existing "ERROR..." `_status` banner, without
  // blocking anything.
  void _commitChannelInput() {
    _channelInputCommitTimer?.cancel();
    _channelInputCommitTimer = null;
    final buffer = _channelInputBuffer;
    if (buffer == null || buffer.isEmpty) return;
    final number = int.tryParse(buffer);
    final index = number == null ? -1 : _channels.indexWhere((c) => c.number == number);
    if (index < 0) {
      setState(() {
        _channelInputBuffer = null;
        _status = 'ERROR: channel $buffer not found';
      });
      return;
    }
    setState(() => _channelInputBuffer = null);
    if (index == _channelIndex) return;
    unawaited(_switchToChannelIndex(index));
  }

  // Human-readable label for a track option in the picker/toast: prefers the
  // "title" metadata tag (e.g. "Director's Commentary"), falls back to
  // "language" (e.g. "por"), falls back to a positional placeholder — same
  // metadata read _createController already does for the tier 2-4 cascade
  // (see _metadataValue).
  String _trackLabel(Map<String, String> metadata, int index) {
    final title = _metadataValue(metadata, 'title');
    if (title != null && title.isNotEmpty) return title;
    final language = _metadataValue(metadata, 'language');
    if (language != null && language.isNotEmpty) return language;
    return 'Track ${index + 1}';
  }

  void _showTrackToast(String message) {
    final messenger = ScaffoldMessenger.maybeOf(context);
    messenger?.showSnackBar(
      SnackBar(key: const Key('track-toast'), content: Text(message), duration: const Duration(seconds: 2)),
    );
  }

  // Applies a tier 1 audio choice live on the CURRENTLY PLAYING controller
  // (no channel reload needed) and persists it, keyed by channel id, so
  // revisiting this channel later re-applies it (StreamSelectionResolver's
  // TierOneOverride, consulted by _createController). Shared by both the
  // picker (TrackSelectorOverlay.onSelect) and the cycleAudioTrack
  // accelerator (_cycleTrack) — the one convergence point for tier 1 writes.
  void _applyAudioTrack(VideoPlayerController controller, String channelId, int index, String label) {
    controller.setAudioTracks([index]);
    unawaited(_settings.setAudioTrackOverride(channelId, index));
    _showTrackToast('Audio: $label');
  }

  // Same as _applyAudioTrack, for subtitles. `index == -1` is the picker's
  // explicit "None" entry — setSubtitleTracks([]) disables subtitles, and
  // -1 is the sentinel TierOneOverride.subtitleIndex/StreamSelectionResolver
  // already understand as "explicit no subtitle" (see their doc comments).
  void _applySubtitleTrack(VideoPlayerController controller, String channelId, int index, String label) {
    controller.setSubtitleTracks(index == -1 ? const [] : [index]);
    unawaited(_settings.setSubtitleTrackOverride(channelId, index));
    _showTrackToast('Subtitle: $label');
  }

  TrackSelectorPanel _trackPickerInitialPanel = TrackSelectorPanel.audio;

  bool _trackPickerSinglePanel = false;

  void _openTrackPicker(TrackSelectorPanel panel, {bool singlePanel = false}) {
    _trackPickerInitialPanel = panel;
    _trackPickerSinglePanel = singlePanel;
    _osdHideTimer?.cancel();
    _osdVisibleNotifier.value = false;
    _trackPickerOpenNotifier.value = true;
  }

  // Most recently watched channels other than the current one, for the OSD's
  // "Channels" section (SettingsService keeps the MRU list).
  List<OsdChannelShortcut> _recentChannelShortcuts() {
    final currentId = (_channelIndex >= 0 && _channelIndex < _channels.length) ? _channels[_channelIndex].id : null;
    return [
      for (final id in _settings.recentChannelIds)
        if (id != currentId)
          for (final c in _channels.where((c) => c.id == id))
            OsdChannelShortcut(id: c.id, number: c.number, name: c.name, artUrl: c.guideArtUrl),
    ];
  }

  void _openEpgFromOsd() {
    _osdHideTimer?.cancel();
    _osdVisibleNotifier.value = false;
    _suppressNextEpgSelectUp = true;
    _epgOpenNotifier.value = true;
  }

  // InputAction.cycleAudioTrack/cycleSubtitleTrack (Path 2, power-user
  // accelerators — no default binding, see input/input_action.dart):
  // advances to the next track in the REAL current track list (wrapping
  // around), applying/persisting/confirming through the exact same
  // _applyAudioTrack/_applySubtitleTrack the picker uses — one storage/
  // resolver mechanism for both paths, not two parallel ones.
  void _cycleTrack(TrackSelectorPanel panel) {
    final controller = _aIsActive ? _controllerA : _controllerB;
    if (controller == null || _channels.isEmpty || _channelIndex >= _channels.length) return;
    final info = controller.getMediaInfo();
    final channelId = _channels[_channelIndex].id;
    if (panel == TrackSelectorPanel.audio) {
      final tracks = info?.audio ?? const [];
      if (tracks.isEmpty) return;
      final current = _settings.getAudioTrackOverride(channelId);
      final currentIndex = (current != null && current >= 0 && current < tracks.length) ? current : -1;
      final nextIndex = (currentIndex + 1) % tracks.length;
      _applyAudioTrack(controller, channelId, nextIndex, _trackLabel(tracks[nextIndex].metadata, nextIndex));
    } else {
      final tracks = info?.subtitle ?? const [];
      // -1 (the virtual "None" entry) is always part of the cycle, same set
      // the picker's Subtitle panel offers.
      final options = <int>[-1, for (var i = 0; i < tracks.length; i++) i];
      if (options.length <= 1) return; // no real subtitle track to cycle to
      final current = _settings.getSubtitleTrackOverride(channelId) ?? -1;
      final currentPosition = options.indexOf(current);
      final nextPosition = ((currentPosition < 0 ? -1 : currentPosition) + 1) % options.length;
      final nextIndex = options[nextPosition];
      final label = nextIndex == -1 ? 'None' : _trackLabel(tracks[nextIndex].metadata, nextIndex);
      _applySubtitleTrack(controller, channelId, nextIndex, label);
    }
  }

  @override
  void dispose() {
    _advanceTimer?.cancel();
    _volumeRampTimer?.cancel();
    _osdHideTimer?.cancel();
    _osdVisibleNotifier.dispose();
    _osdFocusNotifier.dispose();
    _epgOpenNotifier.dispose();
    _debugOverlayVisibleNotifier.dispose();
    _trackPickerOpenNotifier.dispose();
    _gapRetryTimer?.cancel();
    _gapHoldTimer?.cancel();
    _channelInputCommitTimer?.cancel();
    if (_streamPrefsReady) _streamPrefs.dispose();
    // State.dispose() itself can't await, but still routes through the
    // shared queue so this doesn't race a create() the mosaic page (or a
    // still-in-flight generation) might be doing at the same moment.
    final finalA = _controllerA;
    final finalB = _controllerB;
    final finalPending = _gapHoldPending;
    if (finalA != null) unawaited(serializeGlOp(() => finalA.dispose()));
    if (finalB != null) unawaited(serializeGlOp(() => finalB.dispose()));
    if (finalPending != null) unawaited(serializeGlOp(() => finalPending.dispose()));
    _focusNode.dispose();
    super.dispose();
  }

  Widget _videoFor(VideoPlayerController? controller, double opacity) {
    if (controller == null || !controller.value.isInitialized) {
      return const SizedBox.shrink();
    }
    return Opacity(
      opacity: opacity,
      // BoxFit.contain: shows the whole frame (with bars, if the aspect ratio
      // doesn't match the window) instead of cropping edges like BoxFit.cover
      // did — cropping cut off embedded subtitles near the bottom edge.
      child: FittedBox(
        fit: BoxFit.contain,
        child: SizedBox(
          width: controller.value.size.width,
          height: controller.value.size.height,
          child: VideoPlayer(controller),
        ),
      ),
    );
  }

  // Rectangle (in Stack/full-screen coordinates) that the video thumbnail
  // occupies when the EPG is open — the SAME rectangle used by `EpgOverlay` to
  // reserve the "hole" in the darkened background (see
  // EpgOverlay.videoThumbnailRect) and never draw anything on top of the
  // thumbnail. Single source of truth, so the two places never go out of sync.
  Rect _videoThumbnailRect(Size screenSize) => Rect.fromLTWH(
        screenSize.width - epgThumbnailMargin - epgThumbnailWidth,
        epgThumbnailMargin,
        epgThumbnailWidth,
        epgThumbnailHeight,
      );

  @override
  Widget build(BuildContext context) {
    final channelLabel = _channels.isEmpty
        ? '(loading channels)'
        : 'Channel ${_channels[_channelIndex].number} — ${_channels[_channelIndex].name}';
    // While there's no loaded schedule (initial bootstrap, or right after
    // switching channel) the only visible feedback is the plain debug text —
    // the real OSD needs a valid `channel`/`_currentIndex`.
    final hasContent = channel.isNotEmpty && _currentIndex >= 0 && _currentIndex < channel.length;
    final showErrorBanner = hasContent && _status.startsWith('ERROR');
    return KeyboardListener(
      focusNode: _focusNode,
      autofocus: true,
      // With the EPG CLOSED: arrow up/down switches channel (remote-control
      // Channel Up/Down), `0`-`9` accumulate the direct jump by channel
      // number. `select` (Enter) has 2 stages: with the OSD hidden, it shows
      // the OSD; with the OSD already visible, it opens the full-screen EPG.
      // `back` (Esc) has no effect here unless it goes home (see below).
      //
      // With the EPG OPEN, the same key events are redirected to the
      // `EpgOverlayState` (via `_epgOverlayKey`) instead of going to the
      // channel dispatch above: RAW `arrowUp`/`arrowDown` (not remappable, so
      // Up is always visually up regardless of the channel-switch binding)
      // become ROW navigation (`moveRow`, Up = previous row), RAW
      // `arrowLeft`/`arrowRight` (same decision already made in `MosaicPage`
      // for the 2D grid) become COLUMN navigation (`moveColumn`), `select` delegates to the overlay's
      // long-press/short-tap (`handleSelectKeyEvent`, which also receives the
      // `KeyUpEvent` — the only screen in the app that needs it) and `back`
      // closes the EPG.
      //
      // KeyRepeatEvent is ignored right at the top for both modes — holding a
      // key must not repeat any of these actions; the EPG's long-press is
      // measured by a Timer inside the overlay itself, not by KeyRepeatEvent.
      onKeyEvent: (event) {
        if (event is KeyRepeatEvent) return;
        final action = _keyBindings.resolve(event.logicalKey);

        if (_epgOpenNotifier.value) {
          if (action == InputAction.back) {
            if (event is KeyDownEvent) _epgOpenNotifier.value = false;
            return;
          }
          if (action == InputAction.select) {
            if (_suppressNextEpgSelectUp && event is KeyUpEvent) {
              _suppressNextEpgSelectUp = false;
              return;
            }
            _epgOverlayKey.currentState?.handleSelectKeyEvent(event);
            return;
          }
          if (event is! KeyDownEvent) return;
          if (event.logicalKey == LogicalKeyboardKey.arrowDown) {
            _epgOverlayKey.currentState?.moveRow(1);
          } else if (event.logicalKey == LogicalKeyboardKey.arrowUp) {
            _epgOverlayKey.currentState?.moveRow(-1);
          } else if (event.logicalKey == LogicalKeyboardKey.arrowLeft) {
            _epgOverlayKey.currentState?.moveColumn(-1);
          } else if (event.logicalKey == LogicalKeyboardKey.arrowRight) {
            _epgOverlayKey.currentState?.moveColumn(1);
          }
          return;
        }

        // Tier 1 track picker (InputAction.trackSelector — arrowRight by
        // default): same override pattern as the EPG branch above — while
        // open, raw arrowUp/arrowDown (Up = previous option) become focus
        // movement WITHIN the active panel (captured, never a real channel
        // switch), raw
        // arrowLeft/arrowRight switch panel, `select` picks and `back`
        // closes without applying anything.
        if (_trackPickerOpenNotifier.value) {
          if (action == InputAction.back) {
            if (event is KeyDownEvent) _trackPickerOpenNotifier.value = false;
            return;
          }
          if (event is! KeyDownEvent) return;
          if (action == InputAction.select) {
            _trackSelectorKey.currentState?.selectFocused();
          } else if (event.logicalKey == LogicalKeyboardKey.arrowDown) {
            _trackSelectorKey.currentState?.moveFocus(1);
          } else if (event.logicalKey == LogicalKeyboardKey.arrowUp) {
            _trackSelectorKey.currentState?.moveFocus(-1);
          } else if (event.logicalKey == LogicalKeyboardKey.arrowLeft ||
              event.logicalKey == LogicalKeyboardKey.arrowRight) {
            _trackSelectorKey.currentState?.switchPanel();
          }
          return;
        }

        if (event is! KeyDownEvent) return;
        final digit = action?.digitValue;
        if (digit != null) {
          _onDigitKey(digit);
          return;
        }

        // OSD focus mode: raw arrows (never remappable, same as the other 2D
        // navigations) move focus across the OSD controls, Select activates
        // the focused one and Back hides the OSD. Every key press restarts
        // the auto-hide countdown.
        final key = event.logicalKey;
        final isArrow = key == LogicalKeyboardKey.arrowUp ||
            key == LogicalKeyboardKey.arrowDown ||
            key == LogicalKeyboardKey.arrowLeft ||
            key == LogicalKeyboardKey.arrowRight;
        // Select opened the OSD and an arrow follows: enter focus mode (the
        // arrow itself is applied below as the first focus move).
        if (_osdVisibleNotifier.value && _osdArmed && !_osdFocusNotifier.value && isArrow) {
          _osdFocusNotifier.value = true;
        }
        if (_osdVisibleNotifier.value && _osdFocusNotifier.value) {
          if (key == LogicalKeyboardKey.arrowLeft) {
            _osdKey.currentState?.move(-1, 0);
          } else if (key == LogicalKeyboardKey.arrowRight) {
            _osdKey.currentState?.move(1, 0);
          } else if (key == LogicalKeyboardKey.arrowUp) {
            _osdKey.currentState?.move(0, -1);
          } else if (key == LogicalKeyboardKey.arrowDown) {
            _osdKey.currentState?.move(0, 1);
          } else if (action == InputAction.select) {
            _osdKey.currentState?.activateFocused();
          } else if (action == InputAction.back) {
            _osdHideTimer?.cancel();
            _osdVisibleNotifier.value = false;
            return;
          } else {
            return;
          }
          if (_osdVisibleNotifier.value) _showOsd();
          return;
        }

        switch (action) {
          case InputAction.channelNext:
            _switchChannel(1);
          case InputAction.channelPrevious:
            _switchChannel(-1);
          case InputAction.select:
            // Two stages: with the OSD hidden, Select shows it (armed: an
            // arrow next enters focus mode); with it already displayed and
            // no arrow in between, Select opens the full EPG.
            if (_osdVisibleNotifier.value) {
              _openEpgFromOsd();
            } else {
              _showOsd();
              _osdArmed = true;
            }
          case InputAction.trackSelector:
            // Optional accelerator (no default binding): opens the picker
            // directly. The 6-button path is the OSD's CC/audio buttons.
            if (_channelInputBuffer == null || _channelInputBuffer!.isEmpty) {
              _openTrackPicker(TrackSelectorPanel.audio);
            }
          case InputAction.cycleAudioTrack:
            _cycleTrack(TrackSelectorPanel.audio);
          case InputAction.cycleSubtitleTrack:
            _cycleTrack(TrackSelectorPanel.subtitle);
          case InputAction.debugOverlay:
            _debugOverlayVisibleNotifier.value = !_debugOverlayVisibleNotifier.value;
          case InputAction.home:
          case InputAction.back:
            if (action == InputAction.back && _osdVisibleNotifier.value && _osdArmed) {
              _osdHideTimer?.cancel();
              _osdVisibleNotifier.value = false;
              return;
            }
            // With the EPG closed, `back` (Esc) has the same effect as `home`:
            // it goes back to the mosaic. There's no other "previous" screen
            // in the player to go back to — the only place you can arrive
            // here from is the mosaic (channel selection) or a direct boot
            // into the last channel.
            Navigator.of(context).pushReplacement(MaterialPageRoute(builder: (_) => const MosaicPage()));
          default:
            break;
        }
      },
      child: Scaffold(
        body: Stack(
          children: [
            // Video: ALWAYS the first child of the Stack, never removed/reordered
            // conditionally — only the position/size (via AnimatedPositioned) reacts
            // to `_epgOpenNotifier`, same as the pattern already used by the OSD
            // (`_osdVisibleNotifier`). This is ON PURPOSE: never rebuild
            // `_videoFor(...)` as a consequence of opening/closing the EPG — see
            // the long comment about the fvp/libmdk GL texture bug above
            // (_osdVisibleNotifier).
            ValueListenableBuilder<bool>(
              valueListenable: _epgOpenNotifier,
              builder: (context, epgOpen, child) {
                final screenSize = MediaQuery.sizeOf(context);
                final rect = epgOpen ? _videoThumbnailRect(screenSize) : Offset.zero & screenSize;
                return AnimatedPositioned(
                  duration: const Duration(milliseconds: 300),
                  curve: Curves.easeInOut,
                  left: rect.left,
                  top: rect.top,
                  width: rect.width,
                  height: rect.height,
                  child: child!,
                );
              },
              child: Stack(
                fit: StackFit.expand,
                children: [
                  _videoFor(_controllerA, _opacityA),
                  _videoFor(_controllerB, _opacityB),
                ],
              ),
            ),
            // Loading a channel (boot or switch): a spinner in the middle of the
            // screen until playback starts. Errors keep the plain status card.
            if (!hasContent && !_status.startsWith('ERROR'))
              const Positioned.fill(
                child: Center(
                  child: SizedBox(
                    width: 56,
                    height: 56,
                    child: CircularProgressIndicator(key: Key('loading-indicator'), color: Colors.white, strokeWidth: 4),
                  ),
                ),
              ),
            if (!hasContent && _status.startsWith('ERROR'))
              Positioned(
                left: 16,
                top: 16,
                child: Container(
                  padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
                  color: Colors.black54,
                  child: Text(
                    key: const Key('debug-status'),
                    '$channelLabel\n$_status',
                    style: const TextStyle(color: Colors.white, fontSize: 16),
                  ),
                ),
              ),
            if (hasContent)
              // Full-screen so the clock (top-right) and the bar (bottom) share
              // one fade. ValueListenableBuilder (not an AnimatedOpacity
              // reading a State bool directly): showing/hiding the OSD must
              // not call setState on the parent at all — not even once.
              // `child` is the already-built ChannelOsd, passed in from
              // outside the `builder`, so it isn't rebuilt for nothing on each
              // visibility toggle either. See the `_osdVisibleNotifier`
              // comment for why this really matters (a real texture artifact
              // on 1080p+ video).
              Positioned.fill(
                child: ValueListenableBuilder<bool>(
                  valueListenable: _osdVisibleNotifier,
                  builder: (context, visible, child) => IgnorePointer(
                    ignoring: !visible,
                    child: AnimatedOpacity(
                      opacity: visible ? 1 : 0,
                      duration: const Duration(milliseconds: 300),
                      child: child,
                    ),
                  ),
                  child: ChannelOsd(
                    key: _osdKey,
                    channelNumber: _channels[_channelIndex].number,
                    channelName: _channels[_channelIndex].name,
                    channelArtUrl: _channels[_channelIndex].guideArtUrl,
                    itemLabel: channel[_currentIndex].label,
                    nextItemLabel: _currentIndex + 1 < channel.length ? channel[_currentIndex + 1].label : null,
                    startAt: channel[_currentIndex].startAt,
                    endAt: channel[_currentIndex].startAt.add(channel[_currentIndex].clipDuration),
                    controller: _aIsActive ? _controllerA : _controllerB,
                    focusMode: _osdFocusNotifier,
                    onOpenSubtitles: () => _openTrackPicker(TrackSelectorPanel.subtitle, singlePanel: true),
                    onOpenAudio: () => _openTrackPicker(TrackSelectorPanel.audio, singlePanel: true),
                    recentChannels: _recentChannelShortcuts(),
                    onSelectChannel: (channelId) {
                      _osdHideTimer?.cancel();
                      _osdVisibleNotifier.value = false;
                      final targetIndex = _channels.indexWhere((c) => c.id == channelId);
                      if (targetIndex >= 0 && targetIndex != _channelIndex) {
                        unawaited(_switchToChannelIndex(targetIndex));
                      }
                    },
                  ),
                ),
              ),
            if (showErrorBanner)
              Positioned(
                left: 16,
                right: 16,
                top: 16,
                child: Container(
                  key: const Key('error-banner'),
                  padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
                  decoration: BoxDecoration(
                    color: Colors.red.shade900.withValues(alpha: 0.9),
                    borderRadius: BorderRadius.circular(8),
                  ),
                  child: Text(_status, style: const TextStyle(color: Colors.white, fontSize: 14)),
                ),
              ),
            if (_channelInputBuffer != null && _channelInputBuffer!.isNotEmpty)
              Positioned(
                top: 16,
                right: 16,
                child: Container(
                  key: const Key('channel-input-overlay'),
                  padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
                  decoration: BoxDecoration(
                    color: Colors.black.withValues(alpha: 0.78),
                    borderRadius: BorderRadius.circular(8),
                    border: Border.all(color: Colors.white24),
                  ),
                  child: Text(
                    'Channel: ${_channelInputBuffer}_',
                    style: const TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold),
                  ),
                ),
              ),
            // Debug overlay (D key). Positioned.fill in both returns of the
            // builder for the same reason documented in the EPG's
            // ValueListenableBuilder right below — never a direct Stack child
            // without Positioned.
            ValueListenableBuilder<bool>(
              valueListenable: _debugOverlayVisibleNotifier,
              builder: (context, show, _) {
                if (!show) return const Positioned.fill(child: SizedBox.shrink());
                return Positioned.fill(
                  child: IgnorePointer(
                    child: DebugOverlay(
                      controller: _aIsActive ? _controllerA : _controllerB,
                      decoderPreference: 'CUDA > VAAPI > VDPAU > FFmpeg',
                    ),
                  ),
                );
              },
            ),
            // Full-screen EPG. Last child of the Stack on purpose: it needs to
            // sit on top of everything, including the OSD.
            //
            // Positioned.fill in BOTH returns of the builder (not just a bare
            // `SizedBox.shrink()`) is essential, not cosmetic: a Stack child
            // that isn't `Positioned` — even an empty 0x0 one — breaks the
            // composition of the fvp/libmdk video texture on some GPU/driver
            // combinations (seen with NVIDIA's proprietary driver) and leaves
            // the screen black even though the audio plays normally. Root
            // cause isolated by reverse bisection: copying the real file and
            // cutting parts until the video rendered again — reintroducing
            // just this ValueListenableBuilder, even without ever mounting
            // `EpgOverlay` for real, already reproduced it; wrapping the
            // return in `Positioned.fill` fixed it. Never leave a direct Stack
            // child without `Positioned` — not even a temporary "empty" one.
            ValueListenableBuilder<bool>(
              valueListenable: _epgOpenNotifier,
              builder: (context, open, _) {
                if (!open) return const Positioned.fill(child: SizedBox.shrink());
                return Positioned.fill(
                  child: EpgOverlay(
                    key: _epgOverlayKey,
                    channels: _channels,
                    api: _api,
                    currentChannelId: _channels[_channelIndex].id,
                    videoThumbnailRect: _videoThumbnailRect(MediaQuery.sizeOf(context)),
                    onSelectLiveChannel: (channelId) {
                      _epgOpenNotifier.value = false;
                      final targetIndex = _channels.indexWhere((c) => c.id == channelId);
                      if (targetIndex >= 0 && targetIndex != _channelIndex) {
                        unawaited(_switchToChannelIndex(targetIndex));
                      }
                    },
                    onClose: () => _epgOpenNotifier.value = false,
                  ),
                );
              },
            ),
            // Tier 1 track picker (InputAction.trackSelector). Last child of
            // the Stack — sits on top of everything, including the EPG
            // (mutually exclusive in practice: trackSelector only opens
            // when the EPG is closed, see onKeyEvent above). Positioned.fill
            // in BOTH branches for the same GL-texture-composition reason
            // documented above the EPG's ValueListenableBuilder — never a
            // bare Stack child without Positioned.
            ValueListenableBuilder<bool>(
              valueListenable: _trackPickerOpenNotifier,
              builder: (context, open, _) {
                if (!open) return const Positioned.fill(child: SizedBox.shrink());
                final controller = _aIsActive ? _controllerA : _controllerB;
                final info = controller?.getMediaInfo();
                final audioStreams = info?.audio ?? const [];
                final subtitleStreams = info?.subtitle ?? const [];
                final channelId =
                    (_channelIndex >= 0 && _channelIndex < _channels.length) ? _channels[_channelIndex].id : null;

                final audioOptions = [
                  for (var i = 0; i < audioStreams.length; i++)
                    TrackOption(index: i, label: _trackLabel(audioStreams[i].metadata, i)),
                ];
                final subtitleOptions = [
                  const TrackOption(index: -1, label: 'None'),
                  for (var i = 0; i < subtitleStreams.length; i++)
                    TrackOption(index: i, label: _trackLabel(subtitleStreams[i].metadata, i)),
                ];

                final currentAudioIndex = channelId == null ? null : _settings.getAudioTrackOverride(channelId);
                final currentSubtitleIndex =
                    (channelId == null ? null : _settings.getSubtitleTrackOverride(channelId)) ?? -1;
                final audioFocus = currentAudioIndex == null
                    ? 0
                    : audioOptions.indexWhere((o) => o.index == currentAudioIndex).clamp(0, _lastIndex(audioOptions));
                final subtitleFocus =
                    subtitleOptions.indexWhere((o) => o.index == currentSubtitleIndex).clamp(0, _lastIndex(subtitleOptions));

                return Positioned.fill(
                  child: TrackSelectorOverlay(
                    key: _trackSelectorKey,
                    initialPanel: _trackPickerInitialPanel,
                    singlePanel: _trackPickerSinglePanel,
                    audioOptions: audioOptions,
                    subtitleOptions: subtitleOptions,
                    initialAudioFocusIndex: audioFocus,
                    initialSubtitleFocusIndex: subtitleFocus,
                    onSelect: (panel, option) {
                      final activeController = _aIsActive ? _controllerA : _controllerB;
                      if (activeController == null || channelId == null) return;
                      if (panel == TrackSelectorPanel.audio) {
                        _applyAudioTrack(activeController, channelId, option.index, option.label);
                      } else {
                        _applySubtitleTrack(activeController, channelId, option.index, option.label);
                      }
                      _trackPickerOpenNotifier.value = false;
                    },
                    onClose: () => _trackPickerOpenNotifier.value = false,
                  ),
                );
              },
            ),
          ],
        ),
      ),
    );
  }
}

int _lastIndex(List<TrackOption> options) => options.isEmpty ? 0 : options.length - 1;

// Case-insensitive lookup into a track's ffmpeg-style metadata map (used by
// _createController to read "language"/"title" for the stream selection
// cascade) — real-world containers aren't fully consistent about casing,
// and StreamInfo.metadata (see fvp's media_info.dart) is a plain
// Map<String, String> with no dedicated accessor.
String? _metadataValue(Map<String, String> metadata, String key) {
  for (final entry in metadata.entries) {
    if (entry.key.toLowerCase() == key) return entry.value;
  }
  return null;
}

