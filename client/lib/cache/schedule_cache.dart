// The client's local, persistent copy of the backend's schedule metadata: the
// channel list and every channel's materialized guide for the next window,
// stored in one SQLite file (the same engine on every platform, see
// docs/ARCHITECTURE.md "Client offline cache").
//
// Everything the client needs to play and to show a program is in a row:
// the entry JSON is the backend's own `GET /epg?group_tail=1` shape (titles,
// source ids, art tags, and for a grouped tail EVERY individual clip), so a
// cold start with the backend down plays from this file alone.
//
// What reads from here: the player's working set, the EPG grid, the mosaic and
// channel switching. What writes: only the background [ScheduleSyncer]. A
// network or auth error never touches the contents; only a schema version bump
// (the file is recreated on open) or [clear] (an explicit user action) does.
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:sqlite3/sqlite3.dart';

import '../playhead_api.dart';

/// Bumped whenever the tables or the stored JSON change shape in a way an old
/// file cannot serve: opening a file of another version recreates it empty
/// (the next sync refills it).
const scheduleCacheSchemaVersion = 1;

/// The window assumed for a backend that does not report one.
const defaultBackendWindow = Duration(hours: 48);

/// The client's own cap on the cached window (a setting; this is its default).
const defaultClientWindowLimit = Duration(hours: 48);

/// Entries that ended longer ago than this are dropped by a sync (the guide
/// shows a little of the past, and the OSD looks back up to six hours to
/// attribute a filler to its program; nothing needs more).
const cachePastRetention = Duration(hours: 6);

/// The effective window: the smaller of what the backend materializes and
/// what the client is willing to keep. A backend that reports no window counts
/// as [defaultBackendWindow].
Duration effectiveWindow({Duration? backendWindow, required Duration clientLimit}) {
  final backend = backendWindow ?? defaultBackendWindow;
  return backend < clientLimit ? backend : clientLimit;
}

/// What a write changed, for the player to reconcile with: the channels whose
/// guide differs from what it held, and whether the channel list changed.
class CacheChange {
  final Set<String> channelIds;
  final bool channelsChanged;
  const CacheChange(this.channelIds, {this.channelsChanged = false});
}

class ScheduleCache {
  final Database _db;
  final DateTime Function() _clock;
  final _changes = StreamController<CacheChange>.broadcast();

  /// The client-side cap on the window (see [effectiveWindow]); the setting.
  Duration clientLimit;

  ScheduleCache._(this._db, this.clientLimit, this._clock);

  /// Opens (creating it if needed) the cache file at [path].
  factory ScheduleCache.open(String path, {Duration clientLimit = defaultClientWindowLimit, DateTime Function()? clock}) {
    final parent = File(path).parent;
    if (!parent.existsSync()) parent.createSync(recursive: true);
    return ScheduleCache._(_prepare(sqlite3.open(path)), clientLimit, clock ?? DateTime.now);
  }

  factory ScheduleCache.inMemory({Duration clientLimit = defaultClientWindowLimit, DateTime Function()? clock}) =>
      ScheduleCache._(_prepare(sqlite3.openInMemory()), clientLimit, clock ?? DateTime.now);

  static Database _prepare(Database db) {
    final version = db.select('PRAGMA user_version').first.values.first as int;
    if (version != scheduleCacheSchemaVersion) {
      // Another schema (an older app's file, or none yet): start over.
      for (final table in ['epg_entry', 'channel', 'meta']) {
        db.execute('DROP TABLE IF EXISTS $table');
      }
    }
    db.execute('CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)');
    db.execute('CREATE TABLE IF NOT EXISTS channel (id TEXT PRIMARY KEY, position INTEGER NOT NULL, json TEXT NOT NULL)');
    db.execute('''
      CREATE TABLE IF NOT EXISTS epg_entry (
        channel_id TEXT NOT NULL,
        id TEXT NOT NULL,
        start_at INTEGER NOT NULL,
        end_at INTEGER NOT NULL,
        json TEXT NOT NULL,
        PRIMARY KEY (channel_id, id)
      )''');
    db.execute('CREATE INDEX IF NOT EXISTS epg_entry_time ON epg_entry (channel_id, start_at)');
    db.execute('PRAGMA user_version = $scheduleCacheSchemaVersion');
    return db;
  }

  void close() {
    unawaited(_changes.close());
    _db.close();
  }

  /// Fires after a write that changed something. Not for a refresh that found
  /// the same schedule.
  Stream<CacheChange> get changes => _changes.stream;

  DateTime now() => _clock();

  // --- meta ---

