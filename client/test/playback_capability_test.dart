// The "can this device play it?" seam, and the classification of an open that
// failed because of the media rather than the source.
//
// Both exist because of one measured failure: a 320x240 MPEG-4 Part 2 file in an
// AVI cannot be decoded on some set-top boxes, the player spent minutes building
// players for it, and the retries (plus the picture watchdog's own) ran the
// process out of heap. The answer of "unsupported" must mean no player is built
// at all, and an engine error that names a decoder must be treated as the same
// thing.
import 'package:client/video/playback_capability.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  const channel = MethodChannel('playheadtv/codec');
  final messenger =
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;

  tearDown(() {
    messenger.setMockMethodCallHandler(channel, null);
  });

  void answer(String? value, {bool throws = false, List<String>? calls}) {
    messenger.setMockMethodCallHandler(channel, (call) async {
      expect(call.method, 'canDecode');
      calls?.add('${call.arguments['codec']}|${call.arguments['width']}x${call.arguments['height']}');
      if (throws) throw PlatformException(code: 'nope');
      return value;
    });
  }

  test('an unsupported answer is reported as unsupported', () async {
    answer('unsupported');
    final capability = PlatformPlaybackCapability();
    expect(await capability.check(codec: 'mpeg4', width: 320, height: 240), CodecSupport.unsupported);
  });

  test('a supported answer is reported as supported', () async {
    answer('supported');
    final capability = PlatformPlaybackCapability();
    expect(await capability.check(codec: 'hevc', width: 1920, height: 1080), CodecSupport.supported);
  });

  test('no codec metadata never blocks playback', () async {
    final capability = PlatformPlaybackCapability();
    expect(await capability.check(), CodecSupport.unknown);
    expect(await capability.check(codec: '  '), CodecSupport.unknown);
  });

  test('the answer is asked once per codec and size', () async {
    final calls = <String>[];
    answer('supported', calls: calls);
    final capability = PlatformPlaybackCapability();
    await capability.check(codec: 'h264', width: 1920, height: 1080);
    await capability.check(codec: 'h264', width: 1920, height: 1080);
    await capability.check(codec: 'h264', width: 640, height: 480);
    expect(calls, <String>['h264|1920x1080', 'h264|640x480']);
  });

  test('a platform that cannot answer degrades to unknown', () async {
    // No handler installed at all: what a desktop build looks like.
    final capability = PlatformPlaybackCapability();
    expect(await capability.check(codec: 'mpeg4', width: 320, height: 240), CodecSupport.unknown);
  });

  test('a failing platform call degrades to unknown', () async {
    answer(null, throws: true);
    final capability = PlatformPlaybackCapability();
    expect(await capability.check(codec: 'mpeg4', width: 320, height: 240), CodecSupport.unknown);
  });

  group('isUnsupportedMediaError', () {
    test('a decoder that Media3 could not initialize is the media, not the source', () {
      const e = 'PlatformException(VideoError, Video player had error kn: '
          'MediaCodecVideoRenderer error, index=0, format=Format(0, null, null, video/mp4v-es, null, -1, null, '
          '[320, 240, -1.0, null], [-1, -1]), format_supported=YES)';
      expect(isUnsupportedMediaError(e), isTrue);
      expect(isUnsupportedMediaError('Decoder init failed: OMX.amlogic.mpeg4.decoder.awesome'), isTrue);
      expect(isUnsupportedMediaError('MediaCodecAudioRenderer error, index=1'), isTrue);
    });

    test('a source that is slow, gone or refused is not', () {
      for (final e in <String>[
        'SocketException: Connection refused',
        'HttpDataSourceException: 404',
        'TimeoutException after 0:00:10.000000',
        'the plugin answered 503',
      ]) {
        expect(isUnsupportedMediaError(e), isFalse, reason: e);
      }
    });
  });
}
