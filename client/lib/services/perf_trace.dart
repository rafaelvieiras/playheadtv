// Opt-in frame tracing, for measuring on a device where the integration suites
// cannot run (an Android TV box). Off unless the build sets
// `--dart-define=PLAYHEADTV_PERF_TRACE=true`; when off every call is a no-op.
//
// A window is a named stretch of time (opening the guide, a burst of key
// presses in it). While one is open every frame the engine reports is kept; when
// it closes the frame timings are summarized into one JSON line appended to
// `perf_trace.jsonl` in the app's support directory (read with
// `adb shell run-as <package> cat files/perf_trace.jsonl`).
//
// It only observes: no setState, nothing on the UI path but a list append.
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/scheduler.dart';
import 'package:path_provider/path_provider.dart';

import '../log/log.dart';

const perfTraceEnabled = bool.fromEnvironment('PLAYHEADTV_PERF_TRACE');

/// Frame statistics of one window, as the JSON the suites and the device trace
/// both report. Frames over 1.5x the 60 Hz budget count as jank (the DevTools
/// criterion).
Map<String, Object?> summarizeFrames(List<FrameTiming> frames) {
  double ms(Duration d) => d.inMicroseconds / 1000.0;
  return summarizeFrameMillis(
    totals: [for (final f in frames) ms(f.totalSpan)],
    builds: [for (final f in frames) ms(f.buildDuration)],
    rasters: [for (final f in frames) ms(f.rasterDuration)],
  );
}

/// The same summary from plain millisecond lists (what the unit test feeds it).
Map<String, Object?> summarizeFrameMillis({
  required List<double> totals,
  required List<double> builds,
  required List<double> rasters,
}) {
  double round(double v) => (v * 100).roundToDouble() / 100;
  double avg(List<double> v) => v.isEmpty ? 0 : v.reduce((a, b) => a + b) / v.length;
  double pct(List<double> sorted, double p) => sorted.isEmpty ? 0 : sorted[((sorted.length - 1) * p).round()];
  Map<String, double> stats(List<double> raw) {
    final sorted = [...raw]..sort();
    return {'avg': round(avg(sorted)), 'p95': round(pct(sorted, 0.95)), 'max': round(sorted.isEmpty ? 0 : sorted.last)};
  }

  return {
    'frames': totals.length,
    'jank_frames': totals.where((t) => t > 1000 / 60 * 1.5).length,
    'frame_total_ms': stats(totals),
    'build_ms': stats(builds),
    'raster_ms': stats(rasters),
  };
}

/// One open window: its own frame list and its own timings callback, so frames
/// that arrive late for a closed window never land in the next one.
class _Window {
  _Window(this.name) {
    SchedulerBinding.instance.addTimingsCallback(_onTimings);
  }

  final String name;
  final List<FrameTiming> frames = [];
  final Stopwatch wall = Stopwatch()..start();
  int events = 0;

  void _onTimings(List<FrameTiming> timings) => frames.addAll(timings);

  void stopListening() => SchedulerBinding.instance.removeTimingsCallback(_onTimings);
}

class PerfTrace {
  PerfTrace._();

  /// How long a burst of activity may be quiet before its window closes.
  static const burstIdle = Duration(milliseconds: 1500);

  /// FrameTimings reach the app a moment after the frame; a closed window keeps
  /// listening this long before it is summarized.
  static const _tail = Duration(milliseconds: 400);

  static _Window? _open;
  static Timer? _idle;
  static File? _file;

  /// Opens window [name], closing any other one first. No-op when tracing is off.
  static void begin(String name) {
    if (!perfTraceEnabled) return;
    _close();
    _open = _Window(name);
  }

  /// Closes the open window.
  static void end() {
    if (!perfTraceEnabled) return;
    _close();
  }

  /// One event of a burst (a key press): opens window [name] if none is open and
  /// closes it once [burstIdle] passes with no further event.
  static void burst(String name) {
    if (!perfTraceEnabled) return;
    if (_open != null && _open!.name != name) _close();
    final window = _open ??= _Window(name);
    window.events++;
    _idle?.cancel();
    _idle = Timer(burstIdle, _close);
  }

  static void _close() {
    _idle?.cancel();
    final window = _open;
    if (window == null) return;
    _open = null;
    final wallMs = window.wall.elapsedMilliseconds;
    Timer(_tail, () {
      window.stopListening();
      final line = jsonEncode({
        'window': window.name,
        'at': DateTime.now().toUtc().toIso8601String(),
        'wall_ms': wallMs,
        if (window.events > 0) 'events': window.events,
        ...summarizeFrames(window.frames),
      });
      unawaited(_append(line));
    });
  }

  static Future<void> _append(String line) async {
    try {
      var file = _file;
      if (file == null) {
        final dir = await getApplicationSupportDirectory();
        file = _file = File('${dir.path}/perf_trace.jsonl');
      }
      await file.writeAsString('$line\n', mode: FileMode.append);
    } catch (e) {
      Log.warn('perf', 'could not write the perf trace', error: e);
    }
  }
}
