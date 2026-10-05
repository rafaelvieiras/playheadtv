// Unit tests (in-memory and temp-file SQLite, no backend) for the offline
// schedule cache: the window cap, reads, change detection on a refresh,
// persistence, the schema-version rule and that only an explicit clear empties
// it.
import 'dart:io';

import 'package:client/cache/schedule_cache.dart';
import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:sqlite3/sqlite3.dart';

final _t0 = DateTime.utc(2026, 10, 3, 12);

EpgEntry _entry(String id, DateTime start, Duration length, {String kind = 'program', String? title}) => EpgEntry(
      id: id,
      kind: kind,
      catalogItemId: 'ci-$id',
      startAt: start,
      endAt: start.add(length),
      externalId: 'jf-$id',
      title: title ?? 'Title $id',
      imagePrimaryTag: 'tag-$id',
    );

EpgEntry _tail(String id, DateTime start, int clips) => EpgEntry(
      id: id,
      kind: 'tail',
      catalogItemId: 'ci-$id-0',
      startAt: start,
      endAt: start.add(Duration(seconds: 10 * clips)),
      title: 'Tail',
      clips: [
        for (var i = 0; i < clips; i++)
          EpgClip(
            id: '$id-$i',
            catalogItemId: 'ci-$id-$i',
            externalId: 'jf-$id-$i',
            title: 'Clip $i',
            startAt: start.add(Duration(seconds: 10 * i)),
            endAt: start.add(Duration(seconds: 10 * (i + 1))),
            durationMs: 10000,
          ),
      ],
    );

ScheduleCache _cache({Duration clientLimit = const Duration(hours: 48), Duration? backendWindow, DateTime? now}) {
  final clock = now ?? _t0;
  final cache = ScheduleCache.inMemory(clientLimit: clientLimit, clock: () => clock);
  cache.setBackendWindow(backendWindow);
  cache.replaceChannels([PlayheadChannel(id: 'c1', number: 1, name: 'One'), PlayheadChannel(id: 'c2', number: 2, name: 'Two')]);
  return cache;
}

