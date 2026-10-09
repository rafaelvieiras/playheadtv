// Playhead TV client: assembles a "channel" (program -> commercial -> program)
// on the client with preload + crossfade, using fvp (libmdk) through
// video_player, with no server-side transcoding. The Jellyfin server and API
// key are never hardcoded here — they come from --dart-define (see README).
//
// Video engine: fvp (libmdk) replaced media_kit (libmpv) after media_kit was
// found to stall / fall back to software rendering on Linux (upstream bug
// media-kit/media-kit#1404).
//
// The program list is resolved at runtime from the Go backend (GET /channels +
// the enriched GET /epg), which decides what plays and when —
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
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:video_player/video_player.dart';
import 'package:fvp/fvp.dart' as fvp;

import 'cache/offline_runtime.dart';
import 'cache/schedule_cache.dart';
import 'cache/schedule_sync.dart';
import 'config/config_file.dart';
import 'update/app_update.dart';
import 'update/compat.dart';
import 'widgets/update_overlay.dart';
import 'mosaic_layout.dart' show defaultMosaicSafeMargin;
import 'ui/ui_scale.dart';
import 'playhead_api.dart';
import 'services/screen_awake.dart';
import 'services/settings_service.dart';
import 'services/app_window.dart';
import 'services/stream_preferences_service.dart';
import 'services/stream_selection_resolver.dart';
import 'input/input_action.dart';
import 'pages/mosaic_page.dart';
import 'util/gl_serial.dart';
import 'epg_guide.dart' as guide show defaultTailAttributionCap, tailAttributionCap;
import 'epg_guide.dart' show guideContextLookback, isHiddenGuideKind;
import 'osd_subject.dart';
import 'preload_lead.dart';
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
  static int? _workingSetSize;
  static Duration? _cacheSyncInterval;
  static String? _cacheDir;
  static bool? _startFullScreen;
  static double? _mosaicSafeMargin;
  static Duration? _preloadLead;
  static Duration? _preloadRetryBase;
  // The config file's settings (lib/config/config_file.dart), read once at
  // boot by loadConfigFile. A packaged install configures itself here; each
  // getter below falls back to it when neither a test override nor a
  // --dart-define is set.
  static Map<String, Object?> _file = const {};

  /// Reads the config file (PLAYHEADTV_CONFIG, else the XDG config
  /// directory); called once by main() before runApp. An absent or invalid
  /// file is an empty config.
  static void loadConfigFile([Map<String, String>? env]) {
    _file = readConfigFile(configFileLocation(env ?? Platform.environment));
  }

  static String get jellyfinUrl => resolveStringSetting(
      override: _jellyfinUrl, define: const String.fromEnvironment('JELLYFIN_URL'), file: _file, key: 'jellyfin_url');
  static String get jellyfinApiKey => resolveStringSetting(
      override: _jellyfinApiKey,
      define: const String.fromEnvironment('JELLYFIN_API_KEY'),
      file: _file,
      key: 'jellyfin_api_key');
  // Playhead TV backend (Go) that materializes the schedule — see docs/ARCHITECTURE.md.
  static String get playheadBaseUrl => resolveStringSetting(
      override: _playheadBaseUrl,
      define: const String.fromEnvironment('PLAYHEADTV_BASE_URL'),
      file: _file,
      key: 'playhead_base_url');
  // Optional: device token for the Auth flow. Empty = no Authorization header
  // is sent, which matches a backend running without auth.
  static String get playheadDeviceToken => resolveStringSetting(
      override: _playheadDeviceToken,
      define: const String.fromEnvironment('PLAYHEADTV_DEVICE_TOKEN'),
      file: _file,
      key: 'playhead_device_token');
  // Optional: if empty, automatically resolves the channel with the lowest
  // `number` via GET /channels instead of requiring the operator to know an ID
  // up front.
  static String get playheadChannelIdDefine => resolveStringSetting(
      override: _playheadChannelId,
      define: const String.fromEnvironment('PLAYHEADTV_CHANNEL_ID'),
      file: _file,
      key: 'playhead_channel_id');

  // How many programs ahead of the one on air the player keeps resolved and
  // ready (the "working set"). Short items (music videos) want a larger
  // number than long ones, hence a setting: `--dart-define=WORKING_SET_SIZE=`.
  static int get workingSetSize => resolveIntSetting(
      override: _workingSetSize,
      defined: const bool.hasEnvironment('WORKING_SET_SIZE'),
      define: const int.fromEnvironment('WORKING_SET_SIZE'),
      file: _file,
      key: 'working_set_size',
      fallback: defaultWorkingSetSize);
  // How often the background syncer refreshes the offline cache from the
  // backend (`--dart-define=EPG_SYNC_INTERVAL_MINUTES=`). Offline tolerance is
  // the window minus the time since the last refresh, so keep it well below
  // it.
  static Duration get cacheSyncInterval =>
      _cacheSyncInterval ??
      Duration(
          minutes: resolveIntSetting(
              override: null,
              defined: const bool.hasEnvironment('EPG_SYNC_INTERVAL_MINUTES'),
              define: const int.fromEnvironment('EPG_SYNC_INTERVAL_MINUTES'),
              file: _file,
              key: 'epg_sync_interval_minutes',
              fallback: defaultCacheSyncMinutes));
  // What "Start in full screen" is when the user never chose (desktop only):
  // the PLAYHEADTV_FULLSCREEN environment variable, else `start_fullscreen` in
  // the config file, else on. The Settings toggle, once used, wins over it.
  static bool get startFullScreenDefault => resolveBoolSetting(
      override: _startFullScreen,
      envValue: Platform.environment['PLAYHEADTV_FULLSCREEN'],
      file: _file,
      key: 'start_fullscreen',
      fallback: true);
  // Share of the screen the mosaic leaves free on every side for display
  // overscan: `mosaic_safe_margin_percent` in the config file (0 to 10), else
  // 2%.
  static double get mosaicSafeMargin {
    if (_mosaicSafeMargin != null) return _mosaicSafeMargin!;
    final v = _file['mosaic_safe_margin_percent'];
    return v is num && v >= 0 && v <= 10 ? v / 100 : defaultMosaicSafeMargin;
  }

  // The preload lead when the user never chose one in Settings:
  // `preload_lead_seconds` in the config file, else 20 seconds.
  static int get preloadLeadDefaultSeconds => sanitizePreloadLeadSeconds(_file['preload_lead_seconds']);

  // The configured preload lead (before the adaptive growth): a test override
  // (not clamped, so a test can shrink it), else the user's Settings choice,
  // else the installation default.
  static Duration configuredPreloadLead(SettingsService settings) =>
      _preloadLead ?? Duration(seconds: settings.preloadLeadSeconds ?? preloadLeadDefaultSeconds);

  // Base of the preload retry back-off, shrunk by tests.
  static Duration get preloadRetryBase => _preloadRetryBase ?? defaultPreloadRetryBase;

  // Where the offline cache lives, only overridden by integration tests; null
  // (the app) means the platform's application support/cache directories.
  static String? get cacheDirOverride => _cacheDir;

  /// Only used by `integration_test/` — never called in the real app (`main()`
  /// doesn't call this, so production never sees anything but the
  /// --dart-define values).
  static void overrideForTest({
    required String jellyfinUrl,
    required String jellyfinApiKey,
    required String playheadBaseUrl,
    String playheadDeviceToken = '',
    String playheadChannelId = '',
    int? workingSetSize,
    Duration? cacheSyncInterval,
    String? cacheDir,
    Duration? tailAttributionCap,
    bool? startFullScreen,
    double? mosaicSafeMargin,
    Duration? preloadLead,
    Duration? preloadRetryBase,
  }) {
    // Not part of the app's config: the guide's tail cap, shrunk by tests so a
    // tail goes past it within seconds. Every override resets it.
    guide.tailAttributionCap = tailAttributionCap ?? guide.defaultTailAttributionCap;
    // A new configuration is a new app: forget the previous run's cache,
    // syncer and shared runtime. Without an explicit directory each override
    // gets a fresh one, so a test never inherits another's cache.
    OfflineRuntime.reset();
    _jellyfinUrl = jellyfinUrl;
    _jellyfinApiKey = jellyfinApiKey;
    _playheadBaseUrl = playheadBaseUrl;
    _playheadDeviceToken = playheadDeviceToken;
    _playheadChannelId = playheadChannelId;
    _workingSetSize = workingSetSize;
    _cacheSyncInterval = cacheSyncInterval;
    _startFullScreen = startFullScreen;
    _mosaicSafeMargin = mosaicSafeMargin;
    _preloadLead = preloadLead;
    _preloadRetryBase = preloadRetryBase;
    _cacheDir = cacheDir ?? Directory.systemTemp.createTempSync('playheadtv_cache_').path;
  }
}

