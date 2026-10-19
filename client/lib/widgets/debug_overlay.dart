// Debug overlay (D key, see InputAction.debugOverlay): inspired by Jellyfin's
// "Playback info" panel and YouTube's "Stats for nerds" — shows real
// video/audio/buffer data (via fvp's VideoPlayerController.getMediaInfo())
// and Flutter's own real performance (FPS/jank measured, not estimated from
// outside — see services/perf_stats.dart) + process CPU/memory.
//
// Its own StatefulWidget/State with an internal ticker (a Timer, not an
// AnimationController/ValueListenableBuilder from the parent): same reason as
// _OsdOverlay in main.dart — isolating this ~2x/s rebuild from the rest of the
// tree avoids the fvp/libmdk GL texture artifact documented there.
import 'dart:async';

import 'package:flutter/material.dart';
import '../video/video_engine.dart';

import '../log/log.dart';
import '../services/perf_stats.dart';

class DebugOverlay extends StatefulWidget {
  final VideoPlayback? controller;
  final String engineName;
  final String decoderPreference;

  const DebugOverlay({
    super.key,
    required this.controller,
    required this.engineName,
    required this.decoderPreference,
  });

  @override
  State<DebugOverlay> createState() => _DebugOverlayState();
}

class _DebugOverlayState extends State<DebugOverlay> {
  Timer? _timer;
  PerfSnapshot _perf = PerfSnapshot.empty;
  double _cpuPercent = 0;
  int _rssKb = 0;

  @override
  void initState() {
    super.initState();
    PerfStats.instance.start();
    _timer = Timer.periodic(const Duration(milliseconds: 500), (_) async {
      await ProcessStats.instance.sample();
      VideoStallTracker.instance.sample(widget.controller?.state);
      if (!mounted) return;
      setState(() {
        _perf = PerfStats.instance.snapshot();
        _cpuPercent = ProcessStats.instance.cpuPercent;
        _rssKb = ProcessStats.instance.rssKb;
      });
    });
  }

  @override
  void dispose() {
    _timer?.cancel();
    PerfStats.instance.stop();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final tracks = widget.controller?.tracks ?? MediaTracks.none;
    final video = tracks.video.isNotEmpty ? tracks.video.first : null;
    final audioStreams = tracks.audio;
    final value = widget.controller?.state;

    return Align(
      alignment: Alignment.topRight,
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Container(
          key: const Key('debug-overlay'),
          width: 340,
          padding: const EdgeInsets.all(12),
          decoration: BoxDecoration(
            color: Colors.black.withValues(alpha: 0.82),
            borderRadius: BorderRadius.circular(8),
            border: Border.all(color: Colors.white24),
          ),
          child: DefaultTextStyle(
            style: const TextStyle(color: Colors.white, fontSize: 12, height: 1.3),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              mainAxisSize: MainAxisSize.min,
              children: [
                _section('Playback'),
                _row('Player', widget.engineName),
                _row('Decoder preference', widget.decoderPreference),
                _row('Protocol', 'http'),
                _row('Stream type', 'Direct (no transcoding)'),
                const SizedBox(height: 8),
                _section('Performance (Flutter, ${_perf.sampleCount} frames)'),
                _row('FPS', _perf.fps.toStringAsFixed(1)),
                _row('Jank frames', '${_perf.jankCount}'),
                _row('Avg build', '${_perf.avgBuildMs.toStringAsFixed(1)} ms'),
                _row('Avg raster', '${_perf.avgRasterMs.toStringAsFixed(1)} ms'),
                _row('Worst frame', '${_perf.worstFrameMs.toStringAsFixed(1)} ms'),
                const SizedBox(height: 8),
                _section('System (process)'),
                _row('CPU', '${_cpuPercent.toStringAsFixed(0)}%'),
                _row('Memory (RSS)', '${(_rssKb / 1024).toStringAsFixed(0)} MB'),
                if (video != null) ...[
                  const SizedBox(height: 8),
                  _section('Video'),
                  _row('Codec', '${video.codec} (profile ${video.profile}, level ${video.level})'),
                  _row('Resolution', '${video.width}x${video.height}'),
                  _row('Frame rate', '${video.frameRate.toStringAsFixed(2)} fps'),
                  _row('Declared bitrate', '${(video.bitRate / 1000).toStringAsFixed(0)} kbps'),
                ],
                if (audioStreams.isNotEmpty) ...[
                  const SizedBox(height: 8),
                  _section('Audio'),
                  for (final a in audioStreams)
                    _row(
                      '${a.codec} ${a.channels}ch @${a.sampleRate}Hz',
                      '${(a.bitRate / 1000).toStringAsFixed(0)} kbps',
                    ),
                ],
                if (value != null) ...[
                  const SizedBox(height: 8),
                  _section('Buffer'),
                  _row('Position', '${_fmt(value.position)} / ${_fmt(value.duration)}'),
                  _row('Buffered up to', _bufferedLabel(value)),
                  _row('Buffering', value.isBuffering ? 'yes' : 'no'),
                ],
                const SizedBox(height: 8),
                _section('Video freezes'),
                _row('Detected', '${VideoStallTracker.instance.stallCount}'),
                _row('Last', VideoStallTracker.instance.lastStallLabel),
                const SizedBox(height: 8),
                _section('Log'),
                Padding(
                  padding: const EdgeInsets.only(bottom: 1),
                  child: Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      const Text('Level', style: TextStyle(color: Colors.white70)),
                      Text(Log.level.title, key: const Key('debug-overlay-log-level')),
                    ],
                  ),
                ),
                // The overlay already rebuilds itself twice a second; the tail
                // rides on that, so no page above it is touched.
                Column(
                  key: const Key('debug-overlay-log-tail'),
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    for (final line in Log.tail(10))
                      Text(
                        _shortLine(line),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(color: Colors.white54, fontSize: 10, height: 1.2),
                      ),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _section(String title) => Padding(
        padding: const EdgeInsets.only(bottom: 2),
        child: Text(
          title.toUpperCase(),
          style: const TextStyle(color: Colors.amberAccent, fontWeight: FontWeight.bold, fontSize: 11),
        ),
      );

  Widget _row(String label, String value) => Padding(
        padding: const EdgeInsets.only(bottom: 1),
        child: Row(
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            Flexible(child: Text(label, style: const TextStyle(color: Colors.white70))),
            const SizedBox(width: 8),
            Text(value),
          ],
        ),
      );

  // Drops the date from a log line ("2026-10-12T10:11:12.345Z ..." becomes
  // "10:11:12 ..."): the overlay is narrow.
  String _shortLine(String line) {
    final t = line.indexOf('T');
    return t > 0 && line.length > t + 9 ? '${line.substring(t + 1, t + 9)}${line.substring(line.indexOf(' '))}' : line;
  }

  String _bufferedLabel(PlaybackState value) {
    final until = value.bufferedUntil;
    if (until == null) return '—';
    return _fmt(until);
  }

  String _fmt(Duration d) {
    final h = d.inHours;
    final m = d.inMinutes.remainder(60).toString().padLeft(2, '0');
    final s = d.inSeconds.remainder(60).toString().padLeft(2, '0');
    return h > 0 ? '$h:$m:$s' : '$m:$s';
  }
}
