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

/// Writes one clip (8s, 320x240, h264+aac, `testsrc` pattern + audio tone(s))
/// with ffmpeg. Separate from the fixture so the device's media is built by this
/// exact call instead of a hand-copied flag list that would drift.
Future<void> generateClip(File out, {int audioTracks = 1, int clipSeconds = 8}) async {
  assert(audioTracks == 1 || audioTracks == 2, 'only 1 or 2 audio tracks are supported by this fixture');
  final ProcessResult result;
  try {
    result = await Process.run(_ffmpegPath, [
      '-y',
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