void main() {
  group('window cap', () {
    test('the effective window is the smaller of the backend window and the client limit', () {
      expect(effectiveWindow(backendWindow: const Duration(hours: 72), clientLimit: const Duration(hours: 48)), const Duration(hours: 48));
      expect(effectiveWindow(backendWindow: const Duration(hours: 24), clientLimit: const Duration(hours: 48)), const Duration(hours: 24));
      expect(effectiveWindow(backendWindow: const Duration(hours: 48), clientLimit: const Duration(hours: 48)), const Duration(hours: 48));
      expect(effectiveWindow(backendWindow: const Duration(hours: 168), clientLimit: const Duration(hours: 6)), const Duration(hours: 6));
    });

    test('a backend that reports no window counts as 48 hours', () {
      expect(effectiveWindow(clientLimit: const Duration(hours: 72)), const Duration(hours: 48));
      expect(effectiveWindow(clientLimit: const Duration(hours: 12)), const Duration(hours: 12));
    });

    test('reads never go beyond the window, whichever side caps it', () {
      final entries = [for (var h = 0; h < 80; h++) _entry('p$h', _t0.add(Duration(hours: h)), const Duration(hours: 1))];

      // Client limit 24h under a 72h backend window.
      var cache = _cache(clientLimit: const Duration(hours: 24), backendWindow: const Duration(hours: 72));
      cache.applySnapshot({'c1': entries}, from: _t0);
      expect(cache.window, const Duration(hours: 24));
      var read = cache.entries('c1', _t0, _t0.add(const Duration(hours: 100)));
      expect(read.length, 24);
      expect(read.last.id, 'p23');
      expect(cache.entriesFrom('c1', _t0).length, 24);

      // Backend window 36h under a 48h client limit.
      cache = _cache(backendWindow: const Duration(hours: 36));
      cache.applySnapshot({'c1': entries}, from: _t0);
      read = cache.entries('c1', _t0, _t0.add(const Duration(hours: 100)));
      expect(read.length, 36);

      // A larger limit than the backend's changes nothing.
      cache = _cache(clientLimit: const Duration(hours: 168), backendWindow: const Duration(hours: 48));
      cache.applySnapshot({'c1': entries}, from: _t0);
      expect(cache.entries('c1', _t0, _t0.add(const Duration(hours: 100))).length, 48);
    });

    test('lowering the limit hides what is already stored, raising it makes a refresh download again', () {
      final entries = [for (var h = 0; h < 48; h++) _entry('p$h', _t0.add(Duration(hours: h)), const Duration(hours: 1))];
      final cache = _cache(backendWindow: const Duration(hours: 48));
      cache.applySnapshot({'c1': entries}, from: _t0);
      cache.markSynced(etag: '"v1"', revision: 1);
      expect(cache.etag, '"v1"');

      cache.clientLimit = const Duration(hours: 12);
      expect(cache.entries('c1', _t0, _t0.add(const Duration(hours: 100))).length, 12);
      // The validator belongs to the 48h download: not offered for another limit.
      expect(cache.etag, isNull);
      cache.clientLimit = const Duration(hours: 48);
      expect(cache.etag, '"v1"');
    });

    test('a snapshot stores nothing past the window', () {
      final cache = _cache(clientLimit: const Duration(hours: 6), backendWindow: const Duration(hours: 48));
      cache.applySnapshot({
        'c1': [for (var h = 0; h < 20; h++) _entry('p$h', _t0.add(Duration(hours: h)), const Duration(hours: 1))],
      }, from: _t0);
      expect(cache.allEntries('c1').length, 6);
    });
  });

  group('contents', () {
    test('entries come back as stored, a grouped tail with every clip', () {
      final cache = _cache();
      cache.applySnapshot({
        'c1': [_entry('a', _t0, const Duration(hours: 1)), _tail('t', _t0.add(const Duration(hours: 1)), 40)],
      }, from: _t0);
      final read = cache.entries('c1', _t0, _t0.add(const Duration(hours: 3)));
      expect(read.map((e) => e.id), ['a', 't']);
      expect(read[0].title, 'Title a');
      expect(read[0].primaryArt, (itemId: 'jf-a', tag: 'tag-a'));
      expect(read[1].isTailGroup, isTrue);
      expect(read[1].clips.length, 40);
      expect(read[1].clips[39].externalId, 'jf-t-39');
      expect(read[1].isResolved, isTrue);
    });

    test('entries overlapping the span are returned, ordered, per channel', () {
      final cache = _cache();
      cache.applySnapshot({
        'c1': [_entry('late', _t0.add(const Duration(hours: 5)), const Duration(hours: 1)), _entry('now', _t0.subtract(const Duration(minutes: 30)), const Duration(hours: 1))],
        'c2': [_entry('other', _t0, const Duration(hours: 1))],
      }, from: _t0.subtract(const Duration(hours: 1)));
      expect(cache.entries('c1', _t0, _t0.add(const Duration(hours: 1))).map((e) => e.id), ['now']);
      expect(cache.entries('c1', _t0, _t0.add(const Duration(hours: 6))).map((e) => e.id), ['now', 'late']);
      expect(cache.entries('c2', _t0, _t0.add(const Duration(hours: 1))).map((e) => e.id), ['other']);
    });

    test('hasProgrammingAfter tells a cache that ran out', () {
      final cache = _cache();
      cache.applySnapshot({'c1': [_entry('a', _t0, const Duration(hours: 1))]}, from: _t0);
      expect(cache.hasProgrammingAfter('c1', _t0), isTrue);
      expect(cache.hasProgrammingAfter('c1', _t0.add(const Duration(hours: 2))), isFalse);
      expect(cache.hasProgrammingAfter('c2', _t0), isFalse);
    });

    test('the channel list keeps its order and drops the guide of a removed channel', () {
      final cache = _cache();
      cache.applySnapshot({'c2': [_entry('x', _t0, const Duration(hours: 1))]}, from: _t0);
      expect(cache.channels().map((c) => c.id), ['c1', 'c2']);
      expect(cache.replaceChannels([PlayheadChannel(id: 'c1', number: 1, name: 'One')]), isTrue);
      expect(cache.allEntries('c2'), isEmpty);
      expect(cache.replaceChannels([PlayheadChannel(id: 'c1', number: 1, name: 'One')]), isFalse);
    });
  });

  group('refresh', () {
    test('reports the channels whose guide changed and emits one change', () async {
      final cache = _cache();
      final first = {
        'c1': [_entry('a', _t0, const Duration(hours: 1))],
        'c2': [_entry('b', _t0, const Duration(hours: 1))],
      };
      final changes = <CacheChange>[];
      cache.changes.listen(changes.add);
      expect(cache.applySnapshot(first, from: _t0), {'c1', 'c2'});

      // The same schedule again: nothing changed, nothing emitted.
      expect(cache.applySnapshot(first, from: _t0), isEmpty);

      // Only c2 differs (a retitled program).
      final second = {
        'c1': first['c1']!,
        'c2': [_entry('b', _t0, const Duration(hours: 1), title: 'Renamed')],
      };
      expect(cache.applySnapshot(second, from: _t0), {'c2'});
      expect(cache.entries('c2', _t0, _t0.add(const Duration(hours: 1))).single.title, 'Renamed');

      // c1 emptied on the backend.
      expect(cache.applySnapshot({'c2': second['c2']!}, from: _t0), {'c1'});
      expect(cache.allEntries('c1'), isEmpty);

      await Future<void>.delayed(Duration.zero);
      expect(changes.map((c) => c.channelIds.toList()), [
        ['c1', 'c2'],
        ['c2'],
        ['c1'],
      ]);
    });

    test('only what ended long ago is pruned', () {
      final cache = _cache();
      cache.applySnapshot({
        'c1': [
          _entry('old', _t0.subtract(const Duration(hours: 9)), const Duration(hours: 1)),
          _entry('recent', _t0.subtract(const Duration(minutes: 90)), const Duration(hours: 1)),
          _entry('now', _t0, const Duration(hours: 1)),
        ],
      }, from: _t0.subtract(const Duration(hours: 10)));
      expect(cache.allEntries('c1').map((e) => e.id), ['recent', 'now']);
    });

    test('markSynced records the validator, revision and time', () {
      final cache = _cache();
      expect(cache.lastSyncAt, isNull);
      expect(cache.etag, isNull);
      cache.markSynced(etag: '"7-abc"', revision: 7);
      expect(cache.etag, '"7-abc"');
      expect(cache.revision, 7);
      expect(cache.lastSyncAt, _t0);
      // A refresh that found nothing new keeps the validator.
      cache.markSynced(revision: 8);
      expect(cache.etag, '"7-abc"');
      expect(cache.revision, 8);
    });
  });

  group('persistence', () {
    late Directory dir;
    setUp(() => dir = Directory.systemTemp.createTempSync('schedule_cache_test_'));
    tearDown(() => dir.deleteSync(recursive: true));

    test('survives closing and reopening the file', () {
      final path = '${dir.path}/sub/cache.db';
      var cache = ScheduleCache.open(path, clock: () => _t0);
      cache.replaceChannels([PlayheadChannel(id: 'c1', number: 1, name: 'One', guideArtUrl: 'http://x/logo.png')]);
      cache.applySnapshot({'c1': [_entry('a', _t0, const Duration(hours: 1)), _tail('t', _t0.add(const Duration(hours: 1)), 5)]}, from: _t0);
      cache.markSynced(etag: '"e"', revision: 3);
      cache.close();

      cache = ScheduleCache.open(path, clock: () => _t0);
      expect(cache.channels().single.guideArtUrl, 'http://x/logo.png');
      expect(cache.entries('c1', _t0, _t0.add(const Duration(hours: 3))).map((e) => e.id), ['a', 't']);
      expect(cache.entries('c1', _t0, _t0.add(const Duration(hours: 3))).last.clips.length, 5);
      expect(cache.revision, 3);
      cache.close();
    });

    test('a file of another schema version is recreated empty', () {
      final path = '${dir.path}/cache.db';
      final cache = ScheduleCache.open(path, clock: () => _t0);
      cache.replaceChannels([PlayheadChannel(id: 'c1', number: 1, name: 'One')]);
      cache.close();

      final raw = sqlite3.open(path);
      raw.execute('PRAGMA user_version = ${scheduleCacheSchemaVersion + 1}');
      raw.close();

      final reopened = ScheduleCache.open(path, clock: () => _t0);
      expect(reopened.channels(), isEmpty);
      expect(reopened.lastSyncAt, isNull);
      // ...and it is usable at the current version again.
      reopened.replaceChannels([PlayheadChannel(id: 'c9', number: 9, name: 'Nine')]);
      reopened.close();
      final again = ScheduleCache.open(path, clock: () => _t0);
      expect(again.channels().single.id, 'c9');
      again.close();
    });

    test('the same schema version keeps everything (no wipe on open)', () {
      final path = '${dir.path}/cache.db';
      var cache = ScheduleCache.open(path, clock: () => _t0);
      cache.replaceChannels([PlayheadChannel(id: 'c1', number: 1, name: 'One')]);
      cache.close();
      cache = ScheduleCache.open(path, clock: () => _t0);
      expect(cache.channels(), hasLength(1));
      cache.close();
    });
  });

  test('clear (the explicit user action) empties everything', () {
    final cache = _cache();
    cache.applySnapshot({'c1': [_entry('a', _t0, const Duration(hours: 1))]}, from: _t0);
    cache.markSynced(etag: '"e"', revision: 1);
    cache.clear();
    expect(cache.channels(), isEmpty);
    expect(cache.allEntries('c1'), isEmpty);
    expect(cache.etag, isNull);
    expect(cache.lastSyncAt, isNull);
  });
}
