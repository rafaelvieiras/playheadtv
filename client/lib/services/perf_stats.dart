// Performance metrics collection for the debug overlay (see
// widgets/debug_overlay.dart). Two independent collectors:
//
// - PerfStats: real FPS/jank, via SchedulerBinding.addTimingsCallback (the
//   same mechanism the DevTools Performance View uses) — measures what the
//   compositor actually delivered to the screen, not an external estimate
//   (external tools like top/nvidia-smi don't catch sub-second hiccups).
// - ProcessStats: process CPU%/RSS, reading /proc/self/stat and
//   /proc/self/status directly (Linux-only, no external plugin needed).
import 'dart:async';
import 'dart:collection';
import 'dart:io';

import 'package:flutter/scheduler.dart';
import 'package:video_player/video_player.dart';

class PerfSnapshot {
  final double fps;
  final int jankCount;
  final double avgBuildMs;
  final double avgRasterMs;
  final double worstFrameMs;
  final int sampleCount;

  const PerfSnapshot({
    required this.fps,
    required this.jankCount,
    required this.avgBuildMs,
    required this.avgRasterMs,
    required this.worstFrameMs,
    required this.sampleCount,
  });

  static const empty = PerfSnapshot(
    fps: 0,
    jankCount: 0,
    avgBuildMs: 0,
    avgRasterMs: 0,
    worstFrameMs: 0,
    sampleCount: 0,
  );
}

class PerfStats {
  PerfStats._();
  static final PerfStats instance = PerfStats._();

  // ~4s of history at 60fps — short enough to reflect recent fluctuation,
  // long enough not to turn into frame-by-frame noise.
  static const _windowSize = 240;
  static const _budgetMs = 1000 / 60;

  final Queue<FrameTiming> _samples = Queue();
  bool _listening = false;

  void start() {
    if (_listening) return;
    _listening = true;
    SchedulerBinding.instance.addTimingsCallback(_onTimings);
  }

  void stop() {
    if (!_listening) return;
    _listening = false;
    SchedulerBinding.instance.removeTimingsCallback(_onTimings);
    _samples.clear();
  }

  void _onTimings(List<FrameTiming> timings) {
    for (final t in timings) {
      _samples.addLast(t);
      while (_samples.length > _windowSize) {
        _samples.removeFirst();
      }
    }
  }

  PerfSnapshot snapshot() {
    if (_samples.isEmpty) return PerfSnapshot.empty;
    var totalMs = 0.0;
    var buildMs = 0.0;
    var rasterMs = 0.0;
    var worst = 0.0;
    var jank = 0;
    for (final t in _samples) {
      final totalSpanMs = t.totalSpan.inMicroseconds / 1000.0;
      totalMs += totalSpanMs;
      buildMs += t.buildDuration.inMicroseconds / 1000.0;
      rasterMs += t.rasterDuration.inMicroseconds / 1000.0;
      if (totalSpanMs > worst) worst = totalSpanMs;
      // >1.5x the frame budget (16.6ms at 60Hz) counts as visible jank — same
      // criterion used by the DevTools Performance View.
      if (totalSpanMs > _budgetMs * 1.5) jank++;
    }
    final n = _samples.length;
    final avgTotalMs = totalMs / n;
    return PerfSnapshot(
      fps: avgTotalMs > 0 ? 1000 / avgTotalMs : 0,
      jankCount: jank,
      avgBuildMs: buildMs / n,
      avgRasterMs: rasterMs / n,
      worstFrameMs: worst,
      sampleCount: n,
    );
  }
}

class ProcessStats {
  ProcessStats._();
  static final ProcessStats instance = ProcessStats._();

  // sysconf(_SC_CLK_TCK) is practically always 100 on Linux — hardcoded here
  // to avoid needing FFI just for that.
  static const _clockTicksPerSec = 100;

  int? _lastUtime;
  int? _lastStime;
  DateTime? _lastSampleAt;

  double cpuPercent = 0;
  int rssKb = 0;

  Future<void> sample() async {
    try {
      final statRaw = await File('/proc/self/stat').readAsString();
      // field 2 (comm) comes in parentheses and may contain spaces — cut
      // everything up to the last ')' so the following fields don't get
      // misaligned.
      final afterComm = statRaw.substring(statRaw.lastIndexOf(')') + 2);
      final fields = afterComm.split(' ');
      // fields[0] = field 3 (state) of /proc/pid/stat; utime is field 14,
      // stime is 15 => indexes 11 and 12 here.
      final utime = int.parse(fields[11]);
      final stime = int.parse(fields[12]);
      final now = DateTime.now();
      if (_lastUtime != null && _lastStime != null && _lastSampleAt != null) {
        final deltaTicks = (utime + stime) - (_lastUtime! + _lastStime!);
        final deltaSec = now.difference(_lastSampleAt!).inMicroseconds / 1e6;
        if (deltaSec > 0) {
          cpuPercent = (deltaTicks / _clockTicksPerSec) / deltaSec * 100;
        }
      }
      _lastUtime = utime;
      _lastStime = stime;
      _lastSampleAt = now;

      final statusRaw = await File('/proc/self/status').readAsString();
      final match = RegExp(r'VmRSS:\s+(\d+) kB').firstMatch(statusRaw);
      if (match != null) rssKb = int.parse(match.group(1)!);
    } catch (_) {
      // best-effort — must never bring down the debug overlay.
    }
  }
}

// Detects REAL video freezes (like Jellyfin's "dropped frames"): compares the
// advance of `position` against the real time elapsed between two samples.
// This catches a kind of stall that PerfStats/SchedulerBinding does NOT catch
// — Flutter's compositor can keep drawing the screen normally (high FPS, no
// jank) while the video TEXTURE stays stuck on the same frame because the
// decoder/demuxer didn't deliver a new frame in time (e.g. a network read
// stall). A singleton (not tied to the overlay's lifecycle) so the count is
// kept across the overlay being opened/closed.
class VideoStallTracker {
  VideoStallTracker._();
  static final VideoStallTracker instance = VideoStallTracker._();

  Duration? _lastPosition;
  DateTime? _lastSampleAt;

  int stallCount = 0;
  DateTime? lastStallAt;

  void sample(VideoPlayerValue? value) {
    final now = DateTime.now();
    if (value == null || !value.isInitialized) {
      _lastPosition = null;
      _lastSampleAt = now;
      return;
    }
    final position = value.position;
    if (_lastPosition != null && _lastSampleAt != null) {
      final wallElapsed = now.difference(_lastSampleAt!);
      final videoElapsed = position - _lastPosition!;
      // Only counts as a stall if we believed it should be playing (neither
      // paused nor in acknowledged buffering) and the video advanced much less
      // than real time — i.e., the same frame stayed "frozen" on screen.
      if (value.isPlaying && !value.isBuffering && wallElapsed.inMilliseconds > 300) {
        final ratio = videoElapsed.inMilliseconds / wallElapsed.inMilliseconds;
        if (ratio < 0.5) {
          stallCount++;
          lastStallAt = now;
        }
      }
    }
    _lastPosition = position;
    _lastSampleAt = now;
  }

  String get lastStallLabel {
    final at = lastStallAt;
    if (at == null) return '—';
    return '${DateTime.now().difference(at).inSeconds}s ago';
  }
}
