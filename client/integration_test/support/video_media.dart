// The fixture clip: how it is made, and what a target that cannot make it asks
// the host for. Pure dart:io on purpose — `tool/gen_it_media.dart` runs it with
// `dart run` on the host (no Flutter engine), and `video_fixture.dart` uses the
// same code from inside the suite, so both sides produce identical media.
import 'dart:io';

/// Where the clips come from when the target cannot run ffmpeg. Empty on the
/// desktop, where they are generated in `setUpAll` as always.
const String mediaBaseUrl = String.fromEnvironment('PLAYHEADTV_IT_MEDIA_BASE_URL');

/// ffmpeg is resolved from the `FFMPEG_PATH` environment variable, falling back
/// to plain `ffmpeg` on the PATH.
String get _ffmpegPath {
  final fromEnv = Platform.environment['FFMPEG_PATH'];
  return (fromEnv != null && fromEnv.isNotEmpty) ? fromEnv : 'ffmpeg';
}

/// The name a clip of this shape is cached under, on both sides: the host writes
/// it, the device asks for it by this name.
String clipName({int audioTracks = 1, int clipSeconds = 8}) =>
    'clip-${clipSeconds}s-${audioTracks}tr.mp4';

/// A pair (docs/PLUGINS.md "Modes") needs two files, and neither carries the
/// other's track: the video file has no audio at all and the audio file has no
/// picture. That is what makes a test able to prove the two were merged - a
/// player that opened only one of them reports one kind of track and no other.
String pairVideoName({int clipSeconds = 8}) => 'pair-video-${clipSeconds}s.mp4';
String pairAudioName({int clipSeconds = 8}) => 'pair-audio-${clipSeconds}s.mp4';

/// The video half of the pair: the same `testsrc` pattern, with audio dropped.
Future<void> generatePairVideo(File out, {int clipSeconds = 8}) => _ffmpeg(out, [
      '-f', 'lavfi',
      '-i', 'testsrc=duration=$clipSeconds:size=320x240:rate=15',
      '-an',
      '-c:v', 'libx264',
      '-pix_fmt', 'yuv420p',
      out.path,
    ]);

/// The audio half of the pair: the same 440 Hz tone, with the picture dropped.
Future<void> generatePairAudio(File out, {int clipSeconds = 8}) => _ffmpeg(out, [
      '-f', 'lavfi',
      '-i', 'sine=frequency=440:duration=$clipSeconds',
      '-vn',
      '-c:a', 'aac',
      '-metadata:s:a:0', 'language=eng',
      out.path,
    ]);

/// Writes one clip (8s, 320x240, h264+aac, `testsrc` pattern + audio tone(s))
/// with ffmpeg. Separate from the fixture so the device's media is built by this
/// exact call instead of a hand-copied flag list that would drift.
Future<void> generateClip(File out, {int audioTracks = 1, int clipSeconds = 8}) {
  assert(audioTracks == 1 || audioTracks == 2, 'only 1 or 2 audio tracks are supported by this fixture');
  return _ffmpeg(out, [
    '-f', 'lavfi',
    '-i', 'testsrc=duration=$clipSeconds:size=320x240:rate=15',
    '-f', 'lavfi',
    '-i', 'sine=frequency=440:duration=$clipSeconds',
    if (audioTracks == 2) ...['-f', 'lavfi', '-i', 'sine=frequency=880:duration=$clipSeconds'],
    '-map', '0:v',
    '-map', '1:a',
    if (audioTracks == 2) ...['-map', '2:a'],
    '-c:v', 'libx264',
    '-pix_fmt', 'yuv420p',
    '-c:a', 'aac',
    if (audioTracks == 2) ...['-metadata:s:a:0', 'language=eng', '-metadata:s:a:1', 'language=por'],
    '-shortest',
    out.path,
  ]);
}

/// Runs ffmpeg with these arguments, writing [out]; the one place the binary is
/// resolved and its failures are turned into something a reader can act on.
Future<void> _ffmpeg(File out, List<String> arguments) async {
  final ProcessResult result;
  try {
    result = await Process.run(_ffmpegPath, ['-y', ...arguments]);
  } on ProcessException catch (e) {
    throw StateError(
      'Could not run ffmpeg ("$_ffmpegPath"): ${e.message}. Install ffmpeg and make sure it is '
      'on your PATH, or set the FFMPEG_PATH environment variable to the full path of the '
      'ffmpeg binary (e.g. FFMPEG_PATH=/usr/local/bin/ffmpeg).',
    );
  }
  if (result.exitCode != 0) {
    throw StateError('ffmpeg failed (exit ${result.exitCode}): ${result.stderr}');
  }
}
