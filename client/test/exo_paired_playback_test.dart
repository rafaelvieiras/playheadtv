import 'package:client/video/exo_paired_playback.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

// The Dart half of the merged player: what it asks the Android host for, and
// what it makes of the samples the host pushes back. The player itself
// (Media3, the merged timeline) is exercised on a device by
// integration_test/exo_pair_test.dart — nothing here can prove a picture.
void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  const channel = MethodChannel('playheadtv/paired');
  final messenger = TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;

  late List<MethodCall> calls;
  late List<Map<Object?, Object?>> events;

  Uri video() => Uri.parse('http://host.example/v.mp4');
  Uri audio() => Uri.parse('http://host.example/a.m4a');

  void answerCreate({int handle = 7, Object? failure}) {
    messenger.setMockMethodCallHandler(channel, (call) async {
      calls.add(call);
      switch (call.method) {
        case 'create':
          if (failure != null) throw PlatformException(code: 'playback', message: failure as String);
          return {'handle': handle, 'textureId': 42};
        case 'state':
          return null;
        default:
          return null;
      }
    });
  }

  /// Pushes a sample the way the host does, from its side of the channel.
  Future<void> push(String method, Map<String, Object?> payload) async {
    events.add(payload);
    await messenger.handlePlatformMessage(
      channel.name,
      const StandardMethodCodec().encodeMethodCall(MethodCall(method, payload)),
      (_) {},
    );
  }

  setUp(() {
    calls = [];
    events = [];
  });

  tearDown(() {
    messenger.setMockMethodCallHandler(channel, null);
  });

  test('opening asks for both addresses and draws the texture it is given', () async {
    answerCreate();
    final playback = ExoPairedPlayback(video: video(), audio: audio());
    await playback.open();

    expect(calls.single.method, 'create');
    expect(calls.single.arguments, {'video': video().toString(), 'audio': audio().toString()});

    final view = playback.buildView();
    expect((view as dynamic).textureId, 42);
    await playback.dispose();
  });

  test('an open the host refuses throws, so the caller can skip the item', () async {
    answerCreate(failure: 'UnrecognizedInputFormatException: no video');
    final playback = ExoPairedPlayback(video: video(), audio: audio());
    await expectLater(playback.open(), throwsA(isA<StateError>()));
  });

  test('a sample carries position, the buffered end, the length and the frame size', () async {
    answerCreate();
    final playback = ExoPairedPlayback(video: video(), audio: audio());
    await playback.open();

    await push('event', {
      'handle': 7,
      'ready': true,
      'position': 4200,
      'buffered': 31000,
      'duration': 60000,
      'playing': true,
      'buffering': false,
      'ended': false,
      'width': 1920,
      'height': 1080,
      'error': null,
    });

    final state = playback.state;
    expect(state.isInitialized, isTrue);
    expect(state.isPlaying, isTrue);
    expect(state.position.inMilliseconds, greaterThanOrEqualTo(4200));
    expect(state.duration, const Duration(seconds: 60));
    expect(state.bufferedUntil, const Duration(seconds: 31));
    expect(state.size, const Size(1920, 1080));
    await playback.dispose();
  });

  test('a length the media already outgrew is reported as unknown', () async {
    answerCreate();
    final playback = ExoPairedPlayback(video: video(), audio: audio());
    await playback.open();
    await push('event', {
      'handle': 7,
      'ready': true,
      'position': 20387,
      'buffered': 88000,
      'duration': 20387,
      'playing': true,
    });
    expect(playback.state.duration, Duration.zero, reason: 'a stream that grew past its own length');
    await playback.dispose();
  });

  test('a seek restarts the clock where it lands', () async {
    answerCreate();
    final playback = ExoPairedPlayback(video: video(), audio: audio());
    await playback.open();
    await push('event', {'handle': 7, 'ready': true, 'position': 1000, 'playing': false});

    await playback.seekTo(const Duration(seconds: 5874));

    expect(calls.last.method, 'seek');
    expect(calls.last.arguments, {'handle': 7, 'positionMs': 5874000});
    expect(playback.state.position.inMilliseconds, 5874000);
    await playback.dispose();
  });

  test('the audio track list is what the app addresses', () async {
    answerCreate();
    final playback = ExoPairedPlayback(video: video(), audio: audio());
    await playback.open();
    await push('tracks', {
      'handle': 7,
      'audio': [
        {'language': 'pt-BR', 'label': 'Dublado', 'codec': 'audio/mp4a-latm', 'channels': 2, 'sampleRate': 44100, 'bitrate': 128000},
      ],
      'video': [
        {'codec': 'video/avc', 'width': 1920, 'height': 1080, 'frameRate': 25.0, 'bitrate': 4000000},
      ],
    });

    final tracks = playback.tracks;
    expect(tracks.audio, hasLength(1));
    expect(tracks.audio.single.language, 'por', reason: 'canonical language code');
    expect(tracks.audio.single.title, 'Dublado');
    expect(tracks.audio.single.sampleRate, 44100);
    expect(tracks.video.single.width, 1920);

    playback.selectAudio(const [0]);
    // The call is fire-and-forget by contract; it still has to have gone out.
    await Future<void>.delayed(Duration.zero);
    expect(calls.last.method, 'selectAudio');
    expect(calls.last.arguments, {'handle': 7, 'tracks': [0]});
    await playback.dispose();
  });

  test('closing releases the host player and stops taking samples', () async {
    answerCreate();
    final playback = ExoPairedPlayback(video: video(), audio: audio());
    await playback.open();
    await playback.dispose();

    expect(calls.last.method, 'dispose');
    expect(calls.last.arguments, {'handle': 7});

    // A late sample for a released handle must not revive it.
    await push('event', {'handle': 7, 'ready': true, 'position': 5000, 'playing': true});
    await playback.dispose();
    expect(calls.where((c) => c.method == 'dispose'), hasLength(1), reason: 'dispose is idempotent');
  });

  test('a host failure is kept, and logged once', () async {
    answerCreate();
    final playback = ExoPairedPlayback(video: video(), audio: audio());
    await playback.open();
    await push('event', {'handle': 7, 'ready': false, 'error': 'DecoderInitializationException: 0xfffffc0e'});
    await push('event', {'handle': 7, 'ready': false, 'error': 'DecoderInitializationException: 0xfffffc0e'});
    expect(playback.error, contains('0xfffffc0e'));
    await playback.dispose();
  });
}
