// Fake "Jellyfin" for the integration tests: no real Jellyfin is available to
// the test harness, so this starts a plain Dart HttpServer that serves a
// single short video file (generated via ffmpeg in setUpAll, not committed)
// under any `/Videos/...` path, as `streamUrl()` in `lib/main.dart` expects.
// It really supports Range requests because the VideoPlayerController/fvp
// (libmdk) may issue partial requests during the initial probe/seek — without
// that, the player could fail or behave differently from a real Jellyfin.
//
// ffmpeg is resolved from the `FFMPEG_PATH` environment variable, falling
// back to plain `ffmpeg` on the PATH.
import 'dart:async';
import 'dart:io';

class VideoFixture {
  static String get _ffmpegPath {
    final fromEnv = Platform.environment['FFMPEG_PATH'];
    return (fromEnv != null && fromEnv.isNotEmpty) ? fromEnv : 'ffmpeg';
  }

  Directory? _tempDir;
  List<int>? _videoBytes;
  HttpServer? _server;
  StreamSubscription<HttpRequest>? _sub;

  String get baseUrl {
    final server = _server;
    if (server == null) throw StateError('VideoFixture.start() has not run yet');
    return 'http://127.0.0.1:${server.port}';
  }

  /// Generates a short clip (8s, 320x240, h264+aac, `testsrc` pattern +
  /// audio tone(s)) via ffmpeg inside a temporary directory, and starts the
  /// HttpServer that serves it. Call once in setUpAll.
  ///
  /// `audioTracks` defaults to 1 (the original, lighter fixture every
  /// existing suite relies on). Pass 2 only from a suite that genuinely
  /// needs to exercise real navigation between multiple audio options (see
  /// track_selector_test.dart/track_selector_persistence_test.dart) — the
  /// 2nd track is tagged `language=por` (the 1st is `language=eng`) so the
  /// picker's real getMediaInfo() data has something distinguishable to
  /// list. Don't default this to 2 for every suite: reproduced during tier
  /// 1's validation, several real app boots (tester.pumpWidget) of the
  /// heavier 2-track fixture within the SAME test process (i.e. several
  /// testWidgets in one file) intermittently stall the video pipeline of a
  /// later boot indefinitely — see AGENTS.md's mdk/GL-texture-under-load
  /// gotcha. Suites that don't need multiple tracks should stay on the
  /// default to keep their existing reliability.
  Future<void> start({int audioTracks = 1}) async {
    assert(audioTracks == 1 || audioTracks == 2, 'only 1 or 2 audio tracks are supported by this fixture');
    _tempDir = await Directory.systemTemp.createTemp('playheadtv_it_video_');
    final videoFile = File('${_tempDir!.path}/fixture.mp4');

    final ProcessResult result;
    try {
      result = await Process.run(_ffmpegPath, [
        '-y',
        '-f', 'lavfi',
        '-i', 'testsrc=duration=8:size=320x240:rate=15',
        '-f', 'lavfi',
        '-i', 'sine=frequency=440:duration=8',
        if (audioTracks == 2) ...['-f', 'lavfi', '-i', 'sine=frequency=880:duration=8'],
        '-map', '0:v',
        '-map', '1:a',
        if (audioTracks == 2) ...['-map', '2:a'],
        '-c:v', 'libx264',
        '-pix_fmt', 'yuv420p',
        '-c:a', 'aac',
        if (audioTracks == 2) ...['-metadata:s:a:0', 'language=eng', '-metadata:s:a:1', 'language=por'],
        '-shortest',
        videoFile.path,
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
    _videoBytes = await videoFile.readAsBytes();

    _server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    _sub = _server!.listen(_handleRequest);
  }

  void _handleRequest(HttpRequest request) {
    unawaited(_respond(request));
  }

  Future<void> _respond(HttpRequest request) async {
    final bytes = _videoBytes;
    if (bytes == null || !request.uri.path.startsWith('/Videos/')) {
      request.response.statusCode = HttpStatus.notFound;
      await request.response.close();
      return;
    }

    final total = bytes.length;
    final rangeHeader = request.headers.value(HttpHeaders.rangeHeader);
    if (rangeHeader != null) {
      final match = RegExp(r'^bytes=(\d*)-(\d*)$').firstMatch(rangeHeader.trim());
      if (match != null) {
        final startStr = match.group(1) ?? '';
        final endStr = match.group(2) ?? '';
        var start = startStr.isEmpty ? 0 : int.parse(startStr);
        var end = endStr.isEmpty ? total - 1 : int.parse(endStr);
        if (startStr.isEmpty && endStr.isNotEmpty) {
          // "bytes=-500" = last 500 bytes.
          final suffixLength = int.parse(endStr);
          start = (total - suffixLength).clamp(0, total);
          end = total - 1;
        }
        if (end >= total) end = total - 1;
        if (start > end || start >= total) {
          request.response.statusCode = HttpStatus.requestedRangeNotSatisfiable;
          request.response.headers.set(HttpHeaders.contentRangeHeader, 'bytes */$total');
          await request.response.close();
          return;
        }
        final chunk = bytes.sublist(start, end + 1);
        request.response.statusCode = HttpStatus.partialContent;
        request.response.headers.set(HttpHeaders.contentTypeHeader, 'video/mp4');
        request.response.headers.set(HttpHeaders.acceptRangesHeader, 'bytes');
        request.response.headers.set(HttpHeaders.contentRangeHeader, 'bytes $start-$end/$total');
        request.response.headers.contentLength = chunk.length;
        request.response.add(chunk);
        await request.response.close();
        return;
      }
    }

    request.response.statusCode = HttpStatus.ok;
    request.response.headers.set(HttpHeaders.contentTypeHeader, 'video/mp4');
    request.response.headers.set(HttpHeaders.acceptRangesHeader, 'bytes');
    request.response.headers.contentLength = total;
    request.response.add(bytes);
    await request.response.close();
  }

  Future<void> stop() async {
    await _sub?.cancel();
    await _server?.close(force: true);
    final dir = _tempDir;
    if (dir != null) {
      try {
        await dir.delete(recursive: true);
      } catch (_) {
        // best-effort.
      }
    }
  }
}
