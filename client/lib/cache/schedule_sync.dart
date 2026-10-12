// The only part of the client that asks the backend for schedule data: it
// keeps the local [ScheduleCache] topped up while everything else (the player,
// the guide, the mosaic) reads from it.
//
// A refresh is cheap when nothing changed: `GET /epg` is sent with the
// `If-None-Match` validator of the copy held, and `304` costs a few hundred
// bytes. Offline tolerance is the window minus the time since the last
// successful refresh, so it runs often (every [interval], 15 minutes by
// default), at startup and, after a failure, again every [retryInterval]
// until it succeeds: that is how a returning connection is noticed on every
// platform, with no platform connectivity plugin.
//
// A failure of any kind (backend down, no network, a rejected or expired
// token) leaves the cache exactly as it is and playback keeps running from it.
// A rejected token is retried on every attempt like any other failure, so as
// soon as the backend accepts the configured credentials again the cache is
// refreshed without ever having been discarded.
import 'dart:async';

import 'package:flutter/foundation.dart';

import '../playhead_api.dart';
import 'art_cache.dart';
import 'schedule_cache.dart';
import '../log/log.dart';

enum SyncOutcome {
  /// The backend had a different schedule (or channel list): the cache changed.
  updated,

  /// The backend confirmed the copy held is current.
  unchanged,

  /// The backend could not be reached.
  offline,

  /// The backend answered 401/403: the token is missing, expired or revoked.
  authRejected,

  /// Any other failure (an unexpected status, an unreadable answer).
  failed,
}

class SyncStatus {
  final bool syncing;
  final SyncOutcome? lastOutcome;
  final DateTime? lastAttempt;
  final String? lastError;
  const SyncStatus({this.syncing = false, this.lastOutcome, this.lastAttempt, this.lastError});

  bool get lastSucceeded => lastOutcome == SyncOutcome.updated || lastOutcome == SyncOutcome.unchanged;
}

/// How many posters one refresh may download at most, and how many failures in
/// a row (the media server being down) stop it.
const _maxArtPerRun = 400;
const _maxArtFailuresInARow = 3;

class ScheduleSyncer {
  final PlayheadApi api;
  final ScheduleCache cache;
  final ArtCache? art;

  /// Builds a program poster URL from the media server's item id and tag.
  final String Function(String itemId, String tag)? artUrlOf;
  final Duration interval;
  final Duration retryInterval;

  final status = ValueNotifier<SyncStatus>(const SyncStatus());
  Timer? _timer;
  Future<SyncOutcome>? _inFlight;
  bool _stopped = false;
  Future<void>? _artRun;

  ScheduleSyncer({
    required this.api,
    required this.cache,
    required this.interval,
    this.retryInterval = const Duration(seconds: 30),
    this.art,
    this.artUrlOf,
  });

  /// Refreshes now, then keeps refreshing on its own.
  void start() {
    _stopped = false;
    unawaited(syncNow());
  }

  void stop() {
    _stopped = true;
    _timer?.cancel();
    _timer = null;
  }

  /// Refreshes now (joining a refresh already running) and schedules the
  /// next one. Never throws.
  Future<SyncOutcome> syncNow() {
    final running = _inFlight;
    if (running != null) return running;
    final run = _sync();
    _inFlight = run;
    run.whenComplete(() {
      if (identical(_inFlight, run)) _inFlight = null;
    });
    return run;
  }

  void _scheduleNext(SyncOutcome outcome) {
    if (_stopped) return;
    _timer?.cancel();
    final ok = outcome == SyncOutcome.updated || outcome == SyncOutcome.unchanged;
    final wait = ok ? interval : (retryInterval < interval ? retryInterval : interval);
    _timer = Timer(wait, () => unawaited(syncNow()));
  }