// Programs kept resolved and ready ahead of the one on air (the working set),
// see AppConfig.workingSetSize. A grouped tail counts as ONE program however
// many clips it carries.
const defaultWorkingSetSize = 5;

// The default refresh period of the offline cache (minutes), see
// AppConfig.cacheSyncInterval.
const defaultCacheSyncMinutes = 15;

// How long boot waits for the startup refresh before playing from what the
// cache already holds (a later refresh is reconciled while playing).
const startupSyncWait = Duration(seconds: 2);

// Retry interval when the cached schedule has really run out (a real gap in
// the schedule, or the cache exhausted by a long outage) — repeats until
// something is found and playback resumes on its own.
const epgGapRetryInterval = Duration(seconds: 20);

// How much of the last known programming the "loop" fallback replays once
// the cached schedule has run out.
const exhaustedLoopSpan = Duration(hours: 24);

// Direct jump by channel number (real remote-control style navigation): time
// without a new digit before the typed buffer resolves on its own, and the
// limit of accepted digits — no 4-digit channels are planned, so reaching the
// limit resolves immediately without waiting for the timeout.
const channelInputCommitDelay = Duration(milliseconds: 1500);
const channelInputMaxDigits = 3;

// One playable unit of a channel: a single stream the player loads. A guide
// program is one unit, except a grouped tail, which is one program made of
// many units (its clips). `programOrdinal` numbers the programs in the order
// they entered the working set; units of one program share it.
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
  final int programOrdinal;
  final EpgEntry? program;
  // Where in the media item this unit starts playing (epg_entry
  // item_offset_ms: nonzero only for the later segments of a split item).
  // The unit plays from here for `clipDuration`, so every seek into it (a
  // preload, a tune-in in the middle, a resync) is relative to this offset.
  final Duration itemOffset;
  const ChannelEntry(this.id, this.label, this.clipDuration, this.startAt,
      {this.programOrdinal = 0, this.program, this.itemOffset = Duration.zero});

  // What the OSD shows: the guide program this unit belongs to (a tail's
  // generic title and whole span, not its clip's).
  String get programLabel => program?.displayTitle ?? label;
  DateTime get programStartAt => program?.startAt ?? startAt;
  DateTime get programEndAt => program?.endAt ?? startAt.add(clipDuration);
}

/// The playable units of [program]. A single program is one unit; a grouped
/// tail is one unit per clip. Units that already ended by [notBefore], and
/// any with a non-positive duration, are dropped (see AGENTS.md on bad
/// durations). Requires [program] to be resolved (see
/// PlayheadApi.resolveCatalog).
List<ChannelEntry> unitsOfProgram(EpgEntry program, int ordinal, {DateTime? notBefore}) {
  if (!program.isTailGroup) {
    final ext = program.externalId;
    if (ext == null || !program.endAt.isAfter(program.startAt)) return const [];
    if (notBefore != null && !program.endAt.isAfter(notBefore)) return const [];
    return [
      ChannelEntry(ext, program.displayTitle, program.endAt.difference(program.startAt), program.startAt,
          programOrdinal: ordinal, program: program, itemOffset: Duration(milliseconds: program.itemOffsetMs)),
    ];
  }
  return [
    for (final clip in program.clips)
      if (clip.externalId != null &&
          clip.endAt.isAfter(clip.startAt) &&
          (notBefore == null || clip.endAt.isAfter(notBefore)))
        ChannelEntry(clip.externalId!, clip.title ?? program.displayTitle, clip.endAt.difference(clip.startAt), clip.startAt,
            programOrdinal: ordinal, program: program),
  ];
}

/// What reading the cache beyond the cursor adds to a channel's known
/// programs: entries starting at or after [cursor], and the longer version of
/// the last known tail run when the cache has one (the materializer keeps
/// appending clips to a run that reaches the end of its window). [extraUnits]
/// are the new clips of that run, only when it was already expanded into
/// playable units ([knownExpanded] covers it). Null when nothing grew.
({List<EpgEntry> known, DateTime cursor, List<ChannelEntry> extraUnits})? growKnown({
  required List<EpgEntry> known,
  required int knownExpanded,
  required DateTime cursor,
  required List<EpgEntry> cached,
}) {
  final valid = cached.where((e) => e.endAt.isAfter(e.startAt)).toList();
  final fresh = valid.where((e) => !e.startAt.isBefore(cursor)).toList();
  final last = known.isEmpty ? null : known.last;
  final longer = last == null || !last.isTailGroup
      ? null
      : valid.where((e) => e.isTailGroup && e.startAt == last.startAt && e.endAt.isAfter(last.endAt)).firstOrNull;
  if (fresh.isEmpty && longer == null) return null;
  var extra = const <ChannelEntry>[];
  var next = known;
  var newCursor = cursor;
  if (longer != null) {
    final ordinal = known.length - 1;
    if (ordinal < knownExpanded) extra = unitsOfProgram(longer, ordinal, notBefore: last!.endAt);
    next = [...known.take(ordinal), longer];
    if (longer.endAt.isAfter(newCursor)) newCursor = longer.endAt;
  }
  for (final e in fresh) {
    if (e.endAt.isAfter(newCursor)) newCursor = e.endAt;
  }
  return (known: [...next, ...fresh], cursor: newCursor, extraUnits: extra);
}

