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
import 'dart:math' as math;
import 'dart:ui' show PlatformDispatcher;

import 'package:flutter/foundation.dart' show ValueListenable;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:path_provider/path_provider.dart';
import 'video/playback_capability.dart';
import 'video/video_engine.dart';

import 'cache/offline_runtime.dart';
import 'cache/schedule_cache.dart';
import 'cache/stream_cache.dart';
import 'cache/schedule_sync.dart';
import 'config/config_file.dart';
import 'config/connection_settings.dart';
import 'log/log.dart';
import 'log/log_setup.dart';
import 'update/app_update.dart';
import 'update/compat.dart';
import 'channel_switch.dart';
import 'widgets/channel_banner.dart';
import 'widgets/channel_logo.dart';
import 'widgets/unplayable_notice.dart';
import 'widgets/update_overlay.dart';
import 'mosaic_layout.dart' show defaultMosaicSafeMargin;
import 'ui/form_factor.dart';
import 'ui/ui_scale.dart';
import 'playhead_api.dart';
import 'services/perf_trace.dart';
import 'services/screen_awake.dart';
import 'services/system_bars.dart';
import 'services/settings_service.dart';
import 'services/loudness_probe.dart';
import 'services/loudness_service.dart';
import 'services/audio_preamp.dart';
import 'services/app_window.dart';
import 'services/stream_preferences_service.dart';
import 'services/stream_selection_resolver.dart';
import 'input/back_key_guard.dart';
import 'input/input_action.dart';
import 'pages/mosaic_page.dart';
import 'pages/pairing_page.dart';
import 'pages/server_url_page.dart';
import 'services/device_identity.dart';
import 'services/pairing_boot.dart' show BootRoute, ServerUrlResolver, decideBootRoute;
import 'util/controller_lifecycle.dart';
import 'util/open_retry.dart';
import 'epg_guide.dart' as guide show defaultTailAttributionCap, tailAttributionCap;
import 'epg_guide.dart' show buildGuideCells, guideContextLookback, isHiddenGuideKind;
import 'osd_subject.dart';
import 'playback_promotion.dart';
import 'playback_stall.dart';
import 'preload_lead.dart';
import 'clock.dart';
import 'plugin/channel_filter.dart';
import 'dimmer.dart';
import 'epg_thumbnail.dart';
import 'widgets/epg_overlay.dart';
import 'widgets/osd_overlay.dart';
import 'widgets/player_gestures.dart';
import 'widgets/debug_overlay.dart';
import 'widgets/track_selector_overlay.dart';
import 'widgets/clock_overlay.dart';
import 'widgets/dimmer_overlay.dart';
import 'widgets/dimmer_level_dialog.dart';
import 'widgets/watermark_overlay.dart';
import 'watermark.dart';

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
  static Duration? _channelLoadStallBound;
  // The config file's settings (lib/config/config_file.dart), read once at
  // boot by loadConfigFile. A packaged install configures itself here; each
  // getter below falls back to it when neither a test override nor a
  // --dart-define is set.
  static Map<String, Object?> _file = const {};
  // What was typed into the setup screen (SettingsService.connection): the
  // lowest-ranked source, so a config file or a build's --dart-define wins.
  static ConnectionSettings _stored = const ConnectionSettings();

  /// Reads the config file (PLAYHEADTV_CONFIG, else the XDG config
  /// directory); called once by main() before runApp. An absent or invalid
  /// file is an empty config.
  static void loadConfigFile([Map<String, String>? env]) {
    _file = readConfigFile(configFileLocation(env ?? Platform.environment));
  }

  /// Sets the settings typed into the setup screen; called at boot and after a save.
  static void applyStoredConnection(ConnectionSettings value) {
    _stored = value;
    Log.setSecrets([jellyfinApiKey, playheadDeviceToken]);
  }

  /// The file's value when it has one, else the typed one.
  static Map<String, Object?> get _withStored => {
        ..._stored.toStored(),
        for (final e in _file.entries)
          if (!(e.value is String && (e.value as String).isEmpty)) e.key: e.value,
      };

  /// True when the player has what it needs to start (media server, key, backend).
  static bool get isConnectionConfigured =>
      jellyfinUrl.isNotEmpty && jellyfinApiKey.isNotEmpty && playheadBaseUrl.isNotEmpty;

  /// The config file's settings as read at boot (for the logger's level).
  static Map<String, Object?> get fileSettings => _file;

  static String get jellyfinUrl => resolveStringSetting(
      override: _jellyfinUrl, define: const String.fromEnvironment('JELLYFIN_URL'), file: _withStored, key: 'jellyfin_url');
  static String get jellyfinApiKey => resolveStringSetting(
      override: _jellyfinApiKey,
      define: const String.fromEnvironment('JELLYFIN_API_KEY'),
      file: _withStored,
      key: 'jellyfin_api_key');
  // Playhead TV backend (Go) that materializes the schedule — see docs/ARCHITECTURE.md.
  static String get playheadBaseUrl => resolveStringSetting(
      override: _playheadBaseUrl,
      define: const String.fromEnvironment('PLAYHEADTV_BASE_URL'),
      file: _withStored,
      key: 'playhead_base_url');
  // Optional: device token for the Auth flow. Empty = no Authorization header
  // is sent, which matches a backend running without auth.
  static String get playheadDeviceToken => resolveStringSetting(
      override: _playheadDeviceToken,
      define: const String.fromEnvironment('PLAYHEADTV_DEVICE_TOKEN'),
      file: _withStored,
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
  // Which video engine plays (see video/video_engine.dart): the
  // PLAYHEADTV_VIDEO_ENGINE environment variable, else `video_engine` in the
  // config file. Null when neither is set: the choice saved in Settings applies,
  // else the default engine.
  static String? get videoEngineSetting {
    final fromEnv = Platform.environment['PLAYHEADTV_VIDEO_ENGINE'];
    if (fromEnv != null && fromEnv.isNotEmpty) return fromEnv;
    final fromFile = _file['video_engine'];
    return fromFile is String && fromFile.isNotEmpty ? fromFile : null;
  }

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

  // The dim level when the user never chose one in Settings:
  // `dim_level_percent` in the config file (10 to 80, steps of 10), else 50.
  static int get dimLevelDefaultPercent => sanitizeDimLevelPercent(_file['dim_level_percent']);

  // The dim level in force: the user's Settings choice, else the installation
  // default.
  static int configuredDimLevel(SettingsService settings) => settings.dimLevelPercent ?? dimLevelDefaultPercent;

  // Whether the dimmer comes back on at launch: only if the viewer left it on.
  static bool configuredDimActive(SettingsService settings) => settings.dimActive ?? false;

  // The configured preload lead (before the adaptive growth): a test override
  // (not clamped, so a test can shrink it), else the user's Settings choice,
  // else the installation default.
  static Duration configuredPreloadLead(SettingsService settings) =>
      _preloadLead ?? Duration(seconds: settings.preloadLeadSeconds ?? preloadLeadDefaultSeconds);

  // Leveling: measure each piece of audio on the device and pull the player's
  // volume down to a common target (`loudness_enabled`/`loudness_target_lufs`
  // in the config file). On by default: without it a channel change between a
  // quiet film and a loud cartoon is a 9 dB jump.
  static bool get loudnessEnabled => resolveBoolSetting(
        override: null,
        envValue: const String.fromEnvironment('LOUDNESS_LEVELING'),
        file: _file,
        key: 'loudness_enabled',
        fallback: true,
      );

  static double get loudnessTargetLufs => readLoudnessTarget(_file);

  // The most the app lifts any item, in dB (the boost itself follows the item:
  // loudness_service.dart). This is the only gain above unity the app has — the
  // player's volume stops at 1 — so it is what makes the quiet end of the
  // library reachable at all.
  static double get loudnessMaxBoostDb => readLoudnessMaxBoost(_file);

  // Base of the preload retry back-off, shrunk by tests.
  static Duration get preloadRetryBase => _preloadRetryBase ?? defaultPreloadRetryBase;

  // How long a channel load may run before a further switch stops waiting
  // for it (see _switchToChannelIndex); shrunk by tests.
  static Duration get channelLoadStallBound => _channelLoadStallBound ?? defaultChannelLoadStallBound;

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
    Duration? channelLoadStallBound,
  }) {
    // Not part of the app's config: the guide's tail cap, shrunk by tests so a
    // tail goes past it within seconds. Every override resets it.
    guide.tailAttributionCap = tailAttributionCap ?? guide.defaultTailAttributionCap;
    // A new configuration is a new app: forget the previous run's cache,
    // syncer and shared runtime. Without an explicit directory each override
    // gets a fresh one, so a test never inherits another's cache.
    OfflineRuntime.reset();
    Dimmer.reset();
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
    _channelLoadStallBound = channelLoadStallBound;
    _cacheDir = cacheDir ?? Directory.systemTemp.createTempSync('playheadtv_cache_').path;
  }
}

// Programs kept resolved and ready ahead of the one on air (the working set),
// see AppConfig.workingSetSize. A grouped tail counts as ONE program however
// many clips it carries.
const defaultWorkingSetSize = 5;

// How long a channel load runs before a newer switch stops waiting for it and
// loads its own target in parallel. Shorter than that, presses in quick
// succession are still coalesced into one load (see _switchToChannelIndex).
const defaultChannelLoadStallBound = Duration(seconds: 2);

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
  /// The unit's video file, when the guide said what it is: the player asks the
  /// device whether it can decode it before it builds a player for it (see
  /// lib/video/playback_capability.dart). Null means "nobody knows", which is
  /// played, never refused.
  final String? videoCodec;
  final int? width;
  final int? height;
  // Where in the media item this unit starts playing (epg_entry
  // item_offset_ms: nonzero only for the later segments of a split item).
  // The unit plays from here for `clipDuration`, so every seek into it (a
  // preload, a tune-in in the middle, a resync) is relative to this offset.
  final Duration itemOffset;
  const ChannelEntry(this.id, this.label, this.clipDuration, this.startAt,
      {this.programOrdinal = 0,
      this.program,
      this.videoCodec,
      this.width,
      this.height,
      this.itemOffset = Duration.zero});

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
          programOrdinal: ordinal,
          program: program,
          videoCodec: program.videoCodec,
          width: program.width,
          height: program.height,
          itemOffset: Duration(milliseconds: program.itemOffsetMs)),
    ];
  }
  return [
    for (final clip in program.clips)
      if (clip.externalId != null &&
          clip.endAt.isAfter(clip.startAt) &&
          (notBefore == null || clip.endAt.isAfter(notBefore)))
        ChannelEntry(clip.externalId!, clip.title ?? program.displayTitle, clip.endAt.difference(clip.startAt), clip.startAt,
            programOrdinal: ordinal,
            program: program,
            videoCodec: clip.videoCodec,
            width: clip.width,
            height: clip.height),
  ];
}

/// The unit the channel opens with, and the position inside it, from what the
/// cache already knows of the channel.
///
/// This is the same choice `_loadAndPlayChannel` makes for its head unit (the
/// program on air, expanded by [unitsOfProgram], joined where it already
/// started), so the boot prefetch asks the backend for exactly the url that
/// playback is about to need. Null when nothing is playable yet: the cache
/// knows nothing, or the first program already ended.
({ChannelEntry entry, Duration seek, Duration position})? headUnitOf(List<EpgEntry> upcoming, DateTime now) {
  if (upcoming.isEmpty) return null;
  final units = unitsOfProgram(upcoming.first, 0, notBefore: now);
  if (units.isEmpty) return null;
  final head = units.first;
  var seek = now.difference(head.startAt);
  if (seek < Duration.zero) seek = Duration.zero;
  if (seek > head.clipDuration) seek = head.clipDuration;
  return (entry: head, seek: seek, position: head.itemOffset + seek);
}