  Future<SyncOutcome> _sync() async {
    status.value = SyncStatus(syncing: true, lastOutcome: status.value.lastOutcome, lastAttempt: status.value.lastAttempt, lastError: status.value.lastError);
    var outcome = SyncOutcome.failed;
    String? error;
    final clock = Stopwatch()..start();
    try {
      outcome = await _refresh();
    } on PlayheadConnectionException catch (e) {
      outcome = SyncOutcome.offline;
      error = '$e';
    } on PlayheadHttpException catch (e) {
      outcome = e.isAuthRejection ? SyncOutcome.authRejected : SyncOutcome.failed;
      error = '$e';
    } catch (e) {
      outcome = SyncOutcome.failed;
      error = '$e';
    }
    switch (outcome) {
      case SyncOutcome.updated:
        Log.info('sync', 'guide refreshed: updated in ${clock.elapsedMilliseconds}ms');
      case SyncOutcome.unchanged:
        Log.debug('sync', 'guide refreshed: unchanged in ${clock.elapsedMilliseconds}ms');
      case SyncOutcome.offline:
        Log.warn('sync', 'backend unreachable after ${clock.elapsedMilliseconds}ms; keeping the cache: $error');
      case SyncOutcome.authRejected:
        Log.error('sync', 'backend rejected the access token; keeping the cache: $error');
      case SyncOutcome.failed:
        Log.error('sync', 'refresh failed after ${clock.elapsedMilliseconds}ms; keeping the cache: $error');
    }
    status.value = SyncStatus(lastOutcome: outcome, lastAttempt: cache.now(), lastError: error);
    _scheduleNext(outcome);
    if (outcome == SyncOutcome.updated || outcome == SyncOutcome.unchanged) _startArtRun();
    return outcome;
  }

  /// Where the download starts: the top of the previous hour, so the request
  /// (and with it the body and its validator) stays the same for the whole
  /// hour and an unchanged guide answers `304`.
  static DateTime downloadStart(DateTime now) {
    final utc = now.toUtc();
    return DateTime.utc(utc.year, utc.month, utc.day, utc.hour).subtract(const Duration(hours: 1));
  }

  Future<SyncOutcome> _refresh() async {
    final meta = await api.getEpgMeta();
    final channels = await api.listChannels();
    final sortedChannels = [...channels]..sort((a, b) => a.number.compareTo(b.number));
    var changed = false;
    // The window must be in force before the guide is stored (it is the cap).
    cache.setBackendWindow(meta.window);
    if (cache.replaceChannels(sortedChannels)) changed = true;

    final from = downloadStart(cache.now());
    final snapshot = await api.getEpgSnapshot(start: from, etag: cache.etag);
    if (snapshot == null) {
      cache.markSynced(revision: meta.revision);
      return changed ? SyncOutcome.updated : SyncOutcome.unchanged;
    }
    // Against a backend that predates the enriched /epg, resolve what is
    // missing once, here, so the stored copy is complete.
    final resolved = await _resolveLegacy(snapshot.byChannel);
    final changedChannels = cache.applySnapshot(resolved, from: from);
    cache.markSynced(etag: snapshot.etag, revision: snapshot.revision ?? meta.revision);
    return changed || changedChannels.isNotEmpty ? SyncOutcome.updated : SyncOutcome.unchanged;
  }

  Future<Map<String, List<EpgEntry>>> _resolveLegacy(Map<String, List<EpgEntry>> byChannel) async {
    final now = cache.now().toUtc();
    final needs = byChannel.values.any((l) => l.any((e) => e.endAt.isAfter(now) && !e.isResolved));
    if (!needs) return byChannel;
    final out = <String, List<EpgEntry>>{};
    for (final MapEntry(key: channelId, value: list) in byChannel.entries) {
      out[channelId] = await Future.wait(list.map((e) async {
        if (e.isResolved || !e.endAt.isAfter(now)) return e;
        try {
          return await api.resolveCatalog(e);
        } catch (_) {
          return e; // stays unplayable until a later refresh resolves it
        }
      }));
    }
    return out;
  }

  // Poster prefetch runs after the schedule is safely stored and never blocks
  // or fails a refresh.
  void _startArtRun() {
    final art = this.art;
    if (art == null || _artRun != null) return;
    final run = _prefetchArt(art).catchError((Object _) {});
    _artRun = run;
    run.whenComplete(() => _artRun = null);
  }

  /// Completes when the poster prefetch running now (if any) is done.
  Future<void> get artIdle => _artRun ?? Future.value();

  Future<void> _prefetchArt(ArtCache art) async {
    final urls = <String>{};
    final now = cache.now().toUtc();
    for (final channel in cache.channels()) {
      final logo = channel.guideArtUrl;
      if (logo != null && logo.isNotEmpty) urls.add(logo);
    }
    final builder = artUrlOf;
    if (builder != null) {
      for (final channel in cache.channels()) {
        for (final e in cache.entries(channel.id, now, cache.horizonEnd)) {
          final a = e.primaryArt;
          if (a != null) urls.add(builder(a.itemId, a.tag));
        }
      }
    }
    var fetched = 0;
    var failures = 0;
    for (final url in urls) {
      if (_stopped || fetched >= _maxArtPerRun || failures >= _maxArtFailuresInARow) break;
      if (art.cached(url) != null) continue;
      if (await art.fetch(url) == null) {
        failures++;
      } else {
        failures = 0;
        fetched++;
      }
    }
  }
}