// Populated at runtime by _loadAndPlayChannel from the GET /epg materialized
// by the backend — never a hardcoded constant.
List<ChannelEntry> channel = <ChannelEntry>[];

const crossfadeDuration = Duration(milliseconds: 800);

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
// Pixels at 1080p; `_videoThumbnailRect` applies the UI scale.
const epgThumbnailMargin = 24.0;
const epgThumbnailWidth = 320.0;
const epgThumbnailHeight = 180.0;

// Fallback for the backend's EPG freeze margin (GET /epg/meta
// `freeze_margin_ms`): how far ahead the guide of a continuous-schedule channel
// is frozen. Used when the backend does not report one.
// The known runway (end of the last known program minus now) below which the
// player reads what the cache gained beyond it.
const epgExtendThreshold = Duration(hours: 1);

const epgFreezeMarginFallback = Duration(hours: 3);

// Playback buffer window handed to mdk (see _createController).
const playbackBufferMinMs = 2000;
const playbackBufferMaxMs = 30000;

String streamUrl(String itemId) =>
    '${AppConfig.jellyfinUrl}/Videos/$itemId/stream?Static=true&api_key=${AppConfig.jellyfinApiKey}';

// Poster URL built from the media server address and key the client already
// holds and the image tag the backend sent (the backend never serves art).
String jellyfinImageUrl(String itemId, String tag, {int maxWidth = 400}) =>
    '${AppConfig.jellyfinUrl}/Items/$itemId/Images/Primary?tag=$tag&maxWidth=$maxWidth&api_key=${AppConfig.jellyfinApiKey}';

/// Puts the window in (or out of) full screen per the saved choice, else the
/// installation's default. Runs before `runApp`, so there is no visible resize:
/// the runner shows its window on the first Flutter frame. A no-op where the
/// platform has no resizable window (Android).
Future<void> applyStartupFullScreen() async {
  final settings = await SettingsService.create();
  await FullScreenController.shared.apply(settings.startFullScreen(AppConfig.startFullScreenDefault));
}

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  AppConfig.loadConfigFile();
  await applyStartupFullScreen();
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
  // When the launcher started this process right after an update, confirm
  // the new version once it has stayed up for the healthy window (no-op
  // otherwise); an unconfirmed update is rolled back by the next start.
  scheduleHealthConfirmation();
  unawaited(_warnIfBackendIncompatible());
}

// The app's navigator, so a message that is not tied to a screen (the backend
// compatibility notice) can be shown over whatever is on top.
final GlobalKey<NavigatorState> _appNavigatorKey = GlobalKey<NavigatorState>();

// Reads the backend's API version (GET /version) and, only when it is outside
// the range this app supports, shows a clear message. A backend that does not
// answer, or predates /version, is silent: the offline cache keeps playing.
// The dialog is a single button, focused, so Select (the 6-button baseline)
// dismisses it.
Future<void> _warnIfBackendIncompatible() async {
  final compat = await checkBackendCompat(AppConfig.playheadBaseUrl);
  final message = compat.message;
  if (message == null) return;
  // Wait for the first frame so the navigator exists.
  for (var i = 0; i < 50 && _appNavigatorKey.currentContext == null; i++) {
    await Future<void>.delayed(const Duration(milliseconds: 100));
  }
  final context = _appNavigatorKey.currentContext;
  if (context == null || !context.mounted) return;
  await showDialog<void>(
    context: context,
    builder: (ctx) => UiScaled(
      child: AlertDialog(
        key: const Key('backend-compat-dialog'),
        title: const Text('Backend version not supported'),
        content: Text(message),
        actions: [
          TextButton(
            autofocus: true,
            onPressed: () => Navigator.of(ctx).pop(),
            child: const Text('OK'),
          ),
        ],
      ),
    ),
  );
}

// What a channel is doing when the cached schedule has run out while the
// backend cannot be reached (see ExhaustedBehavior, the client's own setting).
enum _Degraded { none, loop, slate }

// Everything behind and around the video (letterbox/pillarbox bars, the loading
// screen, crossfades, the gap slate) is pure black, like a TV. Set statically on
// the player's Scaffold, never through setState (see the GL-texture gotcha).
const Color playerBackgroundColor = Color(0xFF000000);

class PlayheadSpikeApp extends StatelessWidget {
  const PlayheadSpikeApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Playhead TV',
      navigatorKey: _appNavigatorKey,
      // The update banner / changelog sit above the Navigator: they never
      // rebuild a page (the Navigator is passed through as a child).
      builder: (context, child) =>
          UiScaleHost(child: UpdateOverlayHost(child: child ?? const SizedBox.shrink())),
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
    return const Scaffold(backgroundColor: playerBackgroundColor, body: Center(child: CircularProgressIndicator()));
  }
}

class ChannelPlayerPage extends StatefulWidget {
  final String? initialChannelId;
  const ChannelPlayerPage({super.key, this.initialChannelId});

  @override
  State<ChannelPlayerPage> createState() => _ChannelPlayerPageState();
}

class _ChannelPlayerPageState extends State<ChannelPlayerPage> with WidgetsBindingObserver {
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

  // Keeps the display awake while a channel plays. Toggled from
  // _startChannel/lifecycle/dispose only, never via setState.
  final ScreenAwakeController _screenAwake = ScreenAwakeController();

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

  // What the client KNOWS of this channel (programs read from the offline
  // cache, already enriched: titles, source ids, art references, a tail's
  // clips) vs what it holds ready (`channel`, the working set): `channel` only
  // receives the programs of `_known` up to the one on air plus
  // AppConfig.workingSetSize, as playback advances (_expandWorkingSet).
  // Reset on every new _loadAndPlayChannel; refreshed in place when a
  // background sync changed the cache (_reconcileWithCache).
  List<EpgEntry> _known = <EpgEntry>[];
  // Entries before the first fetched one, only when it is a filler, so the OSD
  // can find the program that filler is attributed to (see osd_subject.dart).
  List<EpgEntry> _osdContext = <EpgEntry>[];
  // `_known[0.._knownExpanded)` have been turned into units of `channel`.
  int _knownExpanded = 0;
  // Cursor: the largest `endAt` among `_known` — where to continue reading
  // the cache from.
  DateTime? _epgLoadedUntil;
  Future<void>? _pendingExpand;
  // Only exists while the known schedule has really run out (a real gap, or
  // the cache exhausted by a long outage) — repeats the check until something
  // turns up and playback resumes on its own.
  Timer? _gapRetryTimer;
  // Set while the channel plays the degraded fallback of an exhausted cache
  // (see _Degraded): any later cache change that brings real programming
  // reloads the channel out of it.
  _Degraded _degraded = _Degraded.none;
  // A cache change arrived while a channel was still loading: reconciled as
  // soon as it is playing.
  bool _reconcilePending = false;
  // Counts looped copies of the last known programming (distinct entry ids).
  int _loopCount = 0;
  StreamSubscription<CacheChange>? _cacheSub;
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