/// Resolves the url of the item the app is about to open, before the first
/// frame.
///
/// On a plugin-backed channel the expensive half of a channel start is one
/// backend call: `GET /catalog-items/{id}/play` waits for an extractor run
/// (250-350 ms when the plugin's own cache is warm, seconds when it is not).
/// At boot everything that call needs is already known without the network —
/// prefs say which channel, the guide is on disk — so it can start in parallel
/// with the engine start, the first frame and `runApp` instead of after them.
/// The answer goes into [StreamCache], which is what the player's own
/// `_entrySource` reads, so the work is reused, not just warmed up.
///
/// Deliberately narrow: only the channel the app goes straight into (a mosaic
/// start has no route yet), only its head unit, only when that unit needs a
/// plugin at all (a media-server item is a plain url needing no resolve), and
/// never awaited or retried — anything that fails just loads the old way a
/// moment later. The usual mode order is used, so `pair` is asked for first and
/// a prefetch does not open a remux process on the host for nothing.
Future<void> prefetchFirstChannel() async {
  final started = Stopwatch()..start();
  try {
    final settings = await SettingsService.create();
    final channelId = settings.lastWatchedChannelId;
    if (channelId == null) return; // the mosaic is the first screen: no route yet
    final runtime = await OfflineRuntime.ensure();
    final now = DateTime.now().toUtc();
    final upcoming = runtime.cache
        .entries(channelId, now, runtime.cache.horizonEnd)
        .where((e) => e.endAt.isAfter(now) && e.endAt.isAfter(e.startAt))
        .toList();
    final head = headUnitOf(upcoming, now);
    final program = head?.entry.program;
    final itemId = program?.catalogItemId;
    if (head == null || program == null || itemId == null || itemId.isEmpty) return;
    final requires = program.requires;
    if (requires.isEmpty) return;
    if (StreamCache.instance.lookup(itemId, head.position) != null) return;
    final stream = await runtime.api
        .playUrl(itemId, startAt: head.position, modes: pluginPlaybackModes(videoEngine))
        .timeout(const Duration(seconds: 30));
    StreamCache.instance.store(itemId, head.position, stream);
    Log.info('player', 'boot prefetch: item ${head.entry.id} resolved by "${requires.first.id}" in '
        '${started.elapsedMilliseconds}ms (start=${head.position.inSeconds}s, mode=${stream.mode})');
  } on Object catch (e) {
    Log.debug('player', 'boot prefetch did nothing (${started.elapsedMilliseconds}ms)', error: e);
  }
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

// Leveling. Five seconds of audio is what the measurement decodes: on the box
// that costs about 2.8 s of wall clock, and a shorter window stops being about
// the content and starts being about one sound effect.
const gainMeasureWindow = loudnessMeasureStandardWindow;

// How far apart the two windows of one item may be before the item is left
// unleveled. Within one item the head and a mid-programme window differ by 2.6
// dB typically and 9.8 dB at p90; past this the two are not describing the same
// audio (a malformed file measured 14 dB apart at two offsets), and a gain
// derived from either would be a guess.
const loudnessWindowDisagreementDb = 6.0;
// The correction is a fade, not a step: long enough to be inaudible as a change,
// short enough that two levels are not both remembered.
const gainRampDuration = Duration(seconds: 4);
// A correction that moves the volume up from a guess is a different thing: the
// guess is an estimate, so getting to the truth quickly is better than a slow
// swell that keeps the channel quiet for seconds after it aired.
const gainRampUpDuration = Duration(seconds: 2);
// Below this a correction is not worth a fade.
const gainRampMinDeltaDb = 1.0;

// Last-resort floor under _scheduleTransitions' `effective` duration. The
// GET /epg filters in _loadAndPlayChannel/_doFetchNextEpgSlice (and
// pullsync's own sync-time guard) should already keep a non-positive-duration
// item out of `channel` entirely, but if one ever slips through anyway,
// _scheduleTransitions would otherwise fire its next-item Timer immediately,
// churning real controller create/dispose cycles fast enough to exhaust file
// descriptors (reproduced 2026-09-30 — see AGENTS.md). This never changes
// normal playback: every real clip duration is comfortably above it.
const minTransitionInterval = Duration(seconds: 5);

/// How many units the player gives up on before it reports that the channel
/// cannot start at all. A 24/7 channel must not die because one item of its
/// guide became unplayable: the source behind an item can disappear (a removed
/// video, an extractor the site now challenges), and the honest answer is to
/// keep the channel on air with the next program it has.
const int channelLoadSkips = 3;

// How far `now` may drift from a ChannelEntry's real `startAt` (either way)
// before _crossfadeTo treats it as something to correct, rather than normal
// jitter (network latency, the ~30-step crossfade ramp itself, preload
// timing slop). Below this, items keep playing back to back exactly as
// before issue #9.
const epgSyncTolerance = Duration(seconds: 2);

/// Minimum time between two focus steps while an arrow is held in the EPG.
const epgHoldRepeatInterval = Duration(milliseconds: 90);

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

// What this client's player can do with a plugin's answer (docs/PLUGINS.md
// "Modes"). `pair` is missing on purpose: the engine seam still opens one url,
// so asking for two tracks would get an answer nothing can play. `remux` is the
// plugin feeding a stream it copies, never transcodes, which is what makes a
// plugin channel playable here at all.


String streamUrl(String itemId) =>
    '${AppConfig.jellyfinUrl}/Videos/$itemId/stream?Static=true&ApiKey=${AppConfig.jellyfinApiKey}';

// Poster URL built from the media server address and key the client already
// holds and the image tag the backend sent (the backend never serves art).
String jellyfinImageUrl(String itemId, String tag, {int maxWidth = 400}) =>
    '${AppConfig.jellyfinUrl}/Items/$itemId/Images/Primary?tag=$tag&maxWidth=$maxWidth&ApiKey=${AppConfig.jellyfinApiKey}';

/// Puts the window in (or out of) full screen per the saved choice, else the
/// installation's default. Runs before `runApp`, so there is no visible resize:
/// the runner shows its window on the first Flutter frame. A no-op where the
/// platform has no resizable window (Android).
Future<void> applyStartupFullScreen() async {
  final settings = await SettingsService.create();
  await FullScreenController.shared.apply(settings.startFullScreen(AppConfig.startFullScreenDefault));
}

/// Where the log file lives on a platform with no state directory of its own.
/// Android and iOS have no `PLAYHEADTV_ROOT` and no XDG state dir, so the log
/// stayed in memory and died with the process — the one moment it is wanted. The
/// app's support directory is `files/`, which `adb shell run-as` can read while
/// the app is still running, and which survives its death on disk.
Future<Directory?> _mobileLogDirectory() async {
  if (!Platform.isAndroid && !Platform.isIOS) return null;
  try {
    return await getApplicationSupportDirectory();
  } catch (e) {
    Log.warn('app', 'no log directory on this device: $e');
    return null;
  }
}

Future<void> main() async {
  BootTrace.mark('main');
  WidgetsFlutterBinding.ensureInitialized();
  AppConfig.loadConfigFile();
  AppConfig.applyStoredConnection((await SettingsService.create()).connection);
  initLogging(
    env: Platform.environment,
    file: AppConfig.fileSettings,
    appVersion: appVersion,
    secrets: [AppConfig.jellyfinApiKey, AppConfig.playheadDeviceToken],
    directory: await _mobileLogDirectory(),
  );
  _installErrorLogging();
  // The offline runtime is opened before the first frame instead of while the
  // channel is being loaded: its SQLite open measured 44-892 ms (warm vs cold
  // page cache) and everything that follows reuses the same instance
  // (`ensure()` hands back the future in flight), so this window runs in
  // parallel with the engine start and the first frame rather than after it.
  // Nothing waits for it; a run that goes to pairing instead has no business
  // opening a schedule cache, so only a configured install prewarms.
  if (AppConfig.isConnectionConfigured) {
    final prewarm = Stopwatch()..start();
    unawaited(OfflineRuntime.ensure().then(
        (_) => Log.info('cache', 'offline runtime opened in ${prewarm.elapsedMilliseconds}ms (boot prewarm)'),
        onError: (Object e) => Log.warn('cache', 'prewarming the offline runtime failed', error: e)));
  }
  // A device with no reachable log file needs the log on screen to be readable
  // at all, so the peek build also records every level.
  if (logPeekEnabled) {
    Log.setRuntimeLevel(LogLevel.debug);
    // The platform console is the only channel a device without a reachable log
    // file has, so a peek build writes there too (debugPrint, not print: the
    // analyzer's avoid_print exists to keep app code off the console).
    Log.mirrorTo(debugPrint);
  }
  await applyStartupFullScreen();
  BootTrace.mark('fullscreen_applied');
  selectVideoEngine(resolveVideoEngineKind(
    AppConfig.videoEngineSetting ?? (await SettingsService.create()).videoEngine,
    isAndroid: Platform.isAndroid,
    onFallback: (reason) => Log.warn('video', 'using fvp: $reason'),
  ));
  Log.info('video', 'engine: ${videoEngine.name}');
  videoEngine.register();
  BootTrace.mark('engine_registered');
  // The channel's own resolve rides along with the start instead of after it
  // (see prefetchFirstChannel): the answer is reused by playback, not wasted.
  if (AppConfig.isConnectionConfigured) unawaited(prefetchFirstChannel());
  runApp(const PlayheadSpikeApp());
  WidgetsBinding.instance.addPostFrameCallback((_) => BootTrace.mark('first_frame'));
  // When the launcher started this process right after an update, confirm
  // the new version once it has stayed up for the healthy window (no-op
  // otherwise); an unconfirmed update is rolled back by the next start.
  scheduleHealthConfirmation();
  unawaited(_warnIfBackendIncompatible());
}

// Routes framework and uncaught errors to the log, then lets the default
// handling carry on (the console report, the debugger).
void _installErrorLogging() {
  final previous = FlutterError.onError;
  FlutterError.onError = (details) {
    Log.error('flutter', details.exceptionAsString(), stack: details.stack);
    if (previous != null) previous(details);
  };
  PlatformDispatcher.instance.onError = (error, stack) {
    Log.error('app', 'uncaught error', error: error, stack: stack);
    return false;
  };
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
  Log.warn('app', 'backend version not supported: $message');
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
      // The form-factor host is ABOVE the UI scale host because the scale
      // depends on the form factor.
      builder: (context, child) => ClientFormFactorHost(
        child: UiScaleHost(child: BackKeyGuard(child: UpdateOverlayHost(child: child ?? const SizedBox.shrink()))),
      ),
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

  // The page a configured install opens on: straight to the last watched
  // channel when there is one, else the mosaic.
  static Widget _homePage(SettingsService settings) {
    final lastWatched = settings.lastWatchedChannelId;
    return lastWatched != null ? ChannelPlayerPage(initialChannelId: lastWatched) : const MosaicPage();
  }

  Future<void> _resolve() async {
    final settings = await SettingsService.create();
    if (!mounted) return;
    await _resolveFormFactor(MediaQuery.sizeOf(context));
    if (!mounted) return;
    // First-run routing lives in pairing_boot.dart so the decision is pure Dart
    // and unit tested: a complete connection goes straight home, a known server
    // URL goes to quick-connect pairing, otherwise we ask the user to type one.
    final serverUrl = ServerUrlResolver.knownServerUrl(storedBackendUrl: settings.chosenServerUrl);
    final route = decideBootRoute(configured: AppConfig.isConnectionConfigured, serverUrl: serverUrl);
    if (route == BootRoute.pairing) {
      Navigator.of(context).pushReplacement(MaterialPageRoute(
        builder: (_) => PairingPage(
          serverUrl: serverUrl,
          // Clear the stack: the pairing page may have a secondary screen above it.
          onDone: (doneContext) => Navigator.of(doneContext)
              .pushAndRemoveUntil(MaterialPageRoute(builder: (_) => _homePage(settings)), (route) => false),
        ),
      ));
      return;
    }
    if (route == BootRoute.serverUrl) {
      Navigator.of(context).pushReplacement(MaterialPageRoute(builder: (_) => const ServerUrlPage()));
      return;
    }
    Navigator.of(context).pushReplacement(MaterialPageRoute(builder: (_) => _homePage(settings)));
  }

  // The layout family for the whole process, decided before any page builds
  // and never again (lib/ui/form_factor.dart explains why). On Android the
  // answer needs the platform channel that knows a television from a phone;
  // the wait is bounded so a wedged channel delays boot by nothing measurable
  // and falls back to the layout the app has always drawn. No-op elsewhere.
  static Future<void> _resolveFormFactor(Size size) async {
    final started = DateTime.now();
    final identity = await DeviceIdentity.detect()
        .timeout(const Duration(seconds: 2), onTimeout: () => (isTv: false, model: null));
    final formFactor = resolveFormFactor(
      size: size,
      isTelevision: identity.isTv,
      isMobilePlatform: Platform.isAndroid || Platform.isIOS,
    );
    ClientFormFactorState.notifier.value = formFactor;
    Log.info('boot', 'form factor ${formFactor.name} in ${DateTime.now().difference(started).inMilliseconds}ms');
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

/// On a device with no reachable log file (a television), the in-memory log is
/// the only evidence of a failure. A build with
/// `--dart-define=PLAYHEADTV_LOG_PEEK=true` draws its last lines over the
/// picture; without the define this widget is never built and the app behaves
/// exactly as a release does.
const bool logPeekEnabled = bool.fromEnvironment('PLAYHEADTV_LOG_PEEK');

class _LogPeek extends StatefulWidget {
  const _LogPeek({required this.visible});

  /// Whether the lines are drawn: the OSD's Logs button toggles it while the
  /// log keeps recording. A listenable, not a bool, because this widget is
  /// built once and kept (AGENTS.md, "Client app structure").
  final ValueListenable<bool> visible;

  @override
  State<_LogPeek> createState() => _LogPeekState();
}

class _LogPeekState extends State<_LogPeek> {
  Timer? _timer;
  List<String> _lines = const <String>[];

  @override
  void initState() {
    super.initState();
    _timer = Timer.periodic(const Duration(seconds: 1), (_) {
      // Hidden: no rebuild and no tail read. The log itself is untouched.
      if (!mounted || !widget.visible.value) return;
      setState(() => _lines = Log.tail(7));
    });
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Positioned(
      left: 8,
      right: 8,
      bottom: 8,
      child: ValueListenableBuilder<bool>(
        valueListenable: widget.visible,
        builder: (BuildContext context, bool visible, Widget? _) {
          if (!visible) {
            return const SizedBox.shrink();
          }
          return IgnorePointer(
            child: Container(
              color: const Color(0xDD000000),
              padding: const EdgeInsets.all(6),
              child: DefaultTextStyle(
                style: const TextStyle(color: Colors.white, fontSize: 11, height: 1.2),
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: <Widget>[
                    for (final String line in _lines) Text(line, maxLines: 2, overflow: TextOverflow.ellipsis),
                  ],
                ),
              ),
            ),
          );
        },
      ),
    );
  }
}

class _ChannelPlayerPageState extends State<ChannelPlayerPage> with WidgetsBindingObserver {
  // Each slot holds the VideoPlayback of the item that is playing
  // (active) or being preloaded (inactive). Unlike media_kit, video_player/fvp
  // doesn't reuse the same instance to switch media — every new item is a new
  // VideoPlayerController, disposed when it leaves the scene.
  VideoPlayback? _controllerA;
  VideoPlayback? _controllerB;

  bool _aIsActive = true;
  double _opacityA = 1;
  double _opacityB = 0;
  int _currentIndex = -1;

  /// Where the item on air began playing inside itself, when the source already
  /// started there (a plugin stream). The engine counts from that stream's own
  /// zero, so the OSD adds this back — without it a viewer who tuned in 20
  /// minutes into a program sees a clock stuck at the first seconds.
  Duration _playbackOffset = Duration.zero;
  // The controller that is the picture of `channel[_headIndex]` (set together
  // with `_currentIndex` whenever an item takes over). `_picturePlayhead` reads
  // where the viewer really is in the schedule from it.
  VideoPlayback? _headController;
  int _headIndex = -1;
  String _status = 'starting…';
  // A picture that stops without an error (the source behind it ended) is
  // invisible to the engine: it just waits for frames. `_stallTimer` samples the
  // playing position and `_stallResyncs` bounds how many times the channel is
  // reopened before the failure is reported instead (see playback_stall.dart).
  final StallWatcher _stall = StallWatcher();
  Timer? _stallTimer;
  int _stallResyncs = 0;
  // A stopped position that had media buffered ahead: reported once, not acted on.
  bool _stallBufferedReported = false;
  Timer? _advanceTimer;
  Timer? _volumeRampTimer;
  // Leveling (lib/services/loudness_service.dart): the gain the on-air item is
  // held at, the timer that is walking the volume towards a new one, and the
  // device-side measurement. `_itemGain` is what the crossfade and the end of a
  // fade read, so a level correction never fights a fade.
  final LoudnessProbe _loudnessProbe = const LoudnessProbe();
  final AudioPreamp _preamp = const AudioPreamp();
  LoudnessService? _loudness;
  PreampState? _preampState;
  // Leveling choices in force: the config file/dart-define seeds them, Settings
  // overrides them at boot (there is no config file to edit on Android).
  bool _loudnessOn = AppConfig.loudnessEnabled;
  double _loudnessTarget = AppConfig.loudnessTargetLufs;
  double _loudnessMaxBoost = AppConfig.loudnessMaxBoostDb;
  // The volume the item on air is played at, and the level it was measured at:
  // the boost can move (a quieter item arriving), and the volume has to move
  // with it or the level jumps.
  double _itemGain = 1.0;
  LoudnessLevel? _itemLevel;
  // The volume the gap slate was faded in at (see _holdForGap): the slate has no
  // measurement of its own, so it holds whatever level the channel was at.
  double _slateGain = 1.0;
  Timer? _gainRampTimer;
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

  // Hides the system bars for as long as this page is on screen (a phone has
  // them; a set-top box does not). Driven from initState/dispose/lifecycle
  // only — never from build, and never through setState.
  final SystemBarsController _systemBars = SystemBarsController();

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

  // The on-screen log (log peek builds): a peek build starts with it drawn,
  // because that build exists to show it, and the OSD's Logs button turns it
  // off without stopping the log — the log keeps recording to its file and to
  // the platform console either way.
  final ValueNotifier<bool> _debugLogVisibleNotifier = ValueNotifier(logPeekEnabled);

  // Tier 1 track selector overlay (arrowRight — InputAction.trackSelector):
  // same isolated ValueNotifier pattern as above, for the same GL-texture
  // reason. See widgets/track_selector_overlay.dart.
  final ValueNotifier<bool> _trackPickerOpenNotifier = ValueNotifier(false);

  // The watermark of the entry on air (null: none, also on the gap slate and
  // while a channel loads). Same isolated ValueNotifier pattern as above: it
  // changes with the on-air entry and never rebuilds the video widgets.
  final ValueNotifier<EpgWatermark?> _watermarkNotifier = ValueNotifier(null);
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

  // Holding an arrow in the open EPG moves the focus cell by cell. The platform repeats
  // the key faster than the grid can draw a step, so a repeat is applied at most once per
  // `epgHoldRepeatInterval`; the rest are dropped, which keeps the pace steady instead of
  // queueing moves that would carry on after the key is released.
  final Stopwatch _epgLastStep = Stopwatch();

  static bool _isEpgArrow(LogicalKeyboardKey key) =>
      key == LogicalKeyboardKey.arrowUp ||
      key == LogicalKeyboardKey.arrowDown ||
      key == LogicalKeyboardKey.arrowLeft ||
      key == LogicalKeyboardKey.arrowRight;

  /// True when a held-arrow repeat may move the focus now (and records the step).
  bool _epgHoldRepeat() {
    if (_epgLastStep.isRunning && _epgLastStep.elapsed < epgHoldRepeatInterval) return false;
    _epgLastStep
      ..reset()
      ..start();
    return true;
  }

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
  // Units this session already failed to start. Only the load path reads it,
  // and only to skip what it knows is broken; cleared on a channel switch, a new
  // channel being a fresh set of sources.
  final Set<String> _unplayable = <String>{};

  /// Why each of them failed, so the card can say it in the words the decoder
  /// used (`no decoder for mpeg4 at 320x240`) instead of a generic line.
  final Map<String, String> _unplayableReason = <String, String>{};
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
  VideoPlayback? _gapHoldPending;

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
  // Times the current channel load for the log only (never read by logic).
  final Stopwatch _loadClock = Stopwatch();
  // Whether _loadAndPlayChannel is currently doing real work (network fetch
  // + controller create) for the current generation. See _switchToChannelIndex.
  bool _channelLoadInFlight = false;
  // The most recent target a channel switch asked for while a real load was
  // already in flight — resolved by _runChannelLoad right after that load
  // finishes (or self-aborts via _loadGeneration). See _switchToChannelIndex.
  int? _pendingChannelSwitchTarget;
  // ---- Channel change: the banner and the delay -----------------------------
  //
  // A channel change does not tear the picture down immediately. The banner
  // announces the target at once, the stream that was playing keeps playing, and
  // the real switch happens `_switchDelay` later (Settings; 3 s by default). Its
  // last seconds are spent loading, and the picture of the channel being left
  // stays on screen through them (`_holdPlayback`), so the change looks instant
  // instead of a spinner. A viewer surfing five channels in three seconds
  // contacts the media server zero times. See docs/ARCHITECTURE.md "Channel
  // change" and lib/channel_switch.dart for the policy.
  Duration _switchDelay = const Duration(seconds: defaultChannelSwitchDelaySeconds);
  Duration _bannerDuration = const Duration(seconds: defaultChannelBannerSeconds);
  BannerCorner _bannerCorner = BannerCorner.topLeft;
  // The switch waiting for its delay to elapse (null: none pending).
  int? _pendingSwitchIndex;
  // The channel the banner announces (and the OSD describes) while the picture
  // still belongs to the one being left.
  PlayheadChannel? _pendingChannel;
  Timer? _switchTimer;
  // What the banner says, and the subject the OSD describes while a channel is
  // announced but has no controller yet (that is what makes the OSD usable over
  // the spinner — it reads the guide, not the stream).
  final ValueNotifier<ChannelBannerData?> _bannerNotifier = ValueNotifier<ChannelBannerData?>(null);
  OsdSubject? _pendingSubject;
  Timer? _bannerHideTimer;
  Timer? _bannerHardTimer;
  // Select's hold (opens the guide directly); a short press keeps opening the
  // OSD.
  Timer? _selectHoldTimer;

  // The generation of the most recent real load and when it started. A load
  // that outlives AppConfig.channelLoadStallBound is stalled (a source that is
  // slow or stuck opening): a newer switch stops waiting for it, see
  // _switchToChannelIndex.
  int _channelLoadGeneration = -1;
  DateTime _channelLoadStartedAt = DateTime.fromMillisecondsSinceEpoch(0);
  // Fires when the in-flight load turns stalled while a switch is deferred
  // behind it.
  Timer? _stallReleaseTimer;

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
    _systemBars.player = true;
    _osdVisibleNotifier.addListener(() {
      if (!_osdVisibleNotifier.value) {
        _osdFocusNotifier.value = false;
        _osdArmed = false;
      }
    });
    if (AppConfig.jellyfinUrl.isEmpty || AppConfig.jellyfinApiKey.isEmpty || AppConfig.playheadBaseUrl.isEmpty) {
      _status = 'ERROR: the media server and the Playhead TV backend are not configured. '
          'Set them in Settings > Connection (or via the config file / --dart-define).';
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
    Log.info('player', 'boot: loading channels');
    BootTrace.mark('player_init');
    try {
      _settings = await SettingsService.create();
      // Channel change policy (Settings -> Playback): the delay before the picture
      // changes, how long the banner stays once it does, and the corner.
      _switchDelay = Duration(
          seconds: _settings.channelSwitchDelaySeconds ?? defaultChannelSwitchDelaySeconds);
      _bannerDuration = Duration(seconds: _settings.channelBannerSeconds ?? defaultChannelBannerSeconds);
      _bannerCorner = BannerCorner.fromName(_settings.channelBannerCorner);
      _loudnessOn = _settings.loudnessEnabled ?? _loudnessOn;
      _loudnessTarget = _settings.loudnessTargetLufs ?? _loudnessTarget;
      _loudnessMaxBoost = _settings.loudnessMaxBoostDb ?? _loudnessMaxBoost;
      _keyBindings = KeyBindingsService(_settings);
      _loudness = await LoudnessService.create();
      unawaited(_applyPreamp());
      Dimmer.level.value = AppConfig.configuredDimLevel(_settings);
      Dimmer.active.value = AppConfig.configuredDimActive(_settings);
      EpgThumbnailSetting.enabled.value = _settings.epgThumbnail;
      ClockOverlaySettings.alwaysVisible.value = _settings.clockAlwaysVisible;
      ClockOverlaySettings.opacityPct.value = _settings.clockOpacityPct;
      ClockOverlaySettings.scale.value = _settings.clockScale;
      _debugLogVisibleNotifier.value = _settings.debugLogVisible ?? logPeekEnabled;
      _runtime = await OfflineRuntime.ensure();
      BootTrace.mark('runtime_open');
      _streamPrefs = await StreamPreferencesService.create(_runtime.api);
      _streamPrefsReady = true;
      // Which channel is tuned: the one asked for (last watched, or the dart-define),
      // else the first.
      final wantedId = widget.initialChannelId ??
          (AppConfig.playheadChannelIdDefine.isNotEmpty ? AppConfig.playheadChannelIdDefine : null);
      int pickIndex(List<PlayheadChannel> list) {
        final found = wantedId == null ? -1 : list.indexWhere((c) => c.id == wantedId);
        return found >= 0 ? found : 0;
      }

      // The channel asked for is known to the cache (or none was asked for, so the first
      // one plays). A channel the cache has never heard of, a new one for instance,
      // needs the refresh first: playing another channel in its place would be wrong.
      bool knowsWanted(List<PlayheadChannel> list) => wantedId == null || list.any((c) => c.id == wantedId);

      // Tier 2-4 stream rules ride alongside the startup refresh (a failure keeps the
      // cached rules, see StreamPreferencesService).
      final streamPrefsRefresh = _streamPrefs.refreshAll();
      var channels = _cache.channels();
      // A warm start plays from the cache at once: the guide on disk already covers
      // the channel, and a refresh that brings something different is applied by
      // `_onCacheChange` (reload when what is on air changed, else in place), so the
      // network is never on the way to the picture. Only a first run, with nothing
      // cached for the channel yet, has to wait for the backend.
      final warm = channels.isNotEmpty &&
          knowsWanted(channels) &&
          _upcomingFromCache(channels[pickIndex(channels)].id, DateTime.now().toUtc()).isNotEmpty;
      if (!warm) {
        await _runtime.syncer.syncNow().timeout(startupSyncWait, onTimeout: () => SyncOutcome.offline);
        BootTrace.mark('sync_done');
        channels = _cache.channels();
        if (channels.isEmpty) {
          // Nothing cached yet (first run): the backend is the only source.
          await _runtime.syncer.syncNow();
          channels = _cache.channels();
        }
      }
      // The rules are only waited for when none were ever fetched (a first run): a
      // cached copy is what the first playback decision reads, and the refresh
      // updates it for the next one.
      if (!warm || !_streamPrefs.hasCachedRules) await streamPrefsRefresh;
      BootTrace.mark(warm ? 'warm_start' : 'stream_prefs_done');
      _streamPrefs.startPolling();
      _cacheSub = _cache.changes.listen(_onCacheChange);
      // A channel whose plugin this device does not have is left out: offering it
      // would only produce an error screen (lib/plugin/channel_filter.dart).
      final playable = playableChannels(_cache, channels, _runtime.availability);
      if (playable.allHiddenByPlugins) {
        Log.warn('player', 'boot: every channel needs a plugin this device lacks');
      }
      channels = playable.visible;
      if (channels.isEmpty) {
        Log.error('player', 'boot: no channels available (sync ok=${_runtime.syncer.status.value.lastSucceeded})');
        if (playable.allHiddenByPlugins) {
          if (!mounted) return;
          setState(() => _status = 'ERROR: no channel can play on this device yet. '
              '${playable.reasons.join(' ')} ${playable.nextStep ?? ''}'.trimRight());
          return;
        }
        if (!mounted) return;
        final error = _runtime.syncer.status.value.lastError;
        setState(() => _status = _runtime.syncer.status.value.lastSucceeded
            ? 'ERROR: no channels configured on the backend (GET /channels is empty)'
            : 'ERROR: failed to load channel list: $error');
        return;
      }
      final sorted = channels;
      final index = pickIndex(sorted);
      if (!mounted) return;
      setState(() {
        _channels = sorted;
        _channelIndex = index;
      });
      await _loadAndPlayChannel(sorted[index], generation: _loadGeneration);
      unawaited(_settings.recordWatched(sorted[index].id));
    } catch (e, st) {
      Log.error('player', 'boot: failed to load the channel list', error: e, stack: st);
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
    // While a change is announced but its delay has not run out, the next press
    // walks from the channel the viewer asked for, not from the one still on
    // screen: five quick presses cross five channels instead of landing on the
    // same neighbour again.
    final next = ((_pendingSwitchIndex ?? _channelIndex) + delta) % _channels.length;
    final normalized = next < 0 ? next + _channels.length : next;
    await _switchToChannelIndex(normalized);
  }

  // Common core of any channel switch, whether by arrow (relative index
  // already resolved by `_switchChannel`) or by typed number (index already
  // resolved by `_commitChannelInput`): cancels everything of the current
  // channel (timers + both controllers, active and preloading), clears the
  // digit buffer (no typing should carry over into the new channel) and
  // fetches the target channel's schedule.
  //
  // With `load: false` it only tears down (the app went to the background): no
  // watched-record, no schedule fetch, and the page is left idle until
  // `_resumePlayback` switches back to `targetIndex`.
  Future<void> _switchToChannelIndex(int targetIndex, {bool load = true}) async {
    final target = _channels[targetIndex];
    // A channel change with a delay: announce it now, move the picture later.
    // Nothing is torn down and nothing is fetched while the delay runs — that is
    // the debounce, and it is why surfing five channels costs the media server
    // nothing (lib/channel_switch.dart, docs/ARCHITECTURE.md "Channel change").
    if (load && _switchDelay > Duration.zero && _currentIndex >= 0 && _activePlayback != null) {
      _armPendingSwitch(targetIndex);
      return;
    }
    _switchTimer?.cancel();
    _switchTimer = null;
    _pendingSwitchIndex = null;
    if (load) unawaited(_settings.recordWatched(target.id));

    final generation = ++_loadGeneration;
    Log.info('player', 'switch to channel ${target.number} (generation $generation)');
    _advanceTimer?.cancel();
    _resetPreload();
    _volumeRampTimer?.cancel();
    _gainRampTimer?.cancel();
    _stallTimer?.cancel();
    _stallTimer = null;
    _stall.reset();
    _clearUnplayableNotice();
    _unplayable.clear();
    _unplayableReason.clear();
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
      unawaited(disposeController(_gapHoldPending!));
      _gapHoldPending = null;
    }
    _pendingExpand = null;
    _degraded = _Degraded.none;
    _reconcilePending = false;
    // A channel change has nothing in common with the level of the item that was
    // on air: inheriting it is how a quiet item (volume near 1.0) handed the next
    // channel several dB too loud until the measurement and the fade caught up.
    // Dropping it here makes the new channel start from its own prior (or from
    // the conservative guess), never from the neighbour's level. Internal item
    // transitions keep inheriting on purpose: there the level in force is what
    // the viewer is hearing.
    _itemGain = 1.0;
    _itemLevel = null;
    _slateGain = 1.0;
    _channelInputCommitTimer?.cancel();
    _channelInputCommitTimer = null;
    // Fire-and-forget, but routed through serializeGlOp (see its doc
    // comment): this generation's own execution doesn't stop here to wait
    // for the native teardown, and _loadAndPlayChannel below can create a
    // brand new controller before it's done — without the shared queue,
    // that create() could run on fvp/libmdk's raster thread while this
    // dispose() is still freeing GL resources on its dispose thread.
    final activeSlot = _activePlayback;
    final otherSlot = _aIsActive ? _controllerB : _controllerA;
    if (activeSlot != null) unawaited(disposeController(activeSlot));
    if (otherSlot != null) unawaited(disposeController(otherSlot));
    _osdVisibleNotifier.value = false;
    _watermarkNotifier.value = null;
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
      _onAir = false;
      _pictureLost.reset();
      _pictureLostReopens = 0;
      _status = load ? 'switching channel…' : 'paused';
    });

    if (!load) {
      _pendingChannelSwitchTarget = null;
      _pendingSwitchIndex = null;
      _switchTimer?.cancel();
      _switchTimer = null;
      _hideBanner();
      return;
    }

    if (_channelLoadInFlight && !_inFlightLoadStalled) {
      // A previous switch's real load (network + controller create) hasn't
      // finished yet — starting a second one now is exactly the
      // rapid-succession case that wastes a full GET /epg + controller
      // create per keypress (immediately discarded by the next press's own
      // generation bump) and made the GL-serialization race easy to hit in
      // the first place (see gl_serial.dart). The visual state above
      // already updated instantly regardless — only the real load is
      // deferred, to whichever target the user last asked for once the
      // in-flight one finishes or self-aborts. A load that stays in flight
      // past AppConfig.channelLoadStallBound is stuck, not rapid pressing:
      // the deferral is released then (see _armStallRelease), so a source
      // that never opens cannot hold navigation hostage.
      _pendingChannelSwitchTarget = targetIndex;
      Log.debug('player', 'switch deferred behind the load in flight (generation $generation)');
      _armStallRelease();
      return;
    }
    _pendingChannelSwitchTarget = null;
    await _runChannelLoad(target, generation);
  }

  bool get _inFlightLoadStalled =>
      DateTime.now().difference(_channelLoadStartedAt) >= AppConfig.channelLoadStallBound;

  // Schedules the release of the switch deferred behind the in-flight load for
  // the moment that load counts as stalled.
  void _armStallRelease() {
    if (_stallReleaseTimer != null) return;
    final remaining = AppConfig.channelLoadStallBound - DateTime.now().difference(_channelLoadStartedAt);
    _stallReleaseTimer = Timer(remaining.isNegative ? Duration.zero : remaining, () {
      _stallReleaseTimer = null;
      final pending = _pendingChannelSwitchTarget;
      if (!mounted || pending == null || !_channelLoadInFlight || pending >= _channels.length) return;
      _pendingChannelSwitchTarget = null;
      Log.warn('player', 'channel load stalled past ${AppConfig.channelLoadStallBound.inMilliseconds}ms; releasing the deferred switch');
      // The stalled load aborts on its next generation check, and a
      // controller it creates late is disposed by _startChannel.
      unawaited(_runChannelLoad(_channels[pending], _loadGeneration));
    });
  }

  // Runs the real (network + controller) load for a channel switch, then
  // immediately re-dispatches to the last target requested while it was
  // running, if any — see the `_channelLoadInFlight` branch above. No
  // `await` sits between clearing `_channelLoadInFlight` and reading/
  // clearing `_pendingChannelSwitchTarget`, so a press that arrives in that
  // gap can only ever be the one this function itself is about to act on
  // (Dart's single-threaded event loop doesn't interleave in the middle of
  // synchronous code) — nothing gets lost or double-fired. A load that a newer
  // one replaced after it stalled finishes without touching that bookkeeping.
  Future<void> _runChannelLoad(PlayheadChannel target, int generation) async {
    _stallReleaseTimer?.cancel();
    _stallReleaseTimer = null;
    _channelLoadInFlight = true;
    _channelLoadGeneration = generation;
    _channelLoadStartedAt = DateTime.now();
    await _loadAndPlayChannel(target, generation: generation);
    Log.debug('player', 'channel load call returned after ${DateTime.now().difference(_channelLoadStartedAt).inMilliseconds}ms (generation $generation, superseded=${generation != _loadGeneration})');
    if (_channelLoadGeneration != generation) return;
    _channelLoadInFlight = false;
    _stallReleaseTimer?.cancel();
    _stallReleaseTimer = null;
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
  // A load with nothing in the cache for the channel is a cold start: the first
  // refresh is already in flight, and its answer is the only thing that can make
  // the channel playable, so this waits longer than a normal probe. Measured on
  // the Android lab against a backend that needs per-item lookups: the refresh
  // took 7.8 s, the 4 s probe called the backend unreachable, the load reported
  // "nothing scheduled in the next 48h" 1.5 s before the guide arrived, and
  // nothing tried again (docs/ROADMAP.md). Twenty seconds is well past that
  // refresh and still bounded, and it is only paid when there is nothing to play.
  static const coldStartSyncBound = Duration(seconds: 20);

  Future<bool> _backendUnreachable({Duration bound = const Duration(seconds: 4)}) async {
    final outcome = await _runtime.syncer.syncNow().timeout(bound, onTimeout: () => SyncOutcome.offline);
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
  // ---------------------------------------------------------------------------
  // Channel change: the delay and the banner.
  // ---------------------------------------------------------------------------

  /// The picture the viewer is watching right now, if any (the active slot).
  VideoPlayback? get _activePlayback => _aIsActive ? _controllerA : _controllerB;

  /// What the guide says is on air on [channelId] at [now], read from the
  /// offline cache: enough to announce a channel before its stream is open.
  OsdSubject? _programOnAir(String channelId, DateTime now) {
    final raw = _cache.entries(channelId, now.subtract(guideContextLookback), now.add(const Duration(hours: 6)));
    if (raw.isEmpty) return null;
    for (final cell in buildGuideCells(raw)) {
      if (!cell.displayStart.isAfter(now) && cell.displayEnd.isAfter(now)) {
        return osdSubjectFor(raw, cell.entry, now: now);
      }
    }
    return null;
  }

  String? _bannerLogoUrl(PlayheadChannel channel) => channelLogoUrl(channel) ?? channel.guideArtUrl;

  /// The banner's contents for [channel], described by [subject] when the guide
  /// knows what is on air.
  ChannelBannerData _bannerData(PlayheadChannel channel, OsdSubject? subject) => ChannelBannerData(
        number: channel.number,
        name: channel.name,
        logoUrl: _bannerLogoUrl(channel),
        programTitle: subject?.program.displayTitle,
        description: subject?.program.overview,
        startAt: subject?.start,
        endAt: subject?.end,
      );

  /// Announces [targetIndex] and arms the switch that actually changes the
  /// picture, [Duration]s from now.
  void _armPendingSwitch(int targetIndex) {
    final target = _channels[targetIndex];
    final subject = _programOnAir(target.id, DateTime.now().toUtc());
    _switchTimer?.cancel();
    _pendingSwitchIndex = targetIndex;
    _pendingChannel = target;
    _pendingSubject = subject;
    _showBanner(_bannerData(target, subject));
    final at = channelSwitchLoadAt(_switchDelay);
    Log.info('player',
        'channel ${target.number} announced; the picture changes in ${_switchDelay.inMilliseconds}ms (load starts in ${at.inMilliseconds}ms)');
    if (at <= Duration.zero) {
      unawaited(_commitPendingSwitch(targetIndex));
      return;
    }
    _switchTimer = Timer(at, () => unawaited(_commitPendingSwitch(targetIndex)));
  }

  /// The delay is over: the load starts now, and the picture of the channel being
  /// left gives way to the spinner (the banner keeps announcing the target).
  ///
  /// Loading *behind* the old picture — the obvious next step, and the one that
  /// would make the change look instant — was implemented and removed: keeping
  /// the outgoing controller alive while the incoming one initialized wedged the
  /// frame pipeline of fvp/libmdk in the one suite that guards a switch right
  /// after a live `setAudioTracks` (its own `pump` never returned; paused,
  /// unrendered or drawn as a third layer, all three variants stalled). A slow
  /// switch is a slow switch; a frozen player is a bug.
  Future<void> _commitPendingSwitch(int targetIndex) async {
    _switchTimer = null;
    if (!mounted || _pendingSwitchIndex != targetIndex) return;
    _pendingSwitchIndex = null;
    await _switchToChannelIndex(targetIndex);
  }

  void _showBanner(ChannelBannerData data) {
    _bannerHideTimer?.cancel();
    _bannerHardTimer?.cancel();
    _bannerNotifier.value = data;
    // A channel that never comes up still loses the banner, and the spinner says
    // the rest; without this a stuck source would leave the card up forever.
    _bannerHardTimer = Timer(bannerHardLifetime(_switchDelay, _bannerDuration), _hideBanner);
  }

  /// The picture is up: the banner goes away [Duration] from now (Settings ->
  /// Channel banner time), instead of staying until the hard cap.
  void _armBannerHide() {
    _bannerHideTimer?.cancel();
    _bannerHideTimer = Timer(_bannerDuration, _hideBanner);
  }

  void _hideBanner() {
    _bannerHideTimer?.cancel();
    _bannerHideTimer = null;
    _bannerHardTimer?.cancel();
    _bannerHardTimer = null;
    if (_bannerNotifier.value != null) _bannerNotifier.value = null;
  }

  /// The new channel is on screen: clean up what only existed for the change.
  void _announceChannel() {
    _pendingSubject = null;
    _pendingChannel = null;
    final channelOnAir = _channels[_channelIndex];
    _showBanner(_bannerData(channelOnAir, _osdSubjectAt(_currentIndex)));
    _armBannerHide();
  }

  Future<void> _loadAndPlayChannel(PlayheadChannel target, {required int generation}) async {
    _loadClock
      ..reset()
      ..start();
    Log.info('player', 'channel load started: channel ${target.number} (generation $generation)');
    try {
      final now = DateTime.now().toUtc();
      // What the OSD describes while this channel's stream is still opening, and
      // what the banner already announced: the guide's program, resolved before
      // any network work, so Select over the spinner opens the real OSD. Cleared
      // by _announceChannel once the picture is up.
      _pendingChannel = target;
      _pendingSubject = _programOnAir(target.id, now);
      // Kicked off in parallel with the work below (never awaited until just
      // before it's needed) so resolving tier 3 (channel admin) rules never
      // adds latency to the channel-load path. _createController itself never
      // awaits anything for this; see _currentChannelRules' doc comment.
      final channelRulesFuture = _streamPrefs.ensureChannelRules(target.id);
      var upcoming = _upcomingFromCache(target.id, now);
      if (upcoming.isEmpty) {
        final coldStart = _cache.allEntries(target.id).isEmpty;
        if (coldStart) Log.info('player', 'cold start: nothing cached for channel ${target.number}, waiting for the guide');
        final unreachable = await _backendUnreachable(bound: coldStart ? coldStartSyncBound : const Duration(seconds: 4));
        if (generation != _loadGeneration) return;
        upcoming = _upcomingFromCache(target.id, now);
        if (upcoming.isEmpty && unreachable) {
          // The cache is exhausted: the client's own setting decides.
          Log.warn('player', 'cache exhausted and the backend is unreachable at load (behavior=${_settings.exhaustedBehavior.name})');
          if (_settings.exhaustedBehavior == ExhaustedBehavior.slate) {
            await _holdSlateAtLoad(generation);
            return;
          }
          upcoming = _loopedEntries(target.id, now);
          if (upcoming.isNotEmpty) _degraded = _Degraded.loop;
        }
      }
      if (upcoming.isEmpty) {
        Log.error('player', 'channel load failed: nothing scheduled in the next ${_cache.window.inHours}h for channel ${target.number}');
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
        Log.error('player', 'channel load failed: nothing playable scheduled for channel ${target.number}');
        if (!mounted) return;
        setState(() => _status = 'ERROR: nothing playable scheduled for channel ${target.number}');
        return;
      }

      // A unit that cannot start is dropped and the next one is tried, a few
      // times at most. The alternative (one error card, channel stopped) leaves a
      // live channel dead until somebody touches the remote because a single item
      // of its guide vanished — the source of an item is not under this app's
      // control, and the guide usually has another program ready.
      var head = loaded;
      Object? lastError;
      StackTrace? lastStack;
      for (var attempt = 0; attempt < channelLoadSkips && head.isNotEmpty; attempt++) {
        // "TV turned on" joins the middle of the program on air if it already
        // started, rather than going back to its beginning — only the head unit
        // gets this seek.
        var initialSeek = now.difference(head.first.startAt);
        if (initialSeek < Duration.zero) initialSeek = Duration.zero;
        if (initialSeek > head.first.clipDuration) initialSeek = head.first.clipDuration;
        if (!mounted || generation != _loadGeneration) return;
        setState(() {
          channel = head;
        });
        // Separate try/catch from the backend fetch above: _startChannel opens
        // the actual video stream (controller.initialize()), a completely
        // different failure mode (media server unreachable, item deleted, its
        // source refusing to resolve, unsupported codec) from "the backend didn't
        // answer".
        try {
          await _startChannel(initialSeek: initialSeek, generation: generation);
          return;
        } catch (e, st) {
          lastError = e;
          lastStack = st;
          if (!mounted || generation != _loadGeneration) return;
          _unplayable.add(head.first.id);
          Log.warn('player', 'unit ${head.first.id} cannot start; skipping to the next one '
              '(attempt ${attempt + 1} of $channelLoadSkips)', error: e);
          head = head.where((entry) => !_unplayable.contains(entry.id)).toList();
        }
      }
      Log.error('player', 'channel load failed: could not start playback after ${_loadClock.elapsedMilliseconds}ms',
          error: lastError, stack: lastStack);
      if (!mounted || generation != _loadGeneration) return;
      setState(() => _status = 'ERROR: could not start playback ($lastError)');
    } catch (e, st) {
      Log.error('player', 'channel load failed: could not load the schedule', error: e, stack: st);
      if (!mounted || generation != _loadGeneration) return;
      setState(() => _status = 'ERROR: failed to load the schedule: $e');
    }
  }

  // A controller whose preparation fails is disposed here, so the callers only
  // ever hold controllers that opened.
  /// Opens [entry], retrying once on failure. A network-backed media library
  /// can answer the first read of a cold file late (a socket timeout) and the
  /// next one fast once its cache is warm, so one more try recovers most of
  /// those without an error screen. Every attempt builds and, on failure,
  /// disposes its own controller (see [_createControllerOnce]).
  Future<({VideoPlayback playback, Duration startOffset})> _createController(ChannelEntry entry,
      {Duration? seekTo}) {
    return retryOpen<({VideoPlayback playback, Duration startOffset})>(
      attempt: (_) => _createControllerOnce(entry, seekTo: seekTo),
      onRetry: (n, e) => Log.warn('player', 'item ${entry.id} failed to open (attempt $n), retrying', error: e),
      // A decoder that does not exist, or refuses to start, will not answer
      // differently 600ms later: retrying only builds and tears down another
      // player (and another MediaCodec) for the same refusal.
      shouldRetry: (e) => e is! UnsupportedMediaException,
    );
  }

  Future<({VideoPlayback playback, Duration startOffset})> _createControllerOnce(ChannelEntry entry,
      {Duration? seekTo}) async {
    if (_unplayable.contains(entry.id)) {
      throw UnsupportedMediaException('this unit already failed on this device', codec: entry.videoCodec);
    }
    // Before anything is built: can this device decode it at all? A program
    // whose format this box has no decoder for is never opened, so the channel
    // shows the reason instead of spending minutes failing to start it
    // (main.dart `_enterUnplayableWait`). An unknown answer plays as before.
    final support = await playbackCapability.check(
      codec: entry.videoCodec,
      width: entry.width,
      height: entry.height,
    );
    if (support == CodecSupport.unsupported) {
      // Remembered on the way out: the transition, the preload and the hard cut
      // all read this set to know a player must not be built for it, and to show
      // the "waiting for the next program" card instead of skipping silently.
      _unplayable.add(entry.id);
      _unplayableReason[entry.id] = 'no decoder for ${entry.videoCodec} at ${entry.width ?? 0}x${entry.height ?? 0}';
      throw UnsupportedMediaException(
        'no decoder for ${entry.videoCodec} at ${entry.width ?? 0}x${entry.height ?? 0}',
        codec: entry.videoCodec,
      );
    }
    // Where this unit plays from, and whether that source already starts at the
    // requested position (a plugin asked for a start offset produces a stream
    // that begins there, so seeking again would skip twice).
    final source = await _entrySource(entry, seekTo: seekTo);
    final controller = trackController(videoEngine.createNetwork(source.uri, audioUri: source.audioUri));
    final clock = Stopwatch()..start();
    Log.debug('player', 'opening item ${entry.id} (seek=${seekTo?.inSeconds ?? 0}s, offset=${entry.itemOffset.inSeconds}s)');
    try {
      final prepared = await _prepareController(controller, entry, seekTo,
          alreadyPositioned: source.startOffset > Duration.zero);
      Log.debug('player', 'item ${entry.id} opened in ${clock.elapsedMilliseconds}ms');
      return (playback: prepared, startOffset: source.startOffset);
    } catch (e) {
      if (isUnsupportedMediaError(e)) {
        // The decoder exists on paper but refused this file (Media3 reports a
        // MediaCodecVideoRenderer error, e.g. OMX error -12 when the box has no
        // free instance). Same outcome for the viewer as a missing decoder, so
        // it takes the same path - and it is remembered, so the retry budget and
        // the preload do not keep asking.
        _unplayable.add(entry.id);
        _unplayableReason[entry.id] = 'the decoder refused this file';
        unawaited(disposeController(controller));
        throw UnsupportedMediaException('the decoder refused this file', codec: entry.videoCodec);
      }
      Log.debug('player', 'item ${entry.id} failed to open after ${clock.elapsedMilliseconds}ms', error: e);
      // Whatever url was used may be the reason: never reuse it for the retry.
      final item = entry.program?.catalogItemId;
      if (item != null && item.isNotEmpty) StreamCache.instance.forget(item, entry.itemOffset + (seekTo ?? Duration.zero));
      unawaited(disposeController(controller));
      rethrow;
    }
  }

  /// True when this unit has no media-server item of its own and can only be
  /// played by asking a plugin for a url (see `_entrySource`).
  bool _needsPlugin(ChannelEntry entry) => (entry.program?.requires ?? const <EpgRequirement>[]).isNotEmpty;

  /// The url this unit plays from, and where inside the item that url begins.
  ///
  /// A media-server item is a plain url. An item whose source is a plugin has no
  /// media-server id at all: the plugin runs where its tools exist (the backend
  /// host), so the client asks the backend for a url and plays that. The url
  /// carries the plugin's own token and expires, and a `remux` stream exists
  /// only while its process runs — but asking twice for the same item starts a
  /// second extractor and a second remux on the host, so an answer is kept for
  /// [pluginStreamReuse] and dropped the moment an open with it fails.
  ///
  /// `startOffset` is non-zero only when the source itself begins at a position
  /// (the plugin starts its stream there). The engine then counts from that
  /// stream's own zero, so everything that reads a position has to add the
  /// offset back — see `_playbackOffset`.
  Future<({Uri uri, Uri? audioUri, Duration startOffset})> _entrySource(ChannelEntry entry,
      {Duration? seekTo}) async {
    final program = entry.program;
    final requires = program?.requires ?? const <EpgRequirement>[];
    if (requires.isEmpty) {
      return (uri: Uri.parse(streamUrl(entry.id)), audioUri: null, startOffset: Duration.zero);
    }

    final runtime = await OfflineRuntime.ensure();
    final position = entry.itemOffset + (seekTo ?? Duration.zero);
    // Not [entry.id]: that is the source's own id (a YouTube video id), which only
    // a plugin understands. The play route is the backend's, and names the catalog
    // row the guide was built from.
    final itemId = program?.catalogItemId;
    if (itemId == null || itemId.isEmpty) {
      throw StateError('entry ${entry.id} needs a plugin but carries no catalog item');
    }
    // The position travels with the request: the plugin starts its stream there,
    // so a viewer who tuned in 20 minutes into a program does not wait for it.
    // An answer may already be here: from a retry of this open, from another
    // tune-in inside [streamPositionBucket], or from the boot prefetch, which
    // resolves the channel the app is about to open before the first frame.
    final cached = StreamCache.instance.lookup(itemId, position);
    if (cached != null) {
      Log.debug('player', 'item ${entry.id} reusing the url resolved '
          '${DateTime.now().difference(cached.at).inSeconds}s ago (start=${cached.requestedAt.inSeconds}s)');
      // The offset is the position the CACHED stream was cut at, never this
      // one: a remux that began a bucket earlier is at most that far behind,
      // and its clock has to be read from where it actually starts.
      return _sourceOf(cached.stream, cached.requestedAt);
    }
    final stream =
        await runtime.api.playUrl(itemId, startAt: position, modes: pluginPlaybackModes(videoEngine));
    Log.debug('player', 'item ${entry.id} resolved by "${requires.first.id}" '
        '(start=${position.inSeconds}s, mode=${stream.mode}${stream.audioUri == null ? '' : ', pair'})');
    StreamCache.instance.store(itemId, position, stream);
    return _sourceOf(stream, position);
  }

  /// A resolved plugin stream as the player consumes it. `startOffset` is the
  /// position the stream itself begins at: only a mode the plugin produced
  /// (`remux`) starts where it was asked to, so only then does the player skip
  /// its own seek and add the offset back when reading a position. A `single`
  /// file or a `pair` is the whole item from zero, seeked by the player.
  ({Uri uri, Uri? audioUri, Duration startOffset}) _sourceOf(PluginStream stream, Duration position) => (
        uri: stream.uri,
        audioUri: stream.audioUri,
        startOffset: stream.startsAtRequestedPosition && position > Duration.zero ? position : Duration.zero,
      );

  Future<VideoPlayback> _prepareController(VideoPlayback controller, ChannelEntry entry, Duration? seekTo,
      {bool alreadyPositioned = false}) async {
    await controller.open();
    // `seekTo` is a position within the entry (the "TV turned on" in the
    // middle of a program); the entry itself starts at its item offset, so the
    // later segment of a split item (same file, resumed after the break) is
    // positioned at its resume point as soon as it is prepared, long before it
    // is shown. A whole item has offset zero and is only seeked for a tune-in.
    final position = entry.itemOffset + (seekTo ?? Duration.zero);
    if (!alreadyPositioned && position > Duration.zero) {
      await controller.seekTo(position);
    }
    // Resolve the stream (audio/subtitle) selection cascade (tiers 2-4 —
    // see services/stream_selection_resolver.dart and AGENTS.md "Client app
    // structure") against this item's real tracks. Track candidates are
    // built from the SAME list position setAudioTracks/setSubtitleTracks
    // expect (the mdk API takes a 0-based index within that media type, not
    // StreamInfo.index, which is the demuxer's absolute stream number) —
    // see debug_overlay.dart for another read of this same track list
    // data. `selection.audioTracks == null` (no cascade step matched
    // anything, across every tier) leaves the player's own default alone;
    // `selection.subtitleTracks` is never null — an empty list is what
    // disables fvp/libmdk's default-enabled first MKV subtitle track when
    // no step matched, same as the unconditional setSubtitleTracks([]) this
    // replaced.
    final tracks = controller.tracks;
    final audioStreams = tracks.audio;
    final subtitleStreams = tracks.subtitle;
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
            language: audioStreams[i].language,
            title: audioStreams[i].title,
          ),
      ],
      subtitleTracks: [
        for (var i = 0; i < subtitleStreams.length; i++)
          TrackCandidate(
            index: i,
            language: subtitleStreams[i].language,
            title: subtitleStreams[i].title,
          ),
      ],
      tierOneOverride: tierOneOverride,
      userRules: _streamPrefs.userRules,
      channelRules: _currentChannelRules,
      globalRules: _streamPrefs.globalRules,
    );
    if (selection.audioTracks != null) {
      controller.selectAudio(selection.audioTracks!);
    }
    controller.selectSubtitles(selection.subtitleTracks);
    // Engine-specific workarounds (a/v sync) live in the engine. The read-ahead
    // is "min+max": playback only starts/resumes once `min` is buffered and the
    // demuxer keeps reading up to `max`, to ride out bursty latency from a
    // network-backed library.
    controller.tune(
      const PlaybackTuning(
        bufferMin: Duration(milliseconds: playbackBufferMinMs),
        bufferMax: Duration(milliseconds: playbackBufferMaxMs),
      ),
    );
    return controller;
  }

  Future<void> _startChannel({Duration initialSeek = Duration.zero, required int generation}) async {
    final entry = channel[0];
    // The offset is only known once the source is resolved (a remux starts where
    // it was asked to; a pair or a single file starts at its own zero), so it is
    // read back from the open, not assumed from `_needsPlugin`.
    _playbackOffset = Duration.zero;
    final ({VideoPlayback playback, Duration startOffset}) opened;
    try {
      opened = await _createController(entry, seekTo: initialSeek);
    } on UnsupportedMediaException catch (e) {
      // The program on air right now cannot be played here. Nothing is left
      // half-open and nothing plays: the card says why, and the channel returns
      // by itself when the next program starts.
      await _enterUnplayableWait(entry, e.reason, generation);
      return;
    }
    final controller = opened.playback;
    _playbackOffset = opened.startOffset;
    if (generation != _loadGeneration || !mounted) {
      Log.debug('player', 'channel start abandoned after the controller opened (generation $generation, mounted=$mounted)');
      // Switched channel again, or left the player, while the controller was
      // initializing/seeking. See serializeGlOp's doc comment for why this
      // fire-and-forget dispose still has to go through the shared queue.
      unawaited(disposeController(controller));
      return;
    }
    // A channel change has no lead time to measure anything, so it enters at the
    // level this channel was last at (or this item's own, if it was leveled
    // before): a known number keeps the jump small, and the measurement that
    // follows corrects it slowly, once there is real audio on the wire.
    final knownLevel = _knownLevel(entry);
    final boost = await _applyBoost([?knownLevel]);
    _itemLevel = knownLevel;
    _itemGain = knownLevel != null ? _volumeFor(knownLevel, boostDb: boost) : _enteringVolume(boostDb: boost);
    try {
      await controller.setVolume(_itemGain);
      await controller.play();
    } catch (_) {
      unawaited(disposeController(controller));
      rethrow;
    }
    if (generation != _loadGeneration || !mounted) {
      // The channel was left while play() was in flight: it may already be
      // audible, so it is silenced before its dispose waits its turn.
      unawaited(discardController(controller));
      return;
    }
    _clearUnplayableNotice();
    _screenAwake.playing = true;
    BootTrace.mark('channel_on_air');
    BootTrace.flush();
    Log.info('player', 'channel on air ${_loadClock.elapsedMilliseconds}ms after the load started (units=${channel.length}, seek=${initialSeek.inSeconds}s)');
    setState(() {
      _controllerA = controller;
      _currentIndex = 0;
      _headController = controller;
      _headIndex = 0;
      _onAir = true;
      _status = 'playing: ${entry.label}';
    });
    _syncWatermark();
    // A channel change is announced by the corner banner, not by the full-width
    // OSD: the OSD is what Select opens (and it works while the channel loads).
    _announceChannel();
    // If we joined in the middle of the program (initialSeek > 0), only
    // clipDuration - initialSeek of real playback remains — scheduling by the
    // full clipDuration would leave the video parked on the last frame for
    // initialSeek before the crossfade fires.
    _armStallWatch(generation);
    _scheduleTransitions(0, remaining: entry.clipDuration - initialSeek, generation: generation);
    // Joined mid-programme: the level is measured where the viewer landed, not
    // at the head of the file.
    unawaited(_levelItem(entry, controller, generation, startAt: initialSeek, from: _itemGain));
    unawaited(_maybeExtendGrid(0, generation));
    if (_reconcilePending) {
      _reconcilePending = false;
      unawaited(_reconcileWithCache());
    }
  }

  // Samples the playing position while this generation of the channel is on
  // air and reopens the channel when it stops advancing. A source that ends
  // under a player (a remux that died, a live edge that closed) leaves the
  // engine waiting for frames that never arrive: no error, no state change, one
  // frozen picture forever. Reopening resolves the item again at the position
  // the guide has reached, which is what makes a long channel survive a source
  // that dies mid-item.
  void _armStallWatch(int generation) {
    _stallTimer?.cancel();
    _stall.reset();
    _stallTimer = Timer.periodic(playbackStallCheck, (Timer _) {
      if (!mounted || generation != _loadGeneration) return;
      // A gap hold plays the local slate on purpose; the next item is parked
      // outside the two slots, so the position it reports is not the channel's.
      if (_gapHoldTimer != null || _gapHoldPending != null) return;
      final head = _headController;
      final active = _aIsActive ? _controllerA : _controllerB;
      final healthy = head != null &&
          _currentIndex >= 0 &&
          _currentIndex < channel.length &&
          identical(head, active) &&
          head.state.isInitialized;
      // The reopen budget is cleared by *playback*, not by a controller that
      // merely opened: an item whose engine dies three seconds after it starts
      // reported healthy here, zeroed the budget and looped forever (measured:
      // seven reopenings in eight minutes, each one a full player). Only a run
      // of advancing position counts.
      if (healthy && head.state.isPlaying && !head.state.isBuffering) {
        _healthySince ??= DateTime.now();
        if (DateTime.now().difference(_healthySince!) >= playbackHealthyBeforeReset) {
          _pictureLostReopens = 0;
          _stallResyncs = 0;
        }
      } else {
        _healthySince = null;
      }
      // A channel that says it is on air with nothing to show is a transition
      // that never completed: the incoming controller was built and took a
      // video surface, the outgoing one was disposed, and nothing ever played.
      // There is no position to watch, so StallWatcher below cannot see it —
      // this is the case for the picture watchdog.
      if (_pictureLost.observe(onAir: _onAir, healthy: healthy, now: DateTime.now())) {
        _pictureLost.reset();
        if (_pictureLostReopens >= playbackPictureLostReopensBeforeGivingUp) {
          Log.error('player', 'no picture for ${playbackPictureLostBound.inSeconds}s again after '
              '$_pictureLostReopens reopen(s); giving up');
          setState(() => _status = 'ERROR: the channel stopped playing and reopening it '
              'did not help. Press Back and pick the channel again.');
          _stallTimer?.cancel();
          _stallTimer = null;
          return;
        }
        _pictureLostReopens++;
        Log.warn('player', 'no picture for ${playbackPictureLostBound.inSeconds}s while on air '
            '(controller ${head == null ? 'none' : 'not in the active slot'}, '
            'unit $_currentIndex of ${channel.length}); reopening the channel '
            '(attempt $_pictureLostReopens of $playbackPictureLostReopensBeforeGivingUp)');
        unawaited(_switchToChannelIndex(_channelIndex));
        return;
      }
      if (!healthy) {
        _stall.reset();
        return;
      }
      final state = head.state;
      final verdict = _stall.observe(
        position: state.position,
        playing: state.isPlaying && !state.isBuffering,
        now: DateTime.now(),
      );
      if (verdict == StallState.progressing) {
        _stallResyncs = 0;
        _stallBufferedReported = false;
        return;
      }
      if (verdict != StallState.stalled) return;
      _stall.reset();
      if (_stallResyncs >= playbackStallResyncsBeforeGivingUp) {
        Log.error('player', 'picture stopped again at ${state.position.inSeconds}s after $_stallResyncs reopen(s); giving up');
        setState(() => _status = 'ERROR: the picture stopped and reopening it did not help. '
            'Press Back and pick the channel again.');
        _stallTimer?.cancel();
        _stallTimer = null;
        return;
      }
      // A position that stops with media already ahead of it is not a source
      // that ended, and both remedies tried here made it worse. Seeking restarts
      // the stream where it is: a device reports `buffering` for as long as a
      // growing playlist is followed, the position froze with fifty seconds
      // buffered, and every nudge replayed the same twenty seconds. Reopening
      // resolves the same item again. Both were chasing a clock, not a picture —
      // so the picture is left alone and the disagreement is logged once.
      final ahead = state.bufferedUntil == null ? Duration.zero : state.bufferedUntil! - state.position;
      if (ahead > playbackStallBufferedAhead) {
        if (!_stallBufferedReported) {
          _stallBufferedReported = true;
          Log.warn('player', 'the reported position stopped at ${state.position.inSeconds}s with '
              '${ahead.inSeconds}s already buffered; leaving the picture alone');
        }
        return;
      }

      _stallResyncs++;
      // The buffer at the moment it stopped tells apart the two ways a picture
      // stops: a source that ended (nothing buffered ahead) from a decoder that
      // will not advance (plenty buffered, still frozen).
      Log.warn('player', 'picture stopped at ${state.position.inSeconds}s '
          '(buffered ahead ${state.bufferedUntil == null ? 'unknown' : '${(state.bufferedUntil! - state.position).inSeconds}s'}, '
          'playing ${state.isPlaying}, buffering ${state.isBuffering}); reopening the channel '
          '(attempt $_stallResyncs of $playbackStallResyncsBeforeGivingUp)');
      unawaited(_switchToChannelIndex(_channelIndex));
    });
  }

  // A program this device cannot play: no picture, an honest card, and the
  // channel back on air by itself when the next program starts.
  //
  // Everything that could hold a decoder is torn down first (the slot
  // controllers and a parked preload), because the reason a decoder refused is
  // often that the box had no instance left - leaving them alive would keep the
  // next program from starting too. Nothing is retried: this program is over for
  // this device, whatever the schedule says.
  Future<void> _enterUnplayableWait(ChannelEntry entry, String reason, int generation) async {
    if (!mounted || generation != _loadGeneration) return;
    if (_unplayableNotice.value?.programLabel == entry.programLabel) return; // already waiting
    Log.warn('player', 'cannot play unit ${entry.id} on this device ($reason); '
        'waiting for the next program after ${entry.label}');
    _unplayable.add(entry.id);
    _advanceTimer?.cancel();
    _advanceTimer = null;
    _resetPreload();
    _stallTimer?.cancel();
    _stallTimer = null;
    _stall.reset();
    _gapRetryTimer?.cancel();
    _gapRetryTimer = null;
    _gapHoldTimer?.cancel();
    _gapHoldTimer = null;
    _volumeRampTimer?.cancel();
    _gainRampTimer?.cancel();
    final pending = _gapHoldPending;
    _gapHoldPending = null;
    if (pending != null) unawaited(disposeController(pending));
    final staleA = _controllerA;
    final staleB = _controllerB;
    if (staleA != null) unawaited(disposeController(staleA));
    if (staleB != null) unawaited(disposeController(staleB));
    final index = channel.indexOf(entry);
    final resumeAt = entry.startAt.add(entry.clipDuration);
    if (!mounted || generation != _loadGeneration) return;
    setState(() {
      _controllerA = null;
      _controllerB = null;
      _headController = null;
      _aIsActive = true;
      _opacityA = 0;
      _opacityB = 0;
      _onAir = false;
      if (index >= 0) _currentIndex = index;
      _pictureLost.reset();
      _pictureLostReopens = 0;
      _status = 'UNPLAYABLE: ${entry.programLabel} cannot be played on this device; '
          'waiting for the next program';
    });
    _watermarkNotifier.value = null;
    if (_channels.isEmpty) return;
    final channelInfo = _channels[_channelIndex];
    _unplayableNotice.value = UnplayableNotice(
      channelNumber: channelInfo.number,
      channelName: channelInfo.name,
      programLabel: entry.programLabel,
      reason: reason,
      resumeAt: resumeAt,
    );
    _screenAwake.playing = false;
    // Back on air at the end of the program being waited out. A reload rather
    // than a crossfade: nothing is playing to fade from, and the guide by then
    // points at the next program anyway.
    var wait = resumeAt.difference(DateTime.now().toUtc());
    if (wait < minTransitionInterval) wait = minTransitionInterval;
    _unplayableTimer?.cancel();
    _unplayableTimer = Timer(wait, () {
      if (!mounted || generation != _loadGeneration) return;
      Log.info('player', 'the program after ${entry.label} is due; resuming channel');
      unawaited(_switchToChannelIndex(_channelIndex));
    });
  }

  /// Drops the waiting state: there is a picture again (or the channel moved).
  void _clearUnplayableNotice() {
    _unplayableTimer?.cancel();
    _unplayableTimer = null;
    if (_unplayableNotice.value != null) _unplayableNotice.value = null;
  }

  // Schedules the preload of the next item BEFORE the end of the current clip
  // (the heart of the design: the next one can be ready/buffered without
  // interrupting what is already playing) and the crossfade trigger at the end
  // of the clip. `remaining` overrides the duration used for scheduling when
  // the item started playing from a non-zero point (see _startChannel); all
  // other items always play from zero, so they use clipDuration.
  // `generation` travels with the Timer so _preloadNext/_crossfadeTo can
  // detect whether the channel changed again before firing.
  // The picture watchdog (see PictureLostWatcher in playback_stall.dart): set
  // while a channel is supposed to be on air, and how many reopenings the
  // current absence of picture already caused.
  bool _onAir = false;
  final PictureLostWatcher _pictureLost = PictureLostWatcher();
  int _pictureLostReopens = 0;
  /// Since when the picture has been really playing, cleared by anything that
  /// interrupts it (see the reopen budget in [_armStallWatch]).
  DateTime? _healthySince;

  /// Non-null while the channel is waiting out a program this device cannot
  /// play. Drawn by [PlayerUnplayableNotice] (a layer, never a rebuild of this
  /// page), cleared as soon as there is a picture again.
  final ValueNotifier<UnplayableNotice?> _unplayableNotice = ValueNotifier<UnplayableNotice?>(null);
  Timer? _unplayableTimer;

  void _scheduleTransitions(int index, {Duration? remaining, required int generation}) {
    final entry = channel[index];
    var effective = remaining ?? entry.clipDuration;
    if (effective < minTransitionInterval) effective = minTransitionInterval;
    // The lead is the configured one, grown by what recent preloads measured
    // (preload_lead.dart). An item shorter than it preloads right after it
    // starts.
    final lead = _preloadTracker.effective(AppConfig.configuredPreloadLead(_settings));
    _preloadTimer?.cancel();
    final preloadIn = preloadDelay(remaining: effective, lead: lead);
    Log.debug('player', 'item $index scheduled: preload in ${preloadIn.inSeconds}s (lead ${lead.inSeconds}s), advance in ${effective.inSeconds}s');
    _preloadTimer = Timer(preloadIn, () => _preloadNext(index + 1, generation));

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
      VideoPlayback? controller;
      Log.debug('preload', 'attempt ${failures + 1} for item $nextIndex started');
      try {
        controller = (await _createController(nextEntry)).playback;
        await controller.setVolume(0);
      } catch (e) {
        clock.stop();
        _preloadTracker.record(clock.elapsed);
        final broken = controller;
        if (broken != null) unawaited(disposeController(broken));
        if (e is UnsupportedMediaException) {
          // Retrying cannot change the answer, and each attempt builds another
          // player on a box that just told us it has no decoder to spare. Stop
          // here: the transition brings the channel into the waiting state.
          Log.warn('preload', 'item $nextIndex cannot be played on this device (${e.reason}); not retrying');
          return;
        }
        failures++;
        Log.warn('preload', 'attempt $failures for item $nextIndex failed after ${clock.elapsedMilliseconds}ms', error: e);
        if (!mounted || generation != _loadGeneration) return;
        if (failures >= preloadMaxAttempts) {
          // Out of attempts for this item: the channel keeps playing what is on
          // air and simply skips this one at its turn (the transition's own
          // "cannot start" path). Bounded on purpose - an unbounded retry loop
          // built 49 players in fifteen minutes and ran a TV box out of heap.
          Log.error('preload', 'item $nextIndex failed $failures times; skipping it at its turn', error: e);
          return;
        }
        if (failures >= preloadAttemptsBeforeError) {
          if (failures == preloadAttemptsBeforeError) Log.error('preload', 'item $nextIndex failed $failures times; still retrying', error: e);
          setState(() => _status = 'ERROR: preload of ${nextEntry.label} failed, retrying… ($e)');
        }
        await Future<void>.delayed(preloadRetryDelayFor(
          failures: failures,
          base: AppConfig.preloadRetryBase,
          costlySource: _needsPlugin(nextEntry),
        ));
        continue;
      }
      clock.stop();
      _preloadTracker.record(clock.elapsed);
      Log.debug('preload', 'item $nextIndex ready in ${clock.elapsedMilliseconds}ms after $failures failure(s)');
      // Also dropped when the bookkeeping was reset while this attempt was in
      // flight (the player restarted the channel from another item): a late
      // controller must never reach a slot of a channel that moved on.
      if (!mounted || generation != _loadGeneration || _preloadGeneration != generation || _preloadTargetIndex != nextIndex) {
        unawaited(disposeController(controller));
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
      if (replaced != null) unawaited(disposeController(replaced));
      _preloadReadyIndex = nextIndex;
      // Leveled before it airs: the fade into it can then carry the right gain
      // instead of a correction heard seconds into the programme.
      unawaited(_measureIntoCache(nextEntry));
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
        if (!mounted || generation != _loadGeneration) return false;
        if (_preloadReadyIndex == nextIndex) return true;
        // The run finished without a controller: the retry budget ran out, so
        // this item is not coming and waiting on the same future again would
        // spin. (It used to retry forever, which is why this could only ever
        // mean "still running".)
        if (identical(_preloadFuture, inFlight)) return false;
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
    Log.debug('crossfade', 'transition to item $nextIndex due (known units=${channel.length})');
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
      Log.warn('crossfade', 'no unit known for item $nextIndex; checking the backend');
      if (await _backendUnreachable()) {
        if (generation != _loadGeneration) return;
        Log.warn('crossfade', 'backend unreachable, cache exhausted (behavior=${_settings.exhaustedBehavior.name})');
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
      Log.warn('crossfade', 'nothing more scheduled; retrying every ${epgGapRetryInterval.inSeconds}s');
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
    // A unit this device already refused to play: do not build another player
    // for it. The channel goes to the waiting state instead, and comes back at
    // the program after it.
    if (_unplayable.contains(nextEntry.id)) {
      await _enterUnplayableWait(nextEntry, _unplayableReason[nextEntry.id] ?? 'the video cannot be decoded here', generation);
      return;
    }
    // Not ready yet (a slow source, a failed first attempt): keep what is on
    // air and wait for it; the preload retries a bounded number of times.
    if (_preloadGeneration != generation || _preloadReadyIndex != nextIndex) {
      final waiting = Stopwatch()..start();
      Log.warn('crossfade', 'preload for item $nextIndex not ready at the transition; waiting');
      if (!await _awaitPreload(nextIndex, generation)) {
        // Out of attempts: this unit is not coming. Freezing on the finished
        // item is the worst answer, so it is skipped and the next one starts
        // (a hard cut, since there is nothing prepared to fade from).
        Log.warn('crossfade', 'item $nextIndex could not be prepared; skipping it');
        if (nextIndex + 1 < channel.length) {
          try {
            await _resumeAt(nextIndex + 1, generation);
          } catch (e) {
            Log.error('crossfade', 'the item after $nextIndex also failed to start', error: e);
            if (mounted && generation == _loadGeneration) {
              setState(() => _status = 'ERROR: the channel could not start the next program. '
                  'Press Back and pick the channel again.');
            }
          }
        } else if (mounted && generation == _loadGeneration) {
          setState(() => _status = 'ERROR: nothing more scheduled right now — retrying…');
        }
        return;
      }
      Log.warn('crossfade', 'preload for item $nextIndex became ready after ${waiting.elapsedMilliseconds}ms');
    }
    var incoming = _aIsActive ? _controllerB : _controllerA;
    if (incoming == null) return;
    // Whatever is on air now (the wait above can be long): this is what fades out.
    final outgoing = _aIsActive ? _controllerA : _controllerB;
    _preloadReadyIndex = null;
    _preloadFuture = null;
    _preloadTargetIndex = null;

    // Honor the EPG clock (issue #9) instead of always playing back to back:
    // compare the next item's real, materialized startAt with wall-clock now.
    final gap = nextEntry.startAt.difference(DateTime.now().toUtc());
    var seeked = Duration.zero;
    if (gap > epgSyncTolerance) {
      // Interior schedule gap — hold on the slate instead of starting
      // `incoming` early and running ahead of the EPG clock for the rest of
      // the session.
      Log.info('crossfade', 'schedule gap of ${gap.inSeconds}s before item $nextIndex; holding the slate');
      await _holdForGap(nextIndex: nextIndex, outgoing: outgoing, pending: incoming, gap: gap, generation: generation);
      return;
    }
    if (gap < -epgSyncTolerance) {
      // Running behind the EPG clock (e.g. a slow preload) — resync by
      // seeking into the item instead of starting it from zero, which would
      // only let the drift accumulate further on every following item.
      var seek = -gap;
      if (seek > nextEntry.clipDuration) seek = nextEntry.clipDuration;
      Log.info('crossfade', 'running ${(-gap).inMilliseconds}ms behind the schedule; seeking ${seek.inMilliseconds}ms into item $nextIndex');
      await incoming.seekTo(nextEntry.itemOffset + seek);
      if (generation != _loadGeneration) return;
      seeked = seek;
    }

    // The controller waited off screen for the preload lead, and an idle
    // connection can be closed under it meanwhile: the engine then fails only
    // when it is asked to play. Promote it before the fade, so a dead one is
    // reopened instead of being put on air, and the item on screen keeps the
    // screen while that happens.
    final promoted = await _promoteIncoming(nextIndex, incoming, nextEntry, generation, seek: seeked);
    if (generation != _loadGeneration) return;
    // Null: the channel was resumed on this item another way, done here.
    if (promoted == null) return;
    incoming = promoted;

    setState(() => _status = 'CROSSFADE -> ${nextEntry.label}');
    Log.debug('crossfade', 'ramp to item $nextIndex starting');
    // The item that was measured during its preload arrives already leveled. The
    // boost is one number for the mix, so it is decided for both items at once and
    // each side's volume (the outgoing one included) is recomputed under it: the
    // lift and the volume move by the same amount, so nothing heard in the middle
    // of the fade jumps.
    final incomingLevel = _knownLevel(nextEntry);
    final outgoingLevel = _itemLevel;
    final boost = await _applyBoost([?outgoingLevel, ?incomingLevel]);
    if (generation != _loadGeneration) return;
    final outgoingGain = outgoingLevel != null ? _volumeFor(outgoingLevel, boostDb: boost) : _itemGain;
    final incomingGain = incomingLevel != null ? _volumeFor(incomingLevel, boostDb: boost) : _enteringVolume(boostDb: boost);
    _itemLevel = incomingLevel;
    _itemGain = incomingGain;
    final onAir = await _rampCrossfade(
      outgoing: outgoing,
      incoming: incoming,
      generation: generation,
      incomingGain: incomingGain,
      outgoingGain: outgoingGain,
    );
    if (generation != _loadGeneration) return;
    Log.info('crossfade', 'item $nextIndex on air (${onAir.inMilliseconds}ms on air at the end of the ramp, seeked ${seeked.inMilliseconds}ms)');
    if (!mounted) return;
    final previousKey = _osdSubjectAt(_currentIndex)?.key;
    setState(() {
      _currentIndex = nextIndex;
      _headController = incoming;
      _headIndex = nextIndex;
      _playbackOffset = Duration.zero;
      _status = 'playing: ${nextEntry.label}';
    });
    _syncWatermark();
    // The OSD announces programs: a filler of the same program, or one more
    // 10-second clip inside a tail, is not an announcement.
    if (_osdSubjectAt(nextIndex)?.key != previousKey) _showOsd();
    // The item has been playing since the crossfade began: what is left of it is
    // its entry minus where it joined and minus the ramp, so the next transition
    // falls on the entry's end instead of drifting by the ramp every time.
    _scheduleTransitions(nextIndex, remaining: nextEntry.clipDuration - seeked - onAir, generation: generation);
    unawaited(_maybeExtendGrid(nextIndex, generation));
  }

  // Starts the incoming item and answers with a controller that is playing,
  // opening the unit again when the engine fails on it first. Null means the
  // channel was resumed on this item some other way (a hard cut), so the caller
  // stops where it is.
  //
  // Reopening, never skipping: a skipped unit is invisible to the viewer, and
  // the guide would disagree with what is on screen. The item that is on air is
  // untouched the whole time, because the fade has not started yet.
  Future<VideoPlayback?> _promoteIncoming(
    int nextIndex,
    VideoPlayback first,
    ChannelEntry entry,
    int generation, {
    Duration seek = Duration.zero,
  }) async {
    var controller = first;
    for (var attempt = 0;; attempt++) {
      if (await _awaitPlaying(controller, generation)) return controller;
      if (!mounted || generation != _loadGeneration) return null;
      _dropIncoming(controller);
      unawaited(disposeController(controller));
      if (attempt >= playbackPromotionAttempts) {
        Log.error('crossfade', 'item $nextIndex failed to start after ${attempt + 1} attempts; resuming it with a hard cut');
        await _resumeAt(nextIndex, generation);
        return null;
      }
      Log.warn('crossfade', 'item $nextIndex failed to start (attempt ${attempt + 1} of ${playbackPromotionAttempts + 1}); reopening it before it airs');
      // A reopened unit starts from its own beginning, so the resync seek the
      // dead one had already taken has to be applied again.
      final reopened = await _openIncoming(entry, generation, seek: seek);
      if (reopened == null) {
        // Opening it again did not even produce a controller: hand the item to
        // the hard cut, which opens it from scratch and reports what it can.
        await _resumeAt(nextIndex, generation);
        return null;
      }
      controller = reopened;
      _parkIncoming(controller);
    }
  }

  // Tells the incoming item to play and holds the fade back until the engine
  // has had its chance to fail on it. False when it did fail.
  Future<bool> _awaitPlaying(VideoPlayback incoming, int generation) async {
    final startedAt = incoming.state.position;
    final waited = Stopwatch()..start();
    try {
      await incoming.play();
    } catch (e) {
      Log.warn('crossfade', 'the incoming item refused to play', error: e);
      return false;
    }
    while (mounted && generation == _loadGeneration) {
      final verdict = judgePromotion(
        state: incoming.state,
        startedAt: startedAt,
        waited: waited.elapsed,
      );
      switch (verdict) {
        case PromotionVerdict.reopen:
          return false;
        case PromotionVerdict.promote:
          return true;
        case PromotionVerdict.wait:
          await Future<void>.delayed(playbackPromotionPoll);
      }
    }
    return false;
  }

  // A fresh controller for `entry`, silent, ready to be promoted.
  Future<VideoPlayback?> _openIncoming(
    ChannelEntry entry,
    int generation, {
    Duration seek = Duration.zero,
  }) async {
    VideoPlayback? created;
    try {
      created = (await _createController(entry)).playback;
      await created.setVolume(0);
      if (seek > Duration.zero) await created.seekTo(entry.itemOffset + seek);
    } catch (e) {
      if (created != null) unawaited(disposeController(created));
      Log.warn('crossfade', 'reopening ${entry.label} failed', error: e);
      return null;
    }
    if (!mounted || generation != _loadGeneration) {
      unawaited(disposeController(created));
      return null;
    }
    return created;
  }

  // Takes a dead incoming controller out of its slot, so nothing fades into it.
  void _dropIncoming(VideoPlayback controller) {
    if (!mounted) return;
    setState(() {
      if (identical(_controllerB, controller)) {
        _controllerB = null;
      } else if (identical(_controllerA, controller)) {
        _controllerA = null;
      }
    });
  }

  // Puts a reopened controller in the slot the preload would have used.
  void _parkIncoming(VideoPlayback controller) {
    if (!mounted) return;
    setState(() {
      if (_aIsActive) {
        _controllerB = controller;
      } else {
        _controllerA = controller;
      }
    });
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
  Future<Duration> _rampCrossfade({
    required VideoPlayback? outgoing,
    required VideoPlayback incoming,
    required int generation,
    double incomingGain = 1.0,
    double? outgoingGain,
  }) async {
    if (generation != _loadGeneration) return Duration.zero;
    try {
      await incoming.play();
    } catch (e) {
      // The page was left (or the channel switched) while play() was in
      // flight: both controllers are disposed by whoever moved on.
      if (generation != _loadGeneration) return Duration.zero;
      Log.error('crossfade', 'play of the incoming controller failed', error: e);
      rethrow;
    }
    if (generation != _loadGeneration) return Duration.zero;
    // The incoming item is on air from here: the caller times its end from this
    // instant, not from the end of the ramp.
    final onAir = Stopwatch()..start();
    const steps = 30;
    final stepDuration = crossfadeDuration ~/ steps;
    final completer = Completer<void>();
    _volumeRampTimer?.cancel();
    var step = 0;
    _volumeRampTimer = Timer.periodic(stepDuration, (t) {
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
      // Neither call waits for the other (or for the previous step): a platform
      // call that is slow on one controller must not hold back the other's
      // volume, nor the end of the ramp. Calls on one controller stay in order.
      // Each side fades at its own level: the incoming item may be 6 dB quieter
      // than the one leaving, and a ramp that ignored that would jump at the
      // end of the fade.
      final out = outgoing;
      final from = outgoingGain ?? 1.0;
      if (out != null) unawaited(_bestEffort(() => out.setVolume(from * (1 - progress))));
      unawaited(_bestEffort(() => incoming.setVolume(incomingGain * progress)));
      if (step >= steps) {
        t.cancel();
        unawaited(_finishRamp(outgoing, incoming, generation).whenComplete(() {
          if (!completer.isCompleted) completer.complete();
        }));
      }
    });
    await completer.future;
    return onAir.elapsed;
  }

  // Runs a platform call on a controller that may be stalled or already gone;
  // never throws and never waits for more than a couple of seconds.
  Future<void> _bestEffort(Future<void> Function() call) async {
    try {
      await call().timeout(const Duration(seconds: 2));
    } catch (_) {}
  }

  // The channel being tuned, for the per-channel level memory.
  String? get _currentChannelId =>
      (_channelIndex >= 0 && _channelIndex < _channels.length) ? _channels[_channelIndex].id : null;

  // The boost actually in force, in dB. What the effect reports, not what was
  // asked for: a platform that refused it means nothing is lifting the output,
  // and the gain math must not assume it is.
  double get _activeBoostDb {
    final state = _preampState;
    if (state == null || !state.active) return 0;
    return state.targetMb / 100.0;
  }

  // Asks the platform whether it will lift the output at all, and starts with no
  // lift. Called at boot: the boost is not a setting any more, it is what the
  // item on air needs, and _applyBoost moves it from here.
  Future<void> _applyPreamp() async {
    _preampState = await _preamp.set(0);
    final state = _preampState;
    // `set` never throws and already warns about a platform that refused the
    // call; what is worth a warning here is a platform that answered while
    // holding no control over the output mix. On a desktop there is no
    // pre-amp at all, so silence is the honest report.
    if (_loudnessOn && state != null && !state.control) {
      // Leveling degrades to attenuation only; the channels still play.
      Log.warn('loudness', 'the platform refused the pre-amp; leveling is attenuation only');
    }
  }

  // Raises (or lowers) the mix lift to what [levels] need, all of them at once,
  // and answers the boost that is really in force afterwards.
  //
  // One number for the whole mix is the constraint (the platform effect is on the
  // output, not on one player), so overlapping items share it: the quietest of
  // the set decides, and each player's volume keeps its own item on the target.
  // Nothing is called when the number would not change, which is every item of a
  // channel that sits at one level.
  Future<double> _applyBoost(Iterable<LoudnessLevel> levels) async {
    final inForce = _activeBoostDb;
    if (!_loudnessOn) return inForce;
    // Nothing known is not the same as nothing needed. An empty set is a channel
    // change before its first measurement (measured on a device: the boost fell
    // from +11.9 dB to 0 and back to +15 within 2.7 s, which is a 27 dB step in
    // the middle of the switch), so keep what is in force until a level arrives.
    if (levels.isEmpty) return inForce;
    final wanted = loudnessBoostDb(
      levels.map((level) => loudnessNeedDb(lufs: level.lufs, targetLufs: _loudnessTarget)),
      maxBoostDb: _loudnessMaxBoost,
    );
    if ((wanted - inForce).abs() < 0.05) return inForce;
    _preampState = await _preamp.set(wanted);
    final actual = _activeBoostDb;
    Log.info('loudness', 'boost ${inForce.toStringAsFixed(1)} -> ${actual.toStringAsFixed(1)}dB');
    return actual;
  }

  // The volume an item at [level] plays at, under the boost in force (or under
  // [boostDb], for a transition that is about to change it).
  double _volumeFor(LoudnessLevel level, {double? boostDb}) => clampLoudnessGain(loudnessGain(
        lufs: level.lufs,
        peakDb: level.peakDb,
        targetLufs: _loudnessTarget,
        boostDb: boostDb ?? _activeBoostDb,
      ));

  // The level measured for [entry] before, if any.
  //
  // An item measured before is exact. A channel that was watched in an earlier
  // session is the fallback at boot, when nothing is playing yet: it is what the
  // viewer left that channel at. With something already on air the item is not
  // inherited from anything (see _enteringVolume): the incoming item's own level
  // is what its volume has to be computed from, and a guess made from another
  // item's loudness is what used to make channel changes jump.
  LoudnessLevel? _knownLevel(ChannelEntry entry) {
    final service = _loudness;
    if (service == null || !_loudnessOn) return null;
    final own = service.itemLevel(entry.id);
    if (own != null) return own;
    if (_headController != null) return null;
    final channelId = _currentChannelId;
    return channelId == null ? null : service.channelLevel(channelId);
  }

  // The volume an item nobody measured enters at.
  //
  // With something already playing, the level being heard right now is the best
  // guess for what the viewer expects next, and it is what keeps a channel change
  // from jumping. With nothing playing, it is the level of the loudest material
  // the library holds: the first correction after it can only go up.
  double _enteringVolume({required double boostDb}) {
    if (_headController != null && _itemGain.isFinite && _itemGain > 0) return _itemGain;
    return loudnessConservativeGain(targetLufs: _loudnessTarget, boostDb: boostDb);
  }

  // Measures [entry] at [startAt]. Null when there was nothing usable to
  // measure (silence, no audio track).
  Future<LoudnessMeasurement?> _measureAudio(ChannelEntry entry, Duration startAt) async {
    final measurement = await _loudnessProbe.measure(
      url: streamUrl(entry.id),
      startAt: startAt,
      window: loudnessMeasureWindow(clipDuration: entry.clipDuration, startAt: startAt),
    );
    Log.debug('loudness', '${entry.label} at ${startAt.inSeconds}s: $measurement');
    if (measurement == null) return null;
    if (!measurement.usable) {
      Log.debug('loudness', '${entry.label}: not enough signal to level (${measurement.blocks} blocks)');
      return null;
    }
    return measurement;
  }

  // Measures [entry] at [startAt] and turns the result into a level.
  Future<LoudnessLevel?> _measureLevel(ChannelEntry entry, Duration startAt) async {
    final measurement = await _measureAudio(entry, startAt);
    if (measurement == null) return null;
    return LoudnessLevel(lufs: measurement.lufs, peakDb: measurement.peakDb);
  }

  // Levels an item that is already on air, ramping from the level it entered at.
  // Never throws: a channel that cannot be measured keeps playing as it is.
  Future<void> _levelItem(
    ChannelEntry entry,
    VideoPlayback controller,
    int generation, {
    required Duration startAt,
    required double from,
  }) async {
    try {
      await _levelItemInner(entry, controller, generation, startAt: startAt, from: from);
    } catch (e, stack) {
      // Leveling is a nice-to-have: a failure here must never reach the player.
      Log.error('loudness', 'leveling ${entry.label} failed', error: e, stack: stack);
    }
  }

  Future<void> _levelItemInner(
    ChannelEntry entry,
    VideoPlayback controller,
    int generation, {
    required Duration startAt,
    required double from,
  }) async {
    final service = _loudness;
    Log.debug('loudness', 'leveling ${entry.label}: service=${service != null} enabled=$_loudnessOn from=$from');
    if (service == null || !_loudnessOn) return;
    // A gain measured near the item's start is the item's level; anything
    // sampled mid-programme is only what that stretch sounds like, so it levels
    // this playback and is not remembered as the item's own level.
    final representative = startAt < const Duration(seconds: 30);
    var level = service.itemLevel(entry.id);
    if (level != null) {
      // Known before it aired (the preload measures every item that follows):
      // nothing to decode, and no chance of a noisy window overriding it.
      Log.debug('loudness', '${entry.label}: $level from the cache');
    } else {
      level = await _measureLevel(entry, startAt);
      if (level == null) {
        Log.debug('loudness', '${entry.label}: nothing usable to measure, keeping $from');
        return;
      }
      if (representative) {
        await service.remember(entry.id, _currentChannelId, level);
      }
    }
    // The channel may have moved on while the measurement was decoding.
    if (generation != _loadGeneration || !mounted || !identical(controller, _headController)) {
      Log.debug(
        'loudness',
        'measurement for ${entry.label} arrived after it left the air '
        '(gen $generation vs $_loadGeneration, mounted=$mounted, same=${identical(controller, _headController)})',
      );
      return;
    }
    // The boost may have to move for this item, and the volume moves the other
    // way with it: that pair is what puts the item on the target (a quiet item
    // gets lift instead of the volume being stuck at 1.0).
    final boost = await _applyBoost([level]);
    if (generation != _loadGeneration || !mounted) return;
    final volume = _volumeFor(level, boostDb: boost);
    _itemLevel = level;
    _itemGain = volume;
    Log.info('loudness', '${entry.label}: $level, boost ${boost.toStringAsFixed(1)}dB, volume ${volumeGainDb(volume).toStringAsFixed(1)}dB');
    await _rampGainTo(controller, from: from, to: volume, generation: generation);
  }

  // Levels an item before it airs (called from the preload): the fade into it
  // then carries the right gain instead of a correction heard seconds later.
  Future<void> _measureIntoCache(ChannelEntry entry) async {
    final service = _loudness;
    if (service == null || !_loudnessOn) return;
    if (service.itemLevel(entry.id) != null) return;
    final head = await _measureAudio(entry, entry.itemOffset);
    if (head == null) return;
    var level = LoudnessLevel(lufs: head.lufs, peakDb: head.peakDb);
    // A second window guards the cache. One stretch is not the item, and a gain
    // remembered from a window that happened to land on quiet dialogue is worse
    // than no gain: it would be trusted forever, without measuring again.
    if (entry.clipDuration > const Duration(minutes: 3)) {
      final mid = await _measureAudio(entry, entry.itemOffset + entry.clipDuration * 0.4);
      if (mid == null) return;
      final apart = (mid.lufs - head.lufs).abs();
      if (apart > loudnessWindowDisagreementDb) {
        Log.warn(
          'loudness',
          '${entry.label}: windows ${head.lufs.toStringAsFixed(1)} and '
          '${mid.lufs.toStringAsFixed(1)} LUFS are ${apart.toStringAsFixed(1)}dB apart, not leveling it',
        );
        return;
      }
      // They agree on the level, so the louder stretch decides: the louder
      // average and the higher peak are the two halves that need less help, and
      // a level remembered once is trusted forever.
      level = level.louderOf(LoudnessLevel(lufs: mid.lufs, peakDb: mid.peakDb));
    }
    await service.remember(entry.id, _currentChannelId, level);
    Log.debug('loudness', '${entry.label} leveled before airing: $level');
  }

  // Walks the player's volume from [from] to [to], interpolating in dB so the
  // move sounds even. A move up is faster ([gainRampUpDuration]): it starts from
  // an estimate, and an estimate should not be heard for long.
  Future<void> _rampGainTo(
    VideoPlayback controller, {
    required double from,
    required double to,
    required int generation,
  }) async {
    if (!from.isFinite || !to.isFinite || from <= 0 || to <= 0) return;
    final fromDb = 20 * math.log(from) / math.ln10;
    final toDb = 20 * math.log(to) / math.ln10;
    final deltaDb = toDb - fromDb;
    if (deltaDb.abs() < gainRampMinDeltaDb) {
      await _bestEffort(() => controller.setVolume(to));
      return;
    }
    Log.info('loudness', 'ramping from ${fromDb.toStringAsFixed(1)}dB to ${toDb.toStringAsFixed(1)}dB');
    const steps = 20;
    final stepDuration = (toDb > fromDb ? gainRampUpDuration : gainRampDuration) ~/ steps;
    final completer = Completer<void>();
    _gainRampTimer?.cancel();
    var i = 0;
    _gainRampTimer = Timer.periodic(stepDuration, (t) {
      if (generation != _loadGeneration || !mounted) {
        t.cancel();
        if (!completer.isCompleted) completer.complete();
        return;
      }
      i++;
      final progress = (i / steps).clamp(0.0, 1.0);
      final gain = math.pow(10.0, (fromDb + deltaDb * progress) / 20.0).toDouble();
      // Kept current so a crossfade starting mid ramp fades from where the
      // volume actually is, not from where the ramp began.
      _itemGain = gain;
      unawaited(_bestEffort(() => controller.setVolume(gain)));
      if (i >= steps) {
        t.cancel();
        _itemGain = to;
        if (!completer.isCompleted) completer.complete();
      }
    });
    await completer.future;
  }

  // The end of a crossfade: the outgoing item is silenced and stopped for good,
  // the incoming one is at full volume, the slots are swapped and the outgoing
  // controller is disposed (through the shared GL queue, not awaited: the next
  // controller's initialize is queued behind it). Whatever a platform call
  // does, the swap happens: the ramp must always complete.
  Future<void> _finishRamp(VideoPlayback? outgoing, VideoPlayback incoming, int generation) async {
    if (outgoing != null) {
      await _bestEffort(() => outgoing.setVolume(0));
      await _bestEffort(() => outgoing.pause());
    }
    await _bestEffort(() => incoming.setVolume(_itemGain));
    // A channel switch meanwhile already took both slots and disposes them.
    if (generation != _loadGeneration) return;
    if (outgoing != null) unawaited(disposeController(outgoing));
    if (_aIsActive) {
      _controllerA = null;
    } else {
      _controllerB = null;
    }
    _aIsActive = !_aIsActive;
  }

  Future<VideoPlayback> _createSlateController() async {
    final controller = trackController(videoEngine.createAsset(slateAssetPath));
    try {
      await controller.open(label: 'initialize-slate');
      await controller.setLooping(true);
    } catch (e) {
      Log.error('slate', 'the gap slate failed to open', error: e);
      unawaited(disposeController(controller));
      rethrow;
    }
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
    required VideoPlayback? outgoing,
    required VideoPlayback pending,
    required Duration gap,
    required int generation,
  }) async {
    final slate = await _createSlateController();
    if (generation != _loadGeneration) {
      unawaited(disposeController(slate));
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
    // The slate carries no watermark.
    _watermarkNotifier.value = null;
    // Our own asset has no measurement, so it is played at the level the channel
    // was already at instead of at full volume: a slate that arrives 12 dB louder
    // than the program it replaced is the same jump the leveling exists to
    // prevent, and the boost in force is still the departed item's.
    final leftGain = _itemGain;
    _itemLevel = null;
    _slateGain = leftGain;
    await _rampCrossfade(
      outgoing: outgoing,
      incoming: slate,
      generation: generation,
      outgoingGain: leftGain,
      incomingGain: leftGain,
    );
    if (generation != _loadGeneration) {
      unawaited(disposeController(pending));
      return;
    }

    _gapHoldTimer?.cancel();
    _gapHoldPending = pending;
    Log.debug('crossfade', 'slate on air; next item $nextIndex due in ${gap.inSeconds}s');
    _gapHoldTimer = Timer(gap, () async {
      _gapHoldPending = null;
      Log.info('crossfade', 'gap hold over; crossfading from the slate into item $nextIndex');
      if (generation != _loadGeneration) {
        unawaited(disposeController(pending));
        return;
      }
      final nextEntry = channel[nextIndex];
      final slateOutgoing = _aIsActive ? _controllerA : _controllerB;
      // The item waited for the whole gap held off screen, so its connection
      // had even longer to go idle: promote it before the slate leaves.
      final ready = await _promoteIncoming(nextIndex, pending, nextEntry, generation);
      if (generation != _loadGeneration) return;
      if (ready == null) return;
      setState(() {
        if (_aIsActive) {
          _controllerB = ready;
        } else {
          _controllerA = ready;
        }
        _status = 'CROSSFADE -> ${nextEntry.label}';
      });
      final pendingLevel = _knownLevel(nextEntry);
      final pendingBoost = await _applyBoost([?pendingLevel]);
      if (generation != _loadGeneration) return;
      final pendingGain = pendingLevel != null ? _volumeFor(pendingLevel, boostDb: pendingBoost) : _enteringVolume(boostDb: pendingBoost);
      _itemLevel = pendingLevel;
      _itemGain = pendingGain;
      final onAir = await _rampCrossfade(
        outgoing: slateOutgoing,
        incoming: ready,
        generation: generation,
        incomingGain: pendingGain,
        // The slate is where the level was left, not at 1.0: fading from the
        // wrong number would step the level at the start of the fade.
        outgoingGain: _slateGain,
      );
      unawaited(_levelItem(nextEntry, ready, generation, startAt: Duration.zero, from: pendingGain));
      if (generation != _loadGeneration) return;
      if (!mounted) return;
      final previousKey = _osdSubjectAt(_currentIndex)?.key;
      setState(() {
        _currentIndex = nextIndex;
        _headController = pending;
        _headIndex = nextIndex;
        _status = 'playing: ${nextEntry.label}';
      });
      _syncWatermark();
      if (_osdSubjectAt(nextIndex)?.key != previousKey) _showOsd();
      _scheduleTransitions(nextIndex, remaining: nextEntry.clipDuration - onAir, generation: generation);
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
    Log.info('player', 'resuming at item $index with a hard cut (no preload to fade from)');
    _resetPreload();
    final VideoPlayback controller;
    try {
      controller = (await _createController(entry)).playback;
    } on UnsupportedMediaException catch (e) {
      // The hard cut is the last resort for an item that failed to fade in. If
      // the reason is the media itself, there is nothing to cut to.
      await _enterUnplayableWait(entry, e.reason, generation);
      return;
    }
    if (generation != _loadGeneration || !mounted) {
      unawaited(disposeController(controller));
      return;
    }
    try {
      await controller.setVolume(1);
      await controller.play();
    } catch (_) {
      unawaited(disposeController(controller));
      rethrow;
    }
    if (generation != _loadGeneration || !mounted) {
      unawaited(discardController(controller));
      return;
    }
    final staleA = _controllerA;
    final staleB = _controllerB;
    if (staleA != null) unawaited(disposeController(staleA));
    if (staleB != null) unawaited(disposeController(staleB));
    setState(() {
      _controllerA = controller;
      _controllerB = null;
      _aIsActive = true;
      _opacityA = 1;
      _opacityB = 0;
      _currentIndex = index;
      _headController = controller;
      _headIndex = index;
      _playbackOffset = Duration.zero;
      _status = 'playing: ${entry.label}';
    });
    _syncWatermark();
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
      } catch (e) {
        Log.warn('player', 'topping up the working set failed; will retry', error: e);
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
      // The availability snapshot is refreshed off this path; until it lands, the
      // list is filtered with the answer that was current a moment ago.
      unawaited(_runtime.refreshAvailability());
      final fresh = playableChannels(_cache, _cache.channels(), _runtime.availability).visible;
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
          if (_suspended) {
            // Hidden: nothing is loaded, the resume will open this channel.
            setState(() {
              _channels = fresh;
              _channelIndex = 0;
            });
            return;
          }
          setState(() => _channels = fresh);
          unawaited(_switchToChannelIndex(0));
          return;
        }
      }
    }
    if (_suspended) return; // nothing on air to reconcile; resume reloads from the cache
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
      if (fresh.isNotEmpty) {
        Log.info('cache', 'reconcile: real programming is back; leaving the fallback');
        unawaited(_switchToChannelIndex(_channelIndex));
      }
      return;
    }

    final onAirNow = fresh.where((e) => !e.startAt.isAfter(now) && e.endAt.isAfter(now)).firstOrNull;
    final playing = channel[_currentIndex].program;
    if (onAirNow != null && (playing == null || !_sameAiring(playing, onAirNow))) {
      Log.info('cache', 'reconcile: what is on air changed; reloading the channel');
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
    Log.info('cache', 'reconcile: refreshed ${following.length} following program(s) in place');
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
    Log.warn('crossfade', 'cache exhausted; holding the slate');
    final slate = await _createSlateController();
    if (generation != _loadGeneration || !mounted) {
      unawaited(disposeController(slate));
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
      unawaited(disposeController(slate));
      return;
    }
    await slate.play();
    if (generation != _loadGeneration || !mounted) {
      unawaited(disposeController(slate));
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
      Log.debug('cache', 'exhausted-schedule retry');
      unawaited(_reconcileWithCache());
    });
  }

  // The schedule instant the picture is at: the on-air unit's start plus how
  // far its controller has played (net of the file offset of a split item).
  // The wall clock can be ahead of it (a transition that started late within
  // `epgSyncTolerance`, a seek or buffer fill after the resync), and the OSD
  // must read 0:00 where the viewer sees a program begin. Null while it cannot
  // be told (no unit on air yet, the gap slate, a controller not initialized),
  // and the OSD falls back to the wall clock.
  DateTime? _picturePlayhead() {
    final head = _headController;
    final active = _aIsActive ? _controllerA : _controllerB;
    if (head == null || !identical(head, active) || _headIndex != _currentIndex) return null;
    if (_currentIndex < 0 || _currentIndex >= channel.length) return null;
    final value = head.state;
    if (!value.isInitialized) return null;
    // The engine counts from the stream's own zero, which for a source that
    // already began at an offset (a plugin stream) is not the file's zero:
    // without `_playbackOffset` the OSD would claim the viewer is at the start of
    // the program while the picture is far into it.
    return picturePlayheadAt(channel[_currentIndex].startAt, channel[_currentIndex].itemOffset,
        channel[_currentIndex].clipDuration, value.position + _playbackOffset);
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
    if (index == (_pendingSwitchIndex ?? _channelIndex)) return;
    unawaited(_switchToChannelIndex(index));
  }

  // Human-readable label for a track option in the picker/toast: prefers the
  // "title" metadata tag (e.g. "Director's Commentary"), falls back to
  // "language" (e.g. "por"), falls back to a positional placeholder — same
  // track data _createController already reads for the tier 2-4 cascade.
  String _trackLabel(TrackInfo track, int index) {
    final title = track.title;
    if (title != null && title.isNotEmpty) return title;
    final language = track.language;
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
  void _applyAudioTrack(VideoPlayback controller, String channelId, int index, String label) {
    controller.selectAudio([index]);
    unawaited(_settings.setAudioTrackOverride(channelId, index));
    _showTrackToast('Audio: $label');
    unawaited(_relevelForTrackChange(controller));
  }

  // A different audio track is a different master: one film in the library
  // carries a 2.0 dub at -6.2 LUFS and a 5.1 track at -26.9, so a gain measured
  // on one of them is 20 dB wrong on the other. The remembered gain has to go,
  // and what is playing now has to be leveled again — at this position, so the
  // result corrects this playback and is not remembered as the item's level.
  Future<void> _relevelForTrackChange(VideoPlayback controller) async {
    final service = _loudness;
    if (service == null || !_loudnessOn) return;
    if (_headIndex < 0 || _headIndex >= channel.length) return;
    if (!identical(controller, _headController)) return;
    final entry = channel[_headIndex];
    await service.forgetItem(entry.id);
    Log.info('loudness', 'audio track changed on ${entry.label}: forgetting its gain');
    await _levelItem(
      entry,
      controller,
      _loadGeneration,
      startAt: entry.itemOffset + controller.state.position,
      from: _itemGain,
    );
  }

  // Same as _applyAudioTrack, for subtitles. `index == -1` is the picker's
  // explicit "None" entry — setSubtitleTracks([]) disables subtitles, and
  // -1 is the sentinel TierOneOverride.subtitleIndex/StreamSelectionResolver
  // already understand as "explicit no subtitle" (see their doc comments).
  void _applySubtitleTrack(VideoPlayback controller, String channelId, int index, String label) {
    controller.selectSubtitles(index == -1 ? const [] : [index]);
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

  // Dimmer (issue #85): the black layer is `DimmerOverlay`, the last child of
  // the page's Stack, driven by `Dimmer.active`/`Dimmer.level` (never
  // setState). Select on the focused OSD button toggles it on release; holding
  // Select for `dimmerHoldDuration` opens the level dialog instead (an
  // accelerator: the level chip beside the button, shown while dimmed, opens
  // the same dialog with a plain Select).
  Timer? _dimHoldTimer;
  bool _dimSelectDown = false;
  bool _dimDialogOpen = false;

  /// Turns the on-screen log on or off. Hiding it stops the drawing, never the
  /// logging: `Log` keeps recording, which is the point of doing this while the
  /// app is in use.
  void _toggleDebugLog() {
    _debugLogVisibleNotifier.value = !_debugLogVisibleNotifier.value;
    unawaited(_settings.setDebugLogVisible(_debugLogVisibleNotifier.value));
    _showOsd();
  }

  void _toggleDimmer() {
    Dimmer.active.value = !Dimmer.active.value;
    unawaited(_settings.setDimActive(Dimmer.active.value));
    _showOsd();
  }

  // Select key event on the focused OSD dimmer button (down starts the hold
  // countdown; releasing before it fires toggles).
  void _onDimmerSelect(KeyEvent event) {
    if (event is KeyDownEvent) {
      _dimSelectDown = true;
      _dimHoldTimer?.cancel();
      _dimHoldTimer = Timer(dimmerHoldDuration, () {
        _dimSelectDown = false;
        if (mounted) unawaited(_openDimmerLevel());
      });
    } else if (event is KeyUpEvent && _dimSelectDown) {
      _dimSelectDown = false;
      _dimHoldTimer?.cancel();
      _toggleDimmer();
    }
  }

  // The global dim level dialog. The dim shows live behind it; Back restores
  // the previous state, Select saves the level and leaves the dimmer on.
  Future<void> _openDimmerLevel() async {
    if (_dimDialogOpen || !mounted) return;
    _dimDialogOpen = true;
    _osdHideTimer?.cancel();
    _osdVisibleNotifier.value = false;
    final wasActive = Dimmer.active.value;
    final wasLevel = Dimmer.level.value;
    Dimmer.active.value = true;
    final chosen = await showDimmerLevelDialog(
      context,
      initial: wasLevel,
      resolve: _keyBindings.resolve,
      onChanged: (v) => Dimmer.level.value = v,
    );
    _dimDialogOpen = false;
    if (chosen == null) {
      Dimmer.level.value = wasLevel;
      Dimmer.active.value = wasActive;
    } else {
      Dimmer.level.value = chosen;
      Dimmer.active.value = true;
      await _settings.setDimLevelPercent(chosen);
      await _settings.setDimActive(true);
    }
    if (mounted) _focusNode.requestFocus();
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

  // Whether a channel is on air (the real OSD exists); false while it loads.
  /// Points the watermark overlay at the entry on air (`_currentIndex`): its
  /// own resolved watermark, or none. Called wherever `_currentIndex` takes a
  /// new value; touches only the notifier, never `setState`.
  void _syncWatermark() {
    final i = _currentIndex;
    _watermarkNotifier.value = (i >= 0 && i < channel.length) ? channel[i].program?.watermark : null;
  }

  bool get _channelLoaded => channel.isNotEmpty && _currentIndex >= 0 && _currentIndex < channel.length;

  // Touch: a tap on the picture shows the OSD or hides it again. It skips the
  // "armed" state on purpose — that exists to disambiguate Select from the
  // arrows on a remote, and a tap has no such ambiguity.
  void _toggleOsdFromTouch() {
    if (_osdVisibleNotifier.value) {
      _osdHideTimer?.cancel();
      _osdVisibleNotifier.value = false;
    } else {
      // The banner is the announcement of the change; the OSD replaces it on the
      // screen, the same way Select does.
      _hideBanner();
      _showOsd();
    }
  }

  // Touch: a swipe up opens the guide. The same two notifier writes the OSD's
  // own path does, minus `_suppressNextEpgSelectUp` (that flag exists for a
  // Select whose release lands after the guide opened; a swipe has no Select).
  // Closing the track picker — picked, cancelled or Back — also ends the OSD that
  // opened it and hands the keys back to the player. Leaving the OSD up meant the
  // next Up/Down moved its focus instead of switching channel, which is not what
  // a viewer expects right after choosing a track (measured on the suite that
  // guards a switch after a live track change: Up did nothing at all).
  void _closeTrackPicker() {
    _trackPickerOpenNotifier.value = false;
    _osdHideTimer?.cancel();
    _osdVisibleNotifier.value = false;
    _osdFocusNotifier.value = false;
    _osdArmed = false;
    _focusNode.requestFocus();
  }

  void _openEpgFromTouch() {
    _osdHideTimer?.cancel();
    _osdVisibleNotifier.value = false;
    _hideBanner();
    _epgOpenNotifier.value = true;
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
    final info = controller.tracks;
    final channelId = _channels[_channelIndex].id;
    if (panel == TrackSelectorPanel.audio) {
      final tracks = info.audio;
      if (tracks.isEmpty) return;
      final current = _settings.getAudioTrackOverride(channelId);
      final currentIndex = (current != null && current >= 0 && current < tracks.length) ? current : -1;
      final nextIndex = (currentIndex + 1) % tracks.length;
      _applyAudioTrack(controller, channelId, nextIndex, _trackLabel(tracks[nextIndex], nextIndex));
    } else {
      final tracks = info.subtitle;
      // -1 (the virtual "None" entry) is always part of the cycle, same set
      // the picker's Subtitle panel offers.
      final options = <int>[-1, for (var i = 0; i < tracks.length; i++) i];
      if (options.length <= 1) return; // no real subtitle track to cycle to
      final current = _settings.getSubtitleTrackOverride(channelId) ?? -1;
      final currentPosition = options.indexOf(current);
      final nextPosition = ((currentPosition < 0 ? -1 : currentPosition) + 1) % options.length;
      final nextIndex = options[nextPosition];
      final label = nextIndex == -1 ? 'None' : _trackLabel(tracks[nextIndex], nextIndex);
      _applySubtitleTrack(controller, channelId, nextIndex, label);
    }
  }

  // True while the app is hidden/paused and the channel is torn down. Nothing
  // may play, preload or poll the schedule then: audio kept running in the
  // background and the decoders, sockets and timers stayed alive.
  bool _suspended = false;

  /// Stops all playback when the app leaves the screen (Android Home, a
  /// minimized window). Reuses the channel-switch teardown, so every timer,
  /// preload, gap hold and both controllers go away through the GL queue, and
  /// any load still in flight aborts on its generation check.
  void _suspendPlayback() {
    if (_suspended || !mounted || _channels.isEmpty) return;
    _suspended = true;
    Log.info('player', 'app hidden: suspending playback of channel ${_channels[_channelIndex].number}');
    unawaited(_switchToChannelIndex(_channelIndex, load: false));
  }

  /// Brings the channel back when the app is visible again, at the point the
  /// guide has reached by then (a TV that was left on, not a paused tape).
  void _resumePlayback() {
    if (!_suspended) return;
    _suspended = false;
    if (!mounted || _channels.isEmpty) return;
    Log.info('player', 'app visible again: resuming channel ${_channels[_channelIndex].number}');
    unawaited(_switchToChannelIndex(_channelIndex));
  }

  /// When the window loses and regains OS focus Flutter drops primary focus
  /// to the root scope, and the key listener below stops receiving keys.
  /// Hand focus back on resume. Deliberately no `setState`: rebuilding this
  /// page risks the GL-texture bug (see AGENTS.md).
  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    Log.debug('app', 'lifecycle ${state.name}');
    _screenAwake.lifecycle(state);
    _systemBars.lifecycle(state);
    if (state == AppLifecycleState.hidden || state == AppLifecycleState.paused) {
      _suspendPlayback();
    } else if (state == AppLifecycleState.resumed || state == AppLifecycleState.inactive) {
      _resumePlayback();
    }
    if (state != AppLifecycleState.resumed || !mounted) return;
    final primary = FocusManager.instance.primaryFocus;
    if (primary != null && primary is! FocusScopeNode) return;
    if (ModalRoute.of(context)?.isCurrent == false) return;
    _focusNode.requestFocus();
  }

  @override
  void dispose() {
    // Leaving the player invalidates every load, preload, crossfade and gap
    // hold still in flight: each async path re-checks the generation after its
    // awaits, so a controller that finishes opening after this point is
    // discarded (silenced, then disposed through the GL queue) instead of
    // being attached to a page that no longer exists.
    _loadGeneration++;
    Log.debug('player', 'player page disposed (generation $_loadGeneration)');
    _unplayableTimer?.cancel();
    _unplayableNotice.dispose();
    _pendingChannelSwitchTarget = null;
    _volumeRampTimer?.cancel();
    WidgetsBinding.instance.removeObserver(this);
    _screenAwake.dispose();
    _systemBars.dispose();
    _advanceTimer?.cancel();
    _volumeRampTimer?.cancel();
    _osdHideTimer?.cancel();
    _dimHoldTimer?.cancel();
    _osdVisibleNotifier.dispose();
    _osdFocusNotifier.dispose();
    _epgOpenNotifier.dispose();
    _debugOverlayVisibleNotifier.dispose();
    _debugLogVisibleNotifier.dispose();
    _trackPickerOpenNotifier.dispose();
    _watermarkNotifier.dispose();
    _gapRetryTimer?.cancel();
    _gapHoldTimer?.cancel();
    _preloadTimer?.cancel();
    _channelInputCommitTimer?.cancel();
    _stallReleaseTimer?.cancel();
    _switchTimer?.cancel();
    _bannerHideTimer?.cancel();
    _bannerHardTimer?.cancel();
    _selectHoldTimer?.cancel();
    _bannerNotifier.dispose();
    unawaited(_cacheSub?.cancel());
    if (_streamPrefsReady) _streamPrefs.dispose();
    // State.dispose() itself can't await, but still routes through the
    // shared queue so this doesn't race a create() the mosaic page (or a
    // still-in-flight generation) might be doing at the same moment.
    final finalA = _controllerA;
    final finalB = _controllerB;
    final finalPending = _gapHoldPending;
    if (finalA != null) unawaited(disposeController(finalA));
    if (finalB != null) unawaited(disposeController(finalB));
    if (finalPending != null) unawaited(disposeController(finalPending));
    _focusNode.dispose();
    super.dispose();
  }

  Widget _videoFor(VideoPlayback? controller, double opacity) {
    final state = controller?.state;
    if (controller == null || state == null || !state.isInitialized) {
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
          width: state.size.width,
          height: state.size.height,
          child: VideoSurface(controller),
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
    final k = resolveUiScale(
      screenSize,
      override: UiScaleOverride.notifier.value,
      // Read once, never subscribed: the host resolved the same value.
      formFactor: ClientFormFactorState.notifier.value,
    );
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
    // A channel that is announced but not playing yet (the delay is running, or
    // its stream is still opening) is described from the guide: that is what
    // makes the real OSD usable over the spinner instead of the "number · name"
    // line it used to show.
    // While a change is announced (the delay is running, or the target's stream
    // is still opening) the OSD describes the *target*, the same thing the
    // banner says; once the picture is up it follows what is really on screen.
    final announced = _pendingChannel ?? (hasContent ? _channels[_channelIndex] : null);
    final osd = (_pendingChannel != null ? _pendingSubject : null) ?? (hasContent ? _osdSubjectAt(_currentIndex) : null);
    final hasSubject = hasContent || osd != null;
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
        // Holding a key repeats nothing, except an arrow in the open EPG: there it
        // keeps moving the focus along the grid (see `_epgHoldRepeat`).
        if (event is KeyRepeatEvent && !(_epgOpenNotifier.value && _isEpgArrow(event.logicalKey))) return;
        final action = _keyBindings.resolve(event.logicalKey);

        if (_epgOpenNotifier.value) {
          if (action == InputAction.back) {
            if (event is KeyDownEvent) _epgOpenNotifier.value = false;
            return;
          }
          if (action == InputAction.select) {
            if (event is KeyRepeatEvent) return;
            if (_suppressNextEpgSelectUp && event is KeyUpEvent) {
              _suppressNextEpgSelectUp = false;
              return;
            }
            _epgOverlayKey.currentState?.handleSelectKeyEvent(event);
            return;
          }
          if (event is KeyRepeatEvent) {
            if (!_epgHoldRepeat()) return;
          } else if (event is KeyDownEvent) {
            _epgLastStep
              ..reset()
              ..start();
          } else {
            return;
          }
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
            if (event is KeyDownEvent) _closeTrackPicker();
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

        // Select on the OSD's focused dimmer button (or its release): see
        // _onDimmerSelect.
        if (action == InputAction.select &&
            (event is KeyUpEvent
                ? _dimSelectDown
                : _osdVisibleNotifier.value && _osdFocusNotifier.value && (_osdKey.currentState?.dimmerFocused ?? false))) {
          _onDimmerSelect(event);
          return;
        }
        // Select with the OSD out of the way: the press opens it, as it always
        // did, and holding it (>[selectHoldToGuide]) opens the guide instead —
        // the 6-button path to the guide from a playing channel. The OSD is shown
        // on the press and not on the release on purpose: a release that never
        // arrives (a lost key event, a remote that only sends the down) must not
        // make Select do nothing.
        if (action == InputAction.select &&
            !_osdFocusNotifier.value &&
            !_osdVisibleNotifier.value &&
            !_epgOpenNotifier.value &&
            !_trackPickerOpenNotifier.value) {
          if (event is KeyDownEvent) {
            _hideBanner();
            _showOsd();
            _osdArmed = true;
            _selectHoldTimer?.cancel();
            _selectHoldTimer = Timer(selectHoldToGuide, () {
              if (!mounted) return;
              _osdHideTimer?.cancel();
              _osdVisibleNotifier.value = false;
              _openEpgFromOsd();
            });
            return;
          }
          if (event is KeyUpEvent) {
            _selectHoldTimer?.cancel();
            _selectHoldTimer = null;
            return;
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
        if (_osdVisibleNotifier.value && _osdArmed && !_osdFocusNotifier.value && isArrow && _channelLoaded) {
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
                final rect = epgOpen && EpgThumbnailSetting.enabled.value
                    ? _videoThumbnailRect(screenSize)
                    : Offset.zero & screenSize;
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
            // A program this device cannot play: a card where the picture would
            // be, never a frozen frame and never a spinner. Above the video and
            // below the OSD, so the guide, the channel banner and the remote all
            // keep working while the channel waits for the next program.
            PlayerUnplayableNotice(source: _unplayableNotice),
            // The channel watermark: above the video, below the OSD, the EPG and
            // the dimmer (children after this one); pointer-transparent and
            // never focusable.
            WatermarkOverlay(source: _watermarkNotifier),
            // The touch surface: above the video, below the OSD, the spinner,
            // the error banner and the guide, so every one of those keeps the
            // taps it already had. An extra pointer path for a television with
            // a mouse; the only way in on a phone.
            Positioned.fill(
              child: ValueListenableBuilder<bool>(
                valueListenable: _epgOpenNotifier,
                builder: (context, epgOpen, _) => PlayerGestureLayer(
                  key: const Key('player-gestures'),
                  enabled: !epgOpen && !_trackPickerOpenNotifier.value,
                  onTapPicture: _toggleOsdFromTouch,
                  onHorizontalFling: (direction) => unawaited(_switchChannel(direction)),
                  onSwipeUp: _openEpgFromTouch,
                ),
              ),
            ),
            if (logPeekEnabled) _LogPeek(visible: _debugLogVisibleNotifier),
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
            if (hasSubject)
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
                  // While the channel has no picture the OSD is the one drawn
                  // over the spinner: it keeps the `loading-osd` key it had, so
                  // "the channel is announced while it loads" stays one contract.
                  child: KeyedSubtree(
                    key: hasContent ? null : const Key('loading-osd'),
                    child: ChannelOsd(
                    key: _osdKey,
                    channelNumber: announced?.number ?? 0,
                    channelName: announced?.name ?? '',
                    channelArtUrl: announced?.guideArtUrl,
                    channelLogoImageId: announced?.logoImageId,
                    itemLabel: osd?.program.displayTitle ??
                        (hasContent ? channel[_currentIndex].programLabel : ''),
                    nextItemLabel: osd?.next?.displayTitle,
                    startAt: osd?.start ?? (hasContent ? channel[_currentIndex].programStartAt : DateTime.now()),
                    endAt: osd?.end ?? (hasContent ? channel[_currentIndex].programEndAt : DateTime.now()),
                    progressByClock: osd != null,
                    resolveSubject: hasContent ? () => _osdSubjectAt(_currentIndex) : () => _pendingSubject,
                    playhead: _picturePlayhead,
                    positionOffset: () => _playbackOffset,
                    // No controller while the channel has no picture: the OSD
                    // still draws (the track buttons are skipped, `MediaTracks.none`).
                    controller: hasContent ? (_aIsActive ? _controllerA : _controllerB) : null,
                    focusMode: _osdFocusNotifier,
                    onOpenSubtitles: () => _openTrackPicker(TrackSelectorPanel.subtitle, singlePanel: true),
                    onOpenAudio: () => _openTrackPicker(TrackSelectorPanel.audio, singlePanel: true),
                    onToggleDimmer: _toggleDimmer,
                    onOpenDimmerLevel: () => unawaited(_openDimmerLevel()),
                    showDebugLogButton: logPeekEnabled,
                    debugLogVisible: _debugLogVisibleNotifier,
                    onToggleDebugLog: _toggleDebugLog,
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
              ),
            // The channel banner: the automatic announcement of a channel change
            // (and of a channel whose stream is still opening), in a corner, in
            // place of the full-width OSD. Pointer-transparent and never
            // focusable; shown/hidden through its own notifier, never setState.
            Positioned.fill(
              child: ValueListenableBuilder<ChannelBannerData?>(
                valueListenable: _bannerNotifier,
                builder: (context, data, _) => data == null
                    ? const SizedBox.shrink()
                    : ChannelBanner(
                        key: const Key('channel-banner'),
                        data: data,
                        corner: _bannerCorner,
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
                      engineName: videoEngine.name,
                      decoderPreference: videoEngine.decoderSummary,
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
                    videoThumbnailRect:
                        EpgThumbnailSetting.enabled.value ? _videoThumbnailRect(MediaQuery.sizeOf(context)) : null,
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
                final tracks = controller?.tracks ?? MediaTracks.none;
                final audioStreams = tracks.audio;
                final subtitleStreams = tracks.subtitle;
                final channelId =
                    (_channelIndex >= 0 && _channelIndex < _channels.length) ? _channels[_channelIndex].id : null;

                final audioOptions = [
                  for (var i = 0; i < audioStreams.length; i++)
                    TrackOption(index: i, label: _trackLabel(audioStreams[i], i)),
                ];
                final subtitleOptions = [
                  const TrackOption(index: -1, label: 'None'),
                  for (var i = 0; i < subtitleStreams.length; i++)
                    TrackOption(index: i, label: _trackLabel(subtitleStreams[i], i)),
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
                      _closeTrackPicker();
                    },
                    onClose: _closeTrackPicker,
                  ),
                );
              },
            ),
            // Dimmer: above everything, including the EPG and the pickers.
            const DimmerOverlay(),
            // The always-on clock sits above the dimmer (a dimmed picture is
            // exactly when you want the time) and steps aside while the OSD is
            // open, since the OSD draws it in the same corner.
            ClockOverlay(
              osdVisible: _osdVisibleNotifier,
              guideOpen: _epgOpenNotifier,
            ),
          ],
        ),
      ),
    );
  }
}

int _lastIndex(List<TrackOption> options) => options.isEmpty ? 0 : options.length - 1;