  String? _getMeta(String key) {
    final rows = _db.select('SELECT value FROM meta WHERE key = ?', [key]);
    return rows.isEmpty ? null : rows.first['value'] as String;
  }

  void _setMeta(String key, String? value) {
    if (value == null) {
      _db.execute('DELETE FROM meta WHERE key = ?', [key]);
    } else {
      _db.execute('INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value', [key, value]);
    }
  }

  /// Validator of the copy held (what `If-None-Match` carries), only valid for
  /// the window it was downloaded under: null when the client limit moved
  /// since, so the next refresh downloads again.
  String? get etag => _getMeta('limit_ms') == '${clientLimit.inMilliseconds}' ? _getMeta('etag') : null;

  int? get revision => int.tryParse(_getMeta('revision') ?? '');

  /// When the last successful refresh finished (null: never synced).
  DateTime? get lastSyncAt {
    final ms = int.tryParse(_getMeta('last_sync_at') ?? '');
    return ms == null ? null : DateTime.fromMillisecondsSinceEpoch(ms, isUtc: true);
  }

  /// The window the backend reported at the last refresh, if it did.
  Duration? get backendWindow {
    final ms = int.tryParse(_getMeta('backend_window_ms') ?? '');
    return ms == null ? null : Duration(milliseconds: ms);
  }

  /// The window in force: min(backend window, [clientLimit]).
  Duration get window => effectiveWindow(backendWindow: backendWindow, clientLimit: clientLimit);

  /// Where programming stops being served: now + [window].
  DateTime get horizonEnd => _clock().toUtc().add(window);

  /// Records the window the backend reported (it is the cap [applySnapshot]
  /// applies, so it is set before the guide is stored). Null (a backend that
  /// reports none) keeps what was known.
  void setBackendWindow(Duration? window) {
    if (window != null) _setMeta('backend_window_ms', '${window.inMilliseconds}');
  }

  /// Records a successful refresh (even a 304: it still proves the copy is
  /// current as of now).
  void markSynced({String? etag, int? revision}) {
    _db.execute('BEGIN');
    try {
      if (etag != null) {
        _setMeta('etag', etag);
        _setMeta('limit_ms', '${clientLimit.inMilliseconds}');
      }
      if (revision != null) _setMeta('revision', '$revision');
      _setMeta('last_sync_at', '${_clock().toUtc().millisecondsSinceEpoch}');
      _db.execute('COMMIT');
    } catch (_) {
      _db.execute('ROLLBACK');
      rethrow;
    }
  }

  // --- channels ---

  List<PlayheadChannel> channels() => [
        for (final row in _db.select('SELECT json FROM channel ORDER BY position'))
          PlayheadChannel.fromJson(jsonDecode(row['json'] as String) as Map<String, dynamic>),
      ];

  /// Replaces the channel list (and drops the guide of a channel that is gone).
  /// True when it differs from what was held.
  bool replaceChannels(List<PlayheadChannel> channels) {
    final encoded = [for (final c in channels) jsonEncode(c.toJson())];
    final held = [for (final row in _db.select('SELECT json FROM channel ORDER BY position')) row['json'] as String];
    if (_sameList(encoded, held)) return false;
    _db.execute('BEGIN');
    try {
      _db.execute('DELETE FROM channel');
      final insert = _db.prepare('INSERT INTO channel (id, position, json) VALUES (?, ?, ?)');
      for (var i = 0; i < channels.length; i++) {
        insert.execute([channels[i].id, i, encoded[i]]);
      }
      insert.close();
      _db.execute('DELETE FROM epg_entry WHERE channel_id NOT IN (SELECT id FROM channel)');
      _db.execute('COMMIT');
    } catch (_) {
      _db.execute('ROLLBACK');
      rethrow;
    }
    _emit(const CacheChange({}, channelsChanged: true));
    return true;
  }

  // --- guide ---

  EpgEntry _entryOf(Row row) => EpgEntry.fromJson(jsonDecode(row['json'] as String) as Map<String, dynamic>);

  /// The channel's entries overlapping [start, end), ordered by start and
  /// never beyond the window ([horizonEnd]).
  List<EpgEntry> entries(String channelId, DateTime start, DateTime end) {
    final limit = horizonEnd;
    final cappedEnd = end.isAfter(limit) ? limit : end;
    return [
      for (final row in _db.select(
        'SELECT json FROM epg_entry WHERE channel_id = ? AND end_at > ? AND start_at < ? ORDER BY start_at, end_at',
        [channelId, start.toUtc().millisecondsSinceEpoch, cappedEnd.toUtc().millisecondsSinceEpoch],
      ))
        _entryOf(row),
    ];
  }