  // The preload of the next unit (see _preloadNext). `_preloadReadyIndex` is
  // the channel index whose controller sits in the inactive slot, ready to be
  // crossfaded into; `_preloadFuture` is the attempt loop in flight for
  // `_preloadTargetIndex` of generation `_preloadGeneration`. Plain fields on
  // purpose, never setState: nothing the build reads. `_preloadTracker`
  // measures how long preloads take and grows the lead (preload_lead.dart); it
  // outlives channel switches, a slow library is slow on every channel.
  Timer? _preloadTimer;
  Future<void>? _preloadFuture;
  int? _preloadTargetIndex;
  int _preloadGeneration = -1;
  int? _preloadReadyIndex;
  final PreloadLeadTracker _preloadTracker = PreloadLeadTracker();

  late OfflineRuntime _runtime;
  ScheduleCache get _cache => _runtime.cache;
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
    WidgetsBinding.instance.addObserver(this);
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
    _bootstrapChannels();
  }

  // Reads the channel list from the offline cache (it needs the whole list,
  // not just the initial channel, to know what to switch to with arrow
  // up/down), resolves the initial channel (dart-define or the one with the
  // lowest `number`) and loads its schedule. The cache is refilled by the
  // background syncer; boot gives the startup refresh a moment to bring fresh
  // data and otherwise plays from what is already on disk — so a cold start
  // with the backend down works. Only a first run with nothing cached yet has
  // to wait for the backend.
  Future<void> _bootstrapChannels() async {
    setState(() => _status = 'loading channels…');
    try {
      _settings = await SettingsService.create();
      _keyBindings = KeyBindingsService(_settings);
      _runtime = await OfflineRuntime.ensure();
      _streamPrefs = await StreamPreferencesService.create(_runtime.api);
      _streamPrefsReady = true;
      // Tier 2-4 rules ride alongside the startup refresh (a failure keeps the
      // cached rules, see StreamPreferencesService).
      final streamPrefsRefresh = _streamPrefs.refreshAll();
      await _runtime.syncer.syncNow().timeout(startupSyncWait, onTimeout: () => SyncOutcome.offline);
      var channels = _cache.channels();
      if (channels.isEmpty) {
        // Nothing cached yet (first run): the backend is the only source.
        await _runtime.syncer.syncNow();
        channels = _cache.channels();
      }
      await streamPrefsRefresh;
      _streamPrefs.startPolling();
      _cacheSub = _cache.changes.listen(_onCacheChange);
      if (channels.isEmpty) {
        if (!mounted) return;
        final error = _runtime.syncer.status.value.lastError;
        setState(() => _status = _runtime.syncer.status.value.lastSucceeded
            ? 'ERROR: no channels configured on the backend (GET /channels is empty)'
            : 'ERROR: failed to load channel list: $error');
        return;
      }
      final sorted = channels;
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
    _resetPreload();
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
    _pendingExpand = null;
    _degraded = _Degraded.none;
    _reconcilePending = false;
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
      _known = <EpgEntry>[];
      _osdContext = <EpgEntry>[];
      _knownExpanded = 0;
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

  // What the cache knows of the channel from `now` on, minus whatever already
  // ended (can only happen because of some clock/materialization slack, but
  // safer than assuming it never occurs) and anything with a non-positive
  // duration (a corrupt/unprobed source item — pullsync's own guard should
  // already reject these at sync time, but _scheduleTransitions fires its
  // next-item timer immediately for a zero-length clip, so a single one that
  // slips through would churn real controller create/dispose cycles until the
  // process runs out of file descriptors — reproduced 2026-09-30, see
  // AGENTS.md).
  List<EpgEntry> _upcomingFromCache(String channelId, DateTime now) =>
      _cache.entries(channelId, now, _cache.horizonEnd).where((e) => e.endAt.isAfter(now) && e.endAt.isAfter(e.startAt)).toList();

  // One last refresh attempt when the cache has nothing ahead, to tell "the
  // backend really has nothing" (it answered) from "the cache ran out while
  // the backend is out of reach" (it did not): only the latter is an
  // exhausted cache that the degraded behavior setting applies to.
  Future<bool> _backendUnreachable() async {
    final outcome = await _runtime.syncer.syncNow().timeout(const Duration(seconds: 4), onTimeout: () => SyncOutcome.offline);
    return outcome != SyncOutcome.updated && outcome != SyncOutcome.unchanged;
  }

  // The last known programming of the channel replayed from [from] on, as
  // copies shifted in time with their own ids: the last exhaustedLoopSpan of
  // what the cache holds, in order. Empty when the cache holds nothing.
  List<EpgEntry> _loopedEntries(String channelId, DateTime from) {
    final all = _cache.allEntries(channelId).where((e) => e.endAt.isAfter(e.startAt)).toList();
    if (all.isEmpty) return const [];
    final lastEnd = all.map((e) => e.endAt).reduce((a, b) => a.isAfter(b) ? a : b);
    final cutoff = lastEnd.subtract(exhaustedLoopSpan);
    final source = all.where((e) => !e.startAt.isBefore(cutoff)).toList();
    if (source.isEmpty) return const [];
    final shift = from.difference(source.first.startAt);
    final copy = _loopCount++;
    return [for (final e in source) e.shifted(shift, idPrefix: 'loop$copy:')];
  }

  // Appends another replay of the last known programming to `_known`. False
  // when there is nothing to replay.
  bool _appendLoop() {
    if (_channelIndex >= _channels.length) return false;
    final now = DateTime.now().toUtc();
    var from = _epgLoadedUntil ?? now;
    if (from.isBefore(now)) from = now;
    final looped = _loopedEntries(_channels[_channelIndex].id, from);
    if (looped.isEmpty) return false;
    _known = [..._known, ...looped];
    _epgLoadedUntil = _known.map((e) => e.endAt).reduce((a, b) => a.isAfter(b) ? a : b);
    return true;
  }

  // Reads the channel from the offline cache — the entries are already
  // enriched with titles, source ids, art references and a tail's clips —
  // hands the program on air plus the next AppConfig.workingSetSize to the
  // player and starts playback. The client never decides WHAT plays — it
  // only plays what the backend already decided, as last synced (see
  // AGENTS.md); with the backend down this is the whole flow. `generation`
  // is checked before every `setState` that follows an `await`, so it
  // doesn't trample a channel that was switched again in the meantime.
  Future<void> _loadAndPlayChannel(PlayheadChannel target, {required int generation}) async {
    try {
      final now = DateTime.now().toUtc();
      // Kicked off in parallel with the work below (never awaited until just
      // before it's needed) so resolving tier 3 (channel admin) rules never
      // adds latency to the channel-load path. _createController itself never
      // awaits anything for this; see _currentChannelRules' doc comment.
      final channelRulesFuture = _streamPrefs.ensureChannelRules(target.id);
      var upcoming = _upcomingFromCache(target.id, now);
      if (upcoming.isEmpty) {
        final unreachable = await _backendUnreachable();
        if (generation != _loadGeneration) return;
        upcoming = _upcomingFromCache(target.id, now);
        if (upcoming.isEmpty && unreachable) {
          // The cache is exhausted: the client's own setting decides.
          if (_settings.exhaustedBehavior == ExhaustedBehavior.slate) {
            await _holdSlateAtLoad(generation);
            return;
          }
          upcoming = _loopedEntries(target.id, now);
          if (upcoming.isNotEmpty) _degraded = _Degraded.loop;
        }
      }
      if (upcoming.isEmpty) {
        if (!mounted) return;
        setState(() => _status = 'ERROR: nothing scheduled in the next ${_cache.window.inHours}h for channel ${target.number}');
        return;
      }

      _currentChannelRules = await channelRulesFuture;
      if (generation != _loadGeneration) return;

      _known = upcoming;
      _osdContext = _osdContextFromCache(target.id, upcoming.first);
      if (generation != _loadGeneration) return;
      _knownExpanded = 0;
      _epgLoadedUntil = upcoming.map((e) => e.endAt).reduce((a, b) => a.isAfter(b) ? a : b);
      final loaded = <ChannelEntry>[];
      await _expandInto(loaded, generation, notBefore: now);
      if (generation != _loadGeneration) return;
      if (loaded.isEmpty) {
        if (!mounted) return;
        setState(() => _status = 'ERROR: nothing playable scheduled for channel ${target.number}');
        return;
      }

      // "TV turned on" joins the middle of the first program if it already
      // started, rather than going back to its beginning — only the first
      // item gets this seek.
      var initialSeek = now.difference(loaded.first.startAt);
      if (initialSeek < Duration.zero) initialSeek = Duration.zero;
      if (initialSeek > loaded.first.clipDuration) initialSeek = loaded.first.clipDuration;

      if (!mounted || generation != _loadGeneration) return;
      setState(() {
        channel = loaded;
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
      setState(() => _status = 'ERROR: failed to load the schedule: $e');
    }
  }

  Future<VideoPlayerController> _createController(ChannelEntry entry, {Duration? seekTo}) async {
    final controller = VideoPlayerController.networkUrl(Uri.parse(streamUrl(entry.id)));
    await serializeGlOp(() => controller.initialize());
    // `seekTo` is a position within the entry (the "TV turned on" in the
    // middle of a program); the entry itself starts at its item offset, so the
    // later segment of a split item (same file, resumed after the break) is
    // positioned at its resume point as soon as it is prepared, long before it
    // is shown. A whole item has offset zero and is only seeked for a tune-in.
    final position = entry.itemOffset + (seekTo ?? Duration.zero);
    if (position > Duration.zero) {
      await controller.seekTo(position);
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
    _screenAwake.playing = true;
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
    if (_reconcilePending) {
      _reconcilePending = false;
      unawaited(_reconcileWithCache());
    }
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
    // The lead is the configured one, grown by what recent preloads measured
    // (preload_lead.dart). An item shorter than it preloads right after it
    // starts.
    final lead = _preloadTracker.effective(AppConfig.configuredPreloadLead(_settings));
    _preloadTimer?.cancel();
    _preloadTimer = Timer(preloadDelay(remaining: effective, lead: lead), () => _preloadNext(index + 1, generation));

    _advanceTimer?.cancel();
    _advanceTimer = Timer(effective, () => _crossfadeTo(index + 1, generation));
  }

  // Forgets the preload bookkeeping of the channel being left. A controller an
  // attempt is still creating is disposed by the attempt itself (it checks the
  // generation when it resolves); one already parked in a slot is disposed by
  // the caller's teardown of both slots.
  void _resetPreload() {
    _preloadTimer?.cancel();
    _preloadTimer = null;
    _preloadFuture = null;
    _preloadTargetIndex = null;
    _preloadReadyIndex = null;
    _preloadGeneration = -1;
  }

  // Starts preparing `nextIndex` unless that is already done or in flight (one
  // preload at a time per channel). Never throws: a failed attempt is retried.
  Future<void> _preloadNext(int nextIndex, int generation) async {
    if (!mounted || generation != _loadGeneration) return;
    if (nextIndex >= channel.length) return;
    if (_preloadGeneration == generation) {
      if (_preloadReadyIndex == nextIndex) return;
      if (_preloadFuture != null && _preloadTargetIndex == nextIndex) return;
    }
    _preloadGeneration = generation;
    _preloadTargetIndex = nextIndex;
    _preloadReadyIndex = null;
    final attempts = _runPreload(nextIndex, generation);
    _preloadFuture = attempts;
    await attempts;
  }

  // Attempts to create the controller for `nextIndex` until one succeeds or
  // the channel changes: a failure waits preloadRetryDelay and tries again,
  // reporting an error after preloadAttemptsBeforeError failures but never
  // giving up (the channel must not stay stopped on a late source). Every
  // attempt's duration feeds the adaptive lead.
  Future<void> _runPreload(int nextIndex, int generation) async {
    var failures = 0;
    while (mounted && generation == _loadGeneration) {
      final nextEntry = channel[nextIndex];
      setState(() => _status = 'preload: ${nextEntry.label}');
      final clock = Stopwatch()..start();
      VideoPlayerController? controller;
      try {
        controller = await _createController(nextEntry);
        await controller.setVolume(0);
      } catch (e) {
        clock.stop();
        _preloadTracker.record(clock.elapsed);
        final broken = controller;
        if (broken != null) unawaited(serializeGlOp(() => broken.dispose()));
        failures++;
        if (!mounted || generation != _loadGeneration) return;
        if (failures >= preloadAttemptsBeforeError) {
          setState(() => _status = 'ERROR: preload of ${nextEntry.label} failed, retrying… ($e)');
        }
        await Future<void>.delayed(preloadRetryDelay(failures, AppConfig.preloadRetryBase));
        continue;
      }
      clock.stop();
      _preloadTracker.record(clock.elapsed);
      if (!mounted || generation != _loadGeneration) {
        unawaited(serializeGlOp(() => controller!.dispose()));
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
      _preloadReadyIndex = nextIndex;
      return;
    }
  }

  // Waits until the preload of `nextIndex` is ready, starting it when nothing
  // did (a preload timer that never fired, a unit that only appeared late).
  // The item on air (or the gap slate) stays as it is meanwhile. False when the
  // channel changed while waiting.
  Future<bool> _awaitPreload(int nextIndex, int generation) async {
    while (mounted && generation == _loadGeneration) {
      if (_preloadGeneration == generation && _preloadReadyIndex == nextIndex) return true;
      final inFlight = _preloadGeneration == generation && _preloadTargetIndex == nextIndex ? _preloadFuture : null;
      if (inFlight != null) {
        await inFlight;
      } else {
        await _preloadNext(nextIndex, generation);
        // Nothing started (the unit vanished): do not spin.
        if (_preloadFuture == null) return false;
      }
    }
    return false;
  }

  Future<void> _crossfadeTo(int nextIndex, int generation) async {
    if (generation != _loadGeneration) return;
    if (nextIndex >= channel.length) {
      // What the cache knows ran out before the extension trigger
      // (_maybeExtendGrid) found more — look once more right here before
      // giving up.
      final extended = await _extendWindowNow(generation);
      if (generation != _loadGeneration) return;
      if (extended && nextIndex < channel.length) {
        // There's no preload ready for the newly arrived item (there wasn't
        // time) — resume with a hard cut instead of trying a crossfade with
        // no incoming controller.
        await _resumeAt(nextIndex, generation);
        return;
      }

      // Nothing more is known. Ask the backend one last time: if it cannot be
      // reached the cache is exhausted (offline for longer than the window)
      // and the client's own setting decides what the channel does; if it
      // answers, the schedule really has nothing more and the retry below
      // waits for it.
      if (await _backendUnreachable()) {
        if (generation != _loadGeneration) return;
        if (await _extendWindowNow(generation) && nextIndex < channel.length) {
          await _resumeAt(nextIndex, generation);
          return;
        }
        if (generation != _loadGeneration) return;
        if (_settings.exhaustedBehavior == ExhaustedBehavior.loopLast && _appendLoop()) {
          await _expandWorkingSet(generation);
          if (generation != _loadGeneration) return;
          if (nextIndex < channel.length) {
            _degraded = _Degraded.loop;
            await _resumeAt(nextIndex, generation);
            return;
          }
        }
        if (_settings.exhaustedBehavior == ExhaustedBehavior.slate) {
          await _holdSlateExhausted(generation);
          return;
        }
      } else {
        if (generation != _loadGeneration) return;
        if (await _extendWindowNow(generation) && nextIndex < channel.length) {
          await _resumeAt(nextIndex, generation);
          return;
        }
      }
      if (generation != _loadGeneration) return;

      // Still empty: a real gap in the schedule. Don't hang forever — show a
      // status and retry periodically until something shows up, resuming on
      // its own.
      if (!mounted) return;
      setState(() => _status = 'ERROR: nothing more scheduled right now — retrying…');
      _gapRetryTimer?.cancel();
      _gapRetryTimer = Timer.periodic(epgGapRetryInterval, (t) async {
        if (generation != _loadGeneration) {
          t.cancel();
          return;
        }
        final ok = await _extendWindowNow(generation);
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
    // Not ready yet (a slow source, a failed first attempt): keep what is on
    // air and wait for it; the retry loop in _runPreload does not give up.
    if (_preloadGeneration != generation || _preloadReadyIndex != nextIndex) {
      if (!await _awaitPreload(nextIndex, generation)) return;
    }
    final incoming = _aIsActive ? _controllerB : _controllerA;
    if (incoming == null) return;
    _preloadReadyIndex = null;
    _preloadFuture = null;
    _preloadTargetIndex = null;

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
      await incoming.seekTo(nextEntry.itemOffset + seek);
      if (generation != _loadGeneration) return;
    }

    setState(() => _status = 'CROSSFADE -> ${nextEntry.label}');
    await _rampCrossfade(outgoing: outgoing, incoming: incoming, generation: generation);
    if (generation != _loadGeneration) return;
    if (!mounted) return;
    final previousKey = _osdSubjectAt(_currentIndex)?.key;
    setState(() {
      _currentIndex = nextIndex;
      _status = 'playing: ${nextEntry.label}';
    });
    // The OSD announces programs: a filler of the same program, or one more
    // 10-second clip inside a tail, is not an announcement.
    if (_osdSubjectAt(nextIndex)?.key != previousKey) _showOsd();
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
  // which fires the preload lead before the CURRENT item ends,
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
      final previousKey = _osdSubjectAt(_currentIndex)?.key;
      setState(() {
        _currentIndex = nextIndex;
        _status = 'playing: ${nextEntry.label}';
      });
      if (_osdSubjectAt(nextIndex)?.key != previousKey) _showOsd();
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
    _resetPreload();
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

  // Turns the next programs of `_known` into playable units appended to
  // `out`, until the one on air plus AppConfig.workingSetSize are covered (a
  // grouped tail is one program however many clips it carries). `notBefore`
  // trims units that already ended (only the very first program at tune-in
  // can have any). Nothing here asks the backend: the entries in the cache
  // were completed (a backend that did not enrich them is resolved once, by
  // the syncer) before they were stored, and one that still is not playable
  // is skipped by [unitsOfProgram].
  Future<void> _expandInto(List<ChannelEntry> out, int generation, {DateTime? notBefore, int? currentOrdinal}) async {
    while (_knownExpanded < _known.length) {
      final cur = currentOrdinal ?? (out.isEmpty ? null : out.first.programOrdinal);
      if (cur != null && (_knownExpanded - 1) - cur >= AppConfig.workingSetSize) break;
      if (generation != _loadGeneration) return;
      final ordinal = _knownExpanded;
      out.addAll(unitsOfProgram(_known[ordinal], ordinal, notBefore: ordinal == 0 ? notBefore : null));
      _knownExpanded = ordinal + 1;
    }
  }

  // Keeps the working set topped up while the channel plays: appends the
  // programs that entered the window since the last call to `channel` (never
  // replaces the list nor touches the indexes already in use by scheduled
  // timers/controllers). Single-flight, so concurrent triggers can't append
  // the same program twice.
  Future<void> _expandWorkingSet(int generation) {
    final pending = _pendingExpand;
    if (pending != null) return pending;
    final future = () async {
      try {
        final added = <ChannelEntry>[];
        final current = (_currentIndex >= 0 && _currentIndex < channel.length) ? channel[_currentIndex].programOrdinal : 0;
        await _expandInto(added, generation, currentOrdinal: current);
        if (generation != _loadGeneration || !mounted || added.isEmpty) return;
        setState(() => channel = [...channel, ...added]);
      } catch (_) {
        // A failed lookup (older backend) or fetch must not bring down the
        // playback in progress — the next call tries again.
      }
    }();
    _pendingExpand = future;
    future.whenComplete(() {
      if (identical(_pendingExpand, future)) _pendingExpand = null;
    });
    return future;
  }

  // Run after every item change: tops up the working set, and if the known
  // runway (end of the last known program minus now) fell below an hour, reads
  // whatever the cache gained beyond it (the background sync keeps topping the
  // cache up; this is the view over it).
  Future<void> _maybeExtendGrid(int fromIndex, int generation) async {
    if (generation != _loadGeneration) return;
    await _expandWorkingSet(generation);
    if (generation != _loadGeneration) return;
    final loadedUntil = _epgLoadedUntil;
    if (loadedUntil == null) return;
    if (loadedUntil.difference(DateTime.now().toUtc()) >= epgExtendThreshold) return;
    if (_readNextFromCache()) await _expandWorkingSet(generation);
  }

  // For the dead end (the next unit isn't in `channel` yet): expands what is
  // already known, else reads what the cache holds beyond it and expands that.
  // True when `channel` grew.
  Future<bool> _extendWindowNow(int generation) async {
    final before = channel.length;
    await _expandWorkingSet(generation);
    if (generation != _loadGeneration) return false;
    if (channel.length == before && _knownExpanded >= _known.length) {
      if (_readNextFromCache()) await _expandWorkingSet(generation);
    }
    return generation == _loadGeneration && channel.length > before;
  }

  // Appends what the cache has from the cursor on to `_known` and advances the
  // cursor to the largest endAt among the new programs. True only when
  // something new arrived. Defensively skips an entry starting before the
  // cursor (already incorporated) and, as in _upcomingFromCache, any with a
  // non-positive duration. A tail run that reaches the end of the cached
  // window grows with it (the materializer keeps appending clips): the last
  // known program is replaced by the longer run and its new clips are
  // appended to the working set, so a channel that plays a tail for as long
  // as the window never hits a dead end that the cache can actually fill.
  bool _readNextFromCache() {
    final cursor = _epgLoadedUntil;
    if (cursor == null || _channelIndex >= _channels.length) return false;
    final readFrom = cursor.subtract(const Duration(milliseconds: 1));
    final cached = _cache.entries(_channels[_channelIndex].id, readFrom, _cache.horizonEnd);
    final growth = growKnown(known: _known, knownExpanded: _knownExpanded, cursor: cursor, cached: cached);
    if (growth == null) return false;
    if (growth.extraUnits.isNotEmpty && mounted) setState(() => channel = [...channel, ...growth.extraUnits]);
    _known = growth.known;
    _epgLoadedUntil = growth.cursor;
    return true;
  }

  // The cache changed under the channel on air (a background sync brought a
  // different schedule, e.g. after an admin grid reset or a backend that was
  // unreachable came back): applies it exactly like a grid reset on this
  // channel — if the program on air differs from what the new schedule has
  // on air now, the channel is reloaded and switches to it; otherwise only
  // what comes after the program on air is refreshed in place.
  void _onCacheChange(CacheChange change) {
    if (!mounted || _channels.isEmpty) return;
    if (change.channelsChanged) {
      final fresh = _cache.channels();
      if (fresh.isNotEmpty) {
        final currentId = _channels[_channelIndex].id;
        final at = fresh.indexWhere((c) => c.id == currentId);
        if (at >= 0) {
          setState(() {
            _channels = fresh;
            _channelIndex = at;
          });
        } else {
          // The channel on air was removed: go to the first one.
          setState(() => _channels = fresh);
          unawaited(_switchToChannelIndex(0));
          return;
        }
      }
    }
    if (change.channelIds.contains(_channels[_channelIndex].id)) unawaited(_reconcileWithCache());
  }

  // Two entries are the same airing when they are the same item over the same
  // span (their ids change when a grid reset regenerates them). A tail run
  // only has to start the same way: the materializer keeps appending clips to
  // a run that reaches the end of its window, which must not restart the clip
  // that is playing.
  bool _sameAiring(EpgEntry a, EpgEntry b) {
    if (a.kind != b.kind || a.startAt != b.startAt) return false;
    if (a.isTailGroup || b.isTailGroup) return a.isTailGroup && b.isTailGroup && a.clips.first.catalogItemId == b.clips.first.catalogItemId;
    return a.catalogItemId == b.catalogItemId && a.endAt == b.endAt;
  }

  Future<void> _reconcileWithCache() async {
    if (!mounted || _channelIndex >= _channels.length) return;
    if (_channelLoadInFlight || _currentIndex < 0 || _currentIndex >= channel.length) {
      // Still loading (or holding with nothing playing): reconciled once a
      // channel is playing, or by the degraded-mode check just below.
      if (_degraded == _Degraded.none) {
        _reconcilePending = true;
        return;
      }
    }
    final generation = _loadGeneration;
    final target = _channels[_channelIndex];
    final now = DateTime.now().toUtc();
    final fresh = _upcomingFromCache(target.id, now);

    if (_degraded != _Degraded.none) {
      // Playing the fallback of an exhausted cache: real programming is back,
      // so leave the fallback for it.
      if (fresh.isNotEmpty) unawaited(_switchToChannelIndex(_channelIndex));
      return;
    }

    final onAirNow = fresh.where((e) => !e.startAt.isAfter(now) && e.endAt.isAfter(now)).firstOrNull;
    final playing = channel[_currentIndex].program;
    if (onAirNow != null && (playing == null || !_sameAiring(playing, onAirNow))) {
      // What is on air changed: switch to the new schedule, as a reset would.
      unawaited(_switchToChannelIndex(_channelIndex));
      return;
    }
    if (playing == null) return;

    // Same program on air: refresh what follows it in place. Not while a
    // preloaded controller (or a gap hold) is parked for the old next unit;
    // the next change (or the next sync) tries again.
    final inactive = _aIsActive ? _controllerB : _controllerA;
    if (inactive != null || _gapHoldPending != null || _gapHoldTimer != null) return;
    final curOrdinal = channel[_currentIndex].programOrdinal;
    if (curOrdinal >= _known.length) return;
    // A tail run on air that grew (its window end moved): the clips after the
    // ones already queued are appended below, the clip playing is untouched.
    final grown = onAirNow != null && playing.isTailGroup && onAirNow.isTailGroup && onAirNow.endAt.isAfter(playing.endAt) ? onAirNow : null;
    final curEnd = grown?.endAt ?? playing.endAt;
    final following = fresh.where((e) => !e.startAt.isBefore(curEnd)).toList();
    final held = _known.skip(curOrdinal + 1).toList();
    bool same() {
      if (held.length != following.length) return false;
      for (var i = 0; i < held.length; i++) {
        if (jsonEncode(held[i].toJson()) != jsonEncode(following[i].toJson())) return false;
      }
      return true;
    }

    if (same() && grown == null) return;
    final extra = grown == null ? const <ChannelEntry>[] : unitsOfProgram(grown, curOrdinal, notBefore: playing.endAt);
    _known = [..._known.take(curOrdinal), grown ?? _known[curOrdinal], ...following];
    _knownExpanded = curOrdinal + 1;
    _epgLoadedUntil = _known.map((e) => e.endAt).reduce((a, b) => a.isAfter(b) ? a : b);
    // The units of the programs after the one on air are replaced (the list is
    // a prefix up to the on-air program, so the indexes in use stay valid).
    channel = [...channel.where((u) => u.programOrdinal <= curOrdinal), ...extra];
    await _expandWorkingSet(generation);
  }

  // The cache is exhausted and the client's setting is to hold the slate:
  // crossfades the program that just ended into it and waits, checking for a
  // refreshed schedule (a cache change reconciles at once, the timer is the
  // fallback).
  Future<void> _holdSlateExhausted(int generation) async {
    final outgoing = _aIsActive ? _controllerA : _controllerB;
    final slate = await _createSlateController();
    if (generation != _loadGeneration || !mounted) {
      unawaited(serializeGlOp(() => slate.dispose()));
      return;
    }
    setState(() {
      if (_aIsActive) {
        _controllerB = slate;
      } else {
        _controllerA = slate;
      }
      _status = 'HOLD — schedule unavailable, retrying…';
    });
    await _rampCrossfade(outgoing: outgoing, incoming: slate, generation: generation);
    if (generation != _loadGeneration) return;
    _degraded = _Degraded.slate;
    _startExhaustedRetry(generation);
  }

  // Same, for a channel that had nothing at all to start with (a cold start
  // after the cache ran out): the slate is the only thing playing.
  Future<void> _holdSlateAtLoad(int generation) async {
    final slate = await _createSlateController();
    if (generation != _loadGeneration || !mounted) {
      unawaited(serializeGlOp(() => slate.dispose()));
      return;
    }
    await slate.play();
    if (generation != _loadGeneration || !mounted) {
      unawaited(serializeGlOp(() => slate.dispose()));
      return;
    }
    setState(() {
      _controllerA = slate;
      _controllerB = null;
      _aIsActive = true;
      _opacityA = 1;
      _opacityB = 0;
      _status = 'HOLD — schedule unavailable, retrying…';
    });
    _degraded = _Degraded.slate;
    _startExhaustedRetry(generation);
  }

  void _startExhaustedRetry(int generation) {
    _gapRetryTimer?.cancel();
    _gapRetryTimer = Timer.periodic(epgGapRetryInterval, (t) {
      if (generation != _loadGeneration) {
        t.cancel();
        return;
      }
      unawaited(_reconcileWithCache());
    });
  }

  // What the OSD describes for unit `index` of `channel`: the guide program
  // (never a filler), see osd_subject.dart.
  OsdSubject? _osdSubjectAt(int index) {
    if (index < 0 || index >= channel.length) return null;
    final current = channel[index].program;
    if (current == null) return null;
    // An entry about to air (the crossfade's key check) is read as of its start.
    final now = DateTime.now().toUtc();
    return osdSubjectFor([..._osdContext, ..._known], current, now: now.isBefore(current.startAt) ? current.startAt : now);
  }

  // The entries just before `first`, read from the cache (never the backend),
  // only when `first` is a filler already on air at tune-in, whose program
  // lies before the window. Best effort: without it the OSD attributes the
  // filler to the other neighbour.
  List<EpgEntry> _osdContextFromCache(String channelId, EpgEntry first) {
    if (!isHiddenGuideKind(first.kind)) return <EpgEntry>[];
    try {
      return _cache
          .entries(channelId, first.startAt.subtract(guideContextLookback), first.startAt)
          .where((e) => e.startAt.isBefore(first.startAt) && e.endAt.isAfter(e.startAt))
          .toList();
    } catch (_) {
      return <EpgEntry>[];
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

  /// When the window loses and regains OS focus Flutter drops primary focus
  /// to the root scope, and the key listener below stops receiving keys.
  /// Hand focus back on resume. Deliberately no `setState`: rebuilding this
  /// page risks the GL-texture bug (see AGENTS.md).
  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    _screenAwake.lifecycle(state);
    if (state != AppLifecycleState.resumed || !mounted) return;
    final primary = FocusManager.instance.primaryFocus;
    if (primary != null && primary is! FocusScopeNode) return;
    if (ModalRoute.of(context)?.isCurrent == false) return;
    _focusNode.requestFocus();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _screenAwake.dispose();
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
    _preloadTimer?.cancel();
    _channelInputCommitTimer?.cancel();
    unawaited(_cacheSub?.cancel());
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
  Rect _videoThumbnailRect(Size screenSize) {
    // The same factor the EPG overlay reads from UiScale (the host resolves it
    // the same way); computed here so the page does not subscribe to it.
    final k = resolveUiScale(screenSize, override: UiScaleOverride.notifier.value);
    return Rect.fromLTWH(
      screenSize.width - (epgThumbnailMargin + epgThumbnailWidth) * k,
      epgThumbnailMargin * k,
      epgThumbnailWidth * k,
      epgThumbnailHeight * k,
    );
  }

  @override
  Widget build(BuildContext context) {
    final channelLabel = _channels.isEmpty
        ? '(loading channels)'
        : 'Channel ${_channels[_channelIndex].number} — ${_channels[_channelIndex].name}';
    // While there's no loaded schedule (initial bootstrap, or right after
    // switching channel) the only visible feedback is the plain debug text —
    // the real OSD needs a valid `channel`/`_currentIndex`.
    final hasContent = channel.isNotEmpty && _currentIndex >= 0 && _currentIndex < channel.length;
    final osd = hasContent ? _osdSubjectAt(_currentIndex) : null;
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
        backgroundColor: playerBackgroundColor,
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
            if (!hasContent && !_status.startsWith('ERROR') && !_status.startsWith('HOLD'))
              const Positioned.fill(
                child: Center(
                  child: SizedBox(
                    width: 56,
                    height: 56,
                    child: CircularProgressIndicator(key: Key('loading-indicator'), color: Colors.white, strokeWidth: 4),
                  ),
                ),
              ),
            if (!hasContent && (_status.startsWith('ERROR') || _status.startsWith('HOLD')))
              Positioned(
                left: 16,
                top: 16,
                child: ConstrainedBox(
                  // Long errors (full URLs, OS messages) wrap inside the
                  // screen instead of running off it.
                  constraints: BoxConstraints(maxWidth: MediaQuery.sizeOf(context).width - 32),
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
                    itemLabel: osd?.program.displayTitle ?? channel[_currentIndex].programLabel,
                    nextItemLabel: osd?.next?.displayTitle,
                    startAt: osd?.start ?? channel[_currentIndex].programStartAt,
                    endAt: osd?.end ?? channel[_currentIndex].programEndAt,
                    progressByClock: osd != null,
                    resolveSubject: () => _osdSubjectAt(_currentIndex),
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
                    cache: _cache,
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

