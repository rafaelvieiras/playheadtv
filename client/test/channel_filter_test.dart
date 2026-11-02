import 'dart:convert';
import 'dart:io';

import 'package:client/cache/schedule_cache.dart';
import 'package:client/playhead_api.dart';
import 'package:client/plugin/channel_filter.dart';
import 'package:client/plugin/plugin_manifest.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

DateTime get _t0 => DateTime.utc(2026, 11, 2, 12);

EpgEntry _entry({
  required String id,
  required String channelId,
  List<EpgRequirement> requires = const [],
  int startOffsetMinutes = 0,
}) =>
    EpgEntry(
      id: id,
      kind: 'program',
      catalogItemId: 'item-$id',
      startAt: _t0.add(Duration(minutes: startOffsetMinutes)),
      endAt: _t0.add(Duration(minutes: startOffsetMinutes + 30)),
      title: 'Program $id',
      requires: requires,
    );

void main() {
  late Directory dir;
  late ScheduleCache cache;

  setUp(() {
    dir = Directory.systemTemp.createTempSync('channel_filter_test_');
    cache = ScheduleCache.open('${dir.path}/cache.db', clock: () => _t0);
    cache.replaceChannels([
      PlayheadChannel(id: 'plain', number: 1, name: 'Plain'),
      PlayheadChannel(id: 'plugin', number: 2, name: 'From a plugin'),
      PlayheadChannel(id: 'empty', number: 3, name: 'Never synced'),
    ]);
    cache.applySnapshot({
      'plain': [_entry(id: 'p1', channelId: 'plain')],
      'plugin': [
        _entry(
          id: 'x1',
          channelId: 'plugin',
          requires: const [EpgRequirement(id: 'fake', minVersion: '1.0.0', config: {'url': 'u'})],
        ),
      ],
    }, from: _t0);
  });

  tearDown(() {
    cache.close();
    dir.deleteSync(recursive: true);
  });

  test('a channel without requirements is always visible', () {
    final result = playableChannels(cache, cache.channels(), PluginAvailability.allUsable);
    expect(result.visible.map((c) => c.id), contains('plain'));
    expect(result.hidden, isEmpty);
  });

  test('requirements come from the cached guide, not from the channel', () {
    // allUsable is the state before anything was checked: it hides nothing.
    expect(PluginAvailability.allUsable.isEmpty, isTrue);
    expect(requirementsOf(cache, 'plugin').keys, ['fake']);
    expect(requirementsOf(cache, 'plain'), isEmpty);
  });

  test('a channel with no cached entry stays visible', () {
    final result = playableChannels(cache, cache.channels(), PluginAvailability.allUsable);
    expect(result.visible.map((c) => c.id), contains('empty'));
  });

  test('a channel needing a plugin that is not installed is hidden', () async {
    final availability = await PluginAvailability.compute(cache, cache.channels(), _api(installed: const []));
    final result = playableChannels(cache, cache.channels(), availability);

    expect(result.visible.map((c) => c.id), isNot(contains('plugin')));
    expect(result.hidden.single.channel.id, 'plugin');
    expect(result.hidden.single.reason, contains('not installed'));
    expect(result.allHiddenByPlugins, isFalse);
  });

  test('a channel needing an installed plugin stays visible', () async {
    final availability = await PluginAvailability.compute(
        cache, cache.channels(), _api(installed: _installedSection('1.2.0')));
    expect(availability.isEmpty, isTrue);
    expect(playableChannels(cache, cache.channels(), availability).hidden, isEmpty);
  });

  test('an installed plugin older than the guide needs hides the channel', () async {
    final availability = await PluginAvailability.compute(
        cache, cache.channels(), _api(installed: _installedSection('0.9.0')));
    final result = playableChannels(cache, cache.channels(), availability);
    expect(result.hidden.single.reason, contains('1.0.0'));
  });

  test('requirements keep the strictest minimum across a channel', () {
    cache.applySnapshot({
      'plugin': [
        _entry(id: 'x1', channelId: 'plugin', requires: const [EpgRequirement(id: 'fake', minVersion: '1.0.0')]),
        _entry(
          id: 'x2',
          channelId: 'plugin',
          startOffsetMinutes: 30,
          requires: const [EpgRequirement(id: 'fake', minVersion: '2.0.0')],
        ),
      ],
    }, from: _t0);
    expect(requirementsOf(cache, 'plugin')['fake']!.minVersion, '2.0.0');
  });
}

/// A backend that answers `GET /plugins` with the given installed plugins.
PlayheadApi _api({required List<Map<String, dynamic>> installed, bool configured = true}) {
  final client = MockClient((request) async => http.Response(
        jsonEncode({
          'registry_configured': configured,
          'installed': installed,
          'plugins': [
            {
              'id': 'fake',
              'name': 'Fake',
              'description': '',
              'versions': [
                {
                  'version': '1.2.0',
                  'protocol': 1,
                  'artifacts': [
                    {
                      'os': 'linux',
                      'arch': 'amd64',
                      'url': '/plugins/fake/1.2.0/linux-amd64',
                      'sha256': 'a' * 64,
                      'size': 1,
                    },
                  ],
                },
              ],
            },
          ],
        }),
        200,
        headers: {'content-type': 'application/json'},
      ));
  return PlayheadApi(baseUrl: 'http://backend.test:9394', client: client);
}

List<Map<String, dynamic>> _installedSection(String version) => [
      {
        'id': 'fake',
        'version': version,
        'protocol': 1,
        'name': 'Fake',
        'capabilities': 'media.resolve',
        'enabled': true,
        'os': 'linux',
        'arch': 'amd64',
      },
    ];
