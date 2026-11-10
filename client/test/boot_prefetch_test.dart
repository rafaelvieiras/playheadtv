// Unit test for the head unit the boot prefetch resolves: it has to be the same
// choice playback makes (the program on air, joined where it already started),
// or the prefetched url would be for the wrong item or the wrong position.
import 'package:client/main.dart' show headUnitOf;
import 'package:client/playhead_api.dart';
import 'package:flutter_test/flutter_test.dart';

const _base = 1000000; // any epoch ms; the tests are relative to it

DateTime _at(int ms) => DateTime.fromMillisecondsSinceEpoch(ms, isUtc: true);

Map<String, dynamic> _program(String id, int startMs, int endMs) => {
      'id': 'e-$id',
      'kind': 'program',
      'catalog_item_id': 'ci-$id',
      'start_at': startMs,
      'end_at': endMs,
      'external_id': 'jf-$id',
      'title': 'Title $id',
    };

void main() {
  test('a program on air is joined where it already started', () {
    // Started 40 s ago, runs 120 s.
    final entry = EpgEntry.fromJson(_program('a', _base - 40000, _base + 80000));
    final head = headUnitOf([entry], _at(_base));

    expect(head, isNotNull);
    expect(head!.entry.id, 'jf-a');
    expect(head.seek, const Duration(seconds: 40));
    expect(head.position, const Duration(seconds: 40));
  });

  test('a program that has not started yet is joined at its own zero', () {
    final entry = EpgEntry.fromJson(_program('a', _base + 5000, _base + 65000));
    final head = headUnitOf([entry], _at(_base));
    expect(head!.seek, Duration.zero);
    expect(head.position, Duration.zero);
  });

  test('a program that already ended is dropped, and so is the prefetch', () {
    final ended = EpgEntry.fromJson(_program('a', _base - 200000, _base - 80000));
    // unitsOfProgram drops what ended before `now`, so there is nothing honest
    // to resolve — the player will skip to the next program on its own.
    expect(headUnitOf([ended], _at(_base)), isNull);
  });

  test('nothing known means nothing to prefetch', () {
    expect(headUnitOf(const <EpgEntry>[], _at(_base)), isNull);
  });

  test('a unit with no media item of its own is not prefetched', () {
    final noExternal = EpgEntry.fromJson({..._program('b', _base, _base + 60000), 'external_id': null});
    expect(headUnitOf([noExternal], _at(_base)), isNull);
  });

  test('a tail group prefetches the clip on air, at its offset inside the run', () {
    Map<String, dynamic> clip(String id, int startMs, int endMs) => {
          'id': 'c-$id',
          'catalog_item_id': 'ci-$id',
          'external_id': 'clip-$id',
          'title': 'Clip $id',
          'start_at': startMs,
          'end_at': endMs,
        };
    final group = EpgEntry.fromJson({
      'id': 'e-tail',
      'tenant_id': 't',
      'channel_id': 'c',
      'kind': 'tail',
      'title': 'Tail',
      'start_at': _base,
      'end_at': _base + 30000,
      'clips': [
        clip('1', _base, _base + 10000),
        clip('2', _base + 10000, _base + 30000),
      ],
    });
    final head = headUnitOf([group], _at(_base + 12000));
    expect(head, isNotNull);
    expect(head!.entry.id, 'clip-2', reason: 'the clip on air, not the run');
    expect(head.seek, const Duration(seconds: 2));
  });
}
