// Writes the video fixture's clips for a target that cannot run ffmpeg.
//
// A suite runs the app on the device, so `VideoFixture` would have to shell out
// to ffmpeg there, where no ffmpeg exists. The lab generates the clips here,
// with the same ffmpeg call a desktop run uses, and serves the directory over an
// `adb reverse` tunnel; the device reads them with
// `--dart-define=PLAYHEADTV_IT_MEDIA_BASE_URL`.
//
//   cd client && dart run tool/gen_it_media.dart <out-dir> [--force]
//
// The shapes are read from the suites themselves (`video.start(...)`), so a new
// clip length shows up here without editing this file. Existing clips are kept
// unless --force is passed: encoding is the slow part.
import 'dart:io';

import '../integration_test/support/video_media.dart';

void main(List<String> args) async {
  final force = args.contains('--force');
  final dirs = args.where((a) => !a.startsWith('--')).toList();
  if (dirs.isEmpty) {
    stderr.writeln('usage: dart run tool/gen_it_media.dart <out-dir> [--force]');
    exit(2);
  }
  final clientDir = File.fromUri(Platform.script).parent.parent;
  final out = Directory(dirs.first)..createSync(recursive: true);

  final audio = RegExp(r'audioTracks:\s*(\d+)');
  final seconds = RegExp(r'clipSeconds:\s*(\d+)');
  final call = RegExp(r'\.start\(([^)]*)\)');
  final variants = <String, List<int>>{
    // The shape a suite gets when it calls start() with no arguments.
    clipName(): [1, 8],
  };
  for (final f in Directory('${clientDir.path}/integration_test').listSync().whereType<File>()) {
    if (!f.path.endsWith('.dart')) continue;
    for (final m in call.allMatches(f.readAsStringSync())) {
      final a = m.group(1)!;
      final tracks = int.tryParse(audio.firstMatch(a)?.group(1) ?? '') ?? 1;
      final secs = int.tryParse(seconds.firstMatch(a)?.group(1) ?? '') ?? 8;
      variants[clipName(audioTracks: tracks, clipSeconds: secs)] = [tracks, secs];
    }
  }

  final names = variants.keys.toList()..sort();
  for (final name in names) {
    final file = File('${out.path}/$name');
    final shape = variants[name]!;
    if (!force && file.existsSync() && file.lengthSync() > 0) {
      stdout.writeln('keep   $name');
      continue;
    }
    final started = DateTime.now();
    await generateClip(file, audioTracks: shape[0], clipSeconds: shape[1]);
    final kb = (file.lengthSync() / 1024).round();
    stdout.writeln('write  $name  ${kb}kB  ${DateTime.now().difference(started).inMilliseconds}ms');
  }
}