  /// Entries starting at or after [from], up to the window.
  List<EpgEntry> entriesFrom(String channelId, DateTime from) => [
        for (final row in _db.select(
          'SELECT json FROM epg_entry WHERE channel_id = ? AND start_at >= ? AND start_at < ? ORDER BY start_at, end_at',
          [channelId, from.toUtc().millisecondsSinceEpoch, horizonEnd.millisecondsSinceEpoch],
        ))
          _entryOf(row),
      ];

  /// Every cached entry of the channel, regardless of the window (what the
  /// "loop the last known programming" fallback replays).
  List<EpgEntry> allEntries(String channelId) => [
        for (final row in _db.select('SELECT json FROM epg_entry WHERE channel_id = ? ORDER BY start_at, end_at', [channelId])) _entryOf(row),
      ];

  /// Whether anything of the channel is still on air or ahead at [t].
  bool hasProgrammingAfter(String channelId, DateTime t) =>
      _db.select(
        'SELECT 1 FROM epg_entry WHERE channel_id = ? AND end_at > ? AND start_at < ? LIMIT 1',
        [channelId, t.toUtc().millisecondsSinceEpoch, horizonEnd.millisecondsSinceEpoch],
      ).isNotEmpty;

  /// Replaces what the channels hold from [from] on with [snapshot] (entries
  /// the window does not reach are not stored) and prunes entries long past.
  /// Returns, and emits, the channels whose guide changed. A channel the
  /// snapshot has no entry for is emptied from [from] on: the backend has
  /// nothing there any more.
  Set<String> applySnapshot(Map<String, List<EpgEntry>> byChannel, {required DateTime from}) {
    final fromMs = from.toUtc().millisecondsSinceEpoch;
    final limitMs = horizonEnd.millisecondsSinceEpoch;
    final pruneMs = _clock().toUtc().subtract(cachePastRetention).millisecondsSinceEpoch;
    final changed = <String>{};
    _db.execute('BEGIN');
    try {
      final channelIds = {for (final row in _db.select('SELECT id FROM channel')) row['id'] as String};
      final insert = _db.prepare('INSERT INTO epg_entry (channel_id, id, start_at, end_at, json) VALUES (?, ?, ?, ?, ?)');
      for (final id in channelIds) {
        final fresh = [
          for (final e in byChannel[id] ?? const <EpgEntry>[])
            if (e.startAt.millisecondsSinceEpoch < limitMs) e,
        ];
        final encoded = [for (final e in fresh) jsonEncode(e.toJson())];
        final held = [
          for (final row in _db.select(
            'SELECT json FROM epg_entry WHERE channel_id = ? AND end_at >= ? AND start_at < ? ORDER BY start_at, end_at, id',
            [id, fromMs, limitMs],
          ))
            row['json'] as String,
        ];
        final heldSorted = [...held]..sort();
        final freshSorted = [...encoded]..sort();
        if (_sameList(heldSorted, freshSorted)) continue;
        changed.add(id);
        _db.execute('DELETE FROM epg_entry WHERE channel_id = ? AND end_at >= ?', [id, fromMs]);
        for (var i = 0; i < fresh.length; i++) {
          insert.execute([id, fresh[i].id, fresh[i].startAt.millisecondsSinceEpoch, fresh[i].endAt.millisecondsSinceEpoch, encoded[i]]);
        }
      }
      insert.close();
      _db.execute('DELETE FROM epg_entry WHERE end_at < ?', [pruneMs]);
      _db.execute('COMMIT');
    } catch (_) {
      _db.execute('ROLLBACK');
      rethrow;
    }
    if (changed.isNotEmpty) _emit(CacheChange(changed));
    return changed;
  }

  /// Empties the cache (an explicit user action, never done on an error).
  void clear() {
    _db.execute('BEGIN');
    try {
      _db.execute('DELETE FROM epg_entry');
      _db.execute('DELETE FROM channel');
      _db.execute('DELETE FROM meta');
      _db.execute('COMMIT');
    } catch (_) {
      _db.execute('ROLLBACK');
      rethrow;
    }
    _emit(const CacheChange({}, channelsChanged: true));
  }

  void _emit(CacheChange change) {
    if (!_changes.isClosed) _changes.add(change);
  }
}

bool _sameList(List<String> a, List<String> b) {
  if (a.length != b.length) return false;
  for (var i = 0; i < a.length; i++) {
    if (a[i] != b[i]) return false;
  }
  return true;
}
