// What the client asks a plugin for and what it makes of the answer
// (docs/PLUGINS.md "Modes"): the modes it declares, the pair it can merge, and
// the rule that decides whether the player still has to seek.
import 'dart:convert';

import 'package:client/playhead_api.dart';
import 'package:client/video/video_engine.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

void main() {
  group('what the player declares it can play', () {
    test('an engine that merges asks for the pair first, then a remux', () {
      final engine = _Engine(paired: true);
      expect(pluginPlaybackModes(engine), ['pair', 'remux']);
    });

    test('an engine that cannot merge asks for the remux alone, never the pair', () {
      final engine = _Engine(paired: false);
      expect(pluginPlaybackModes(engine), ['remux']);
    });
  });

  group('reading the answer', () {
    test('a remux starts where it was asked to', () async {
      final api = _api({'mode': 'remux', 'url': 'http://host.example/s/tok/index.m3u8'});
      final stream = await api.playUrl('item-1', startAt: const Duration(seconds: 90));
      expect(stream.mode, 'remux');
      expect(stream.audioUri, isNull);
      expect(stream.startsAtRequestedPosition, isTrue,
          reason: 'the plugin started its stream at the requested position');
    });

    test('a pair carries both addresses and is seeked by the player', () async {
      final api = _api({
        'mode': 'pair',
        'url': 'http://host.example/v?itag=137',
        'audio_url': 'http://host.example/a?itag=140',
      });
      final stream = await api.playUrl('item-1');
      expect(stream.mode, 'pair');
      expect(stream.uri.toString(), 'http://host.example/v?itag=137');
      expect(stream.audioUri.toString(), 'http://host.example/a?itag=140');
      expect(stream.startsAtRequestedPosition, isFalse,
          reason: 'a pair is the whole item from its own zero');
    });

    test('a single file is seeked by the player too', () async {
      final api = _api({'mode': 'single', 'url': 'http://host.example/s.mp4'});
      final stream = await api.playUrl('item-1');
      expect(stream.audioUri, isNull);
      expect(stream.startsAtRequestedPosition, isFalse);
    });
  });

  group('what it asks for', () {
    test('the modes travel in the order given', () async {
      final seen = <Uri>[];
      final api = _api({'mode': 'remux', 'url': 'http://host.example/x'}, onRequest: seen.add);
      await api.playUrl('item 1', startAt: const Duration(seconds: 5), modes: const ['single', 'pair']);
      expect(seen.single.path, contains('item%201'));
      expect(seen.single.queryParameters, {'start_ms': '5000', 'modes': 'single,pair'});
    });

    test('no modes asked for means no modes param', () async {
      final seen = <Uri>[];
      final api = _api({'mode': 'remux', 'url': 'http://host.example/x'}, onRequest: seen.add);
      await api.playUrl('item-1', modes: const []);
      expect(seen.single.queryParameters, isEmpty);
    });

    test('an answer with no url is an error, not a silent empty playback', () async {
      final api = _api({'mode': 'pair'});
      expect(api.playUrl('item-1'), throwsA(isA<Exception>()));
    });
  });
}

PlayheadApi _api(Map<String, dynamic> answer, {void Function(Uri)? onRequest}) => PlayheadApi(
      baseUrl: 'http://127.0.0.1:1',
      client: MockClient((request) async {
        onRequest?.call(request.url);
        return http.Response(jsonEncode(answer), 200, headers: {'content-type': 'application/json'});
      }),
    );

class _Engine implements VideoEngine {
  _Engine({required this.paired});
  final bool paired;

  @override
  String get name => 'test';
  @override
  String get decoderSummary => 'none';
  @override
  bool get supportsPairedAudio => paired;
  @override
  void register() {}
  @override
  VideoPlayback createNetwork(Uri uri, {Uri? audioUri}) => throw UnimplementedError();
  @override
  VideoPlayback createAsset(String assetPath) => throw UnimplementedError();
}
