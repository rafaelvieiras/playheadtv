// The one shared set of offline-cache objects for the running app: the backend
// client, the on-disk schedule cache, the art cache and the background syncer.
// The player, the mosaic, the guide and Settings all reach the same instance
// (there is no shared-state package in this app, see AGENTS.md), created on
// first use and kept for the life of the process.
import 'dart:async';
import 'dart:io';

import 'package:path_provider/path_provider.dart';

import '../main.dart' show AppConfig, jellyfinImageUrl;
import '../playhead_api.dart';
import '../services/settings_service.dart';
import 'art_cache.dart';
import 'schedule_cache.dart';
import 'schedule_sync.dart';

class OfflineRuntime {
  final PlayheadApi api;
  final ScheduleCache cache;
  final ArtCache art;
  final ScheduleSyncer syncer;
  final SettingsService settings;

  OfflineRuntime._(this.api, this.cache, this.art, this.syncer, this.settings);

  static OfflineRuntime? _current;
  static Future<OfflineRuntime>? _opening;
  static String? _currentKey;

  /// The runtime if it was already opened, else null (Settings may be reached
  /// before any channel was loaded).
  static OfflineRuntime? get current => _current;

  /// The shared runtime, created (and its background sync started) on the
  /// first call. A changed backend address, token or cache location builds a
  /// new one.
  static Future<OfflineRuntime> ensure() async {
    final key = '${AppConfig.playheadBaseUrl}|${AppConfig.playheadDeviceToken}|${AppConfig.cacheDirOverride}|${AppConfig.cacheSyncInterval}';
    if (_current != null && _currentKey == key) return _current!;
    if (_opening != null && _currentKey == key) return _opening!;
    reset();
    _currentKey = key;
    final opening = _open();
    _opening = opening;
    try {
      return _current = await opening;
    } finally {
      if (identical(_opening, opening)) _opening = null;
    }
  }

  static Future<OfflineRuntime> _open() async {
    final settings = await SettingsService.create();
    final override = AppConfig.cacheDirOverride;
    final Directory dataDir;
    final Directory artDir;
    if (override != null) {
      dataDir = Directory('$override/data');
      artDir = Directory('$override/art');
    } else {
      // The schedule must survive; the OS may reclaim the art folder.
      // Two platform round trips, independent of each other: asked together.
      final (support, cache) = await (getApplicationSupportDirectory(), getApplicationCacheDirectory()).wait;
      dataDir = Directory('${support.path}/schedule_cache');
      artDir = Directory('${cache.path}/art');
    }
    // One file per backend address: pointing the app at another backend never
    // shows (or wipes) the schedule of the first.
    final fileKey = ArtCache.keyFor(AppConfig.playheadBaseUrl).substring(0, 12);
    final cache = ScheduleCache.open('${dataDir.path}/schedule_$fileKey.db', clientLimit: settings.cacheWindowLimit);
    final art = ArtCache(dir: artDir);
    final token = AppConfig.playheadDeviceToken;
    final api = PlayheadApi(baseUrl: AppConfig.playheadBaseUrl, deviceToken: token.isEmpty ? null : token);
    final syncer = ScheduleSyncer(
      api: api,
      cache: cache,
      interval: AppConfig.cacheSyncInterval,
      art: art,
      artUrlOf: (itemId, tag) => jellyfinImageUrl(itemId, tag),
    );
    syncer.start();
    return OfflineRuntime._(api, cache, art, syncer, settings);
  }

  /// Stops the syncer and closes the cache file (a test starting a new app
  /// configuration; the app itself keeps the runtime until the process ends).
  static void reset() {
    final runtime = _current;
    _current = null;
    _opening = null;
    _currentKey = null;
    if (runtime != null) {
      runtime.syncer.stop();
      runtime.cache.close();
      runtime.art.close();
    }
  }

  /// Applies a new cache window limit (Settings): the cached window follows it
  /// at once for reads, and a refresh brings in what a larger one now allows.
  Future<void> applyWindowLimit(Duration limit) async {
    cache.clientLimit = limit;
    unawaited(syncer.syncNow());
  }

  /// Empties the schedule and the art cache (the explicit "clear cache" user
  /// action) and refills them from the backend when it is reachable.
  Future<void> clearCache() async {
    cache.clear();
    await art.clear();
    unawaited(syncer.syncNow());
  }
}
