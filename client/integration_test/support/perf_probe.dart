// Resource measurements for the performance suites (perf_*_test.dart).
//
// A [PerfProbe] records, per named phase: wall time, every frame the engine
// reported (build/raster/total spans), and the process CPU and RSS. The result
// is one JSON document, printed as a `PERF_JSON {...}` line (the GUI runner keeps
// the suite log) and written to `$PERF_OUT` when that is set.
//
// Absolute numbers depend on the machine (a software-GL container is not a TV
// box); the point is to compare the same suite on the same setup before and
// after a change, so every report carries `env` to make that visible.
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:client/services/perf_stats.dart';
import 'package:flutter/scheduler.dart';

class PhaseResult {
  PhaseResult({
    required this.name,
    required this.wallMs,
    required this.frames,
    required this.cpuPercent,
    required this.rssMb,
  });

  final String name;
  final int wallMs;
  final List<FrameTiming> frames;
  final double cpuPercent;
  final double rssMb;

  static double _ms(Duration d) => d.inMicroseconds / 1000.0;

  static double _percentile(List<double> sorted, double p) {
    if (sorted.isEmpty) return 0;
    final index = ((sorted.length - 1) * p).round();
    return sorted[index];
  }

  Map<String, Object?> toJson() {
    final totals = frames.map((f) => _ms(f.totalSpan)).toList()..sort();
    final builds = frames.map((f) => _ms(f.buildDuration)).toList()..sort();
    final rasters = frames.map((f) => _ms(f.rasterDuration)).toList()..sort();
    double avg(List<double> v) => v.isEmpty ? 0 : v.reduce((a, b) => a + b) / v.length;
    double round(double v) => (v * 100).roundToDouble() / 100;
    return {
      'wall_ms': wallMs,
      'frames': frames.length,
      // Frames over 1.5x the 60 Hz budget, the DevTools "jank" criterion.
      'jank_frames': totals.where((t) => t > 1000 / 60 * 1.5).length,
      'frame_total_ms': {'avg': round(avg(totals)), 'p95': round(_percentile(totals, 0.95)), 'max': round(totals.isEmpty ? 0 : totals.last)},
      'build_ms': {'avg': round(avg(builds)), 'p95': round(_percentile(builds, 0.95)), 'max': round(builds.isEmpty ? 0 : builds.last)},
      'raster_ms': {'avg': round(avg(rasters)), 'p95': round(_percentile(rasters, 0.95)), 'max': round(rasters.isEmpty ? 0 : rasters.last)},
      'cpu_percent': round(cpuPercent),
      'rss_mb': round(rssMb),
    };
  }
}

class PerfProbe {
  PerfProbe(this.suite);

  final String suite;
  final List<PhaseResult> phases = [];
  final Map<String, Object?> extra = {};

  List<FrameTiming>? _frames;
  Stopwatch? _wall;
  String? _phase;

  void _onTimings(List<FrameTiming> timings) => _frames?.addAll(timings);

  /// Starts a phase; ends the previous one if the caller forgot.
  Future<void> begin(String name) async {
    if (_phase != null) await end();
    _frames = [];
    _phase = name;
    SchedulerBinding.instance.addTimingsCallback(_onTimings);
    // Prime the CPU sampler: its first reading has no delta to report.
    await ProcessStats.instance.sample();
    _wall = Stopwatch()..start();
  }

  /// Ends the current phase and records it. FrameTimings are delivered a moment
  /// after the frame, so it waits briefly for the tail to arrive.
  Future<PhaseResult> end() async {
    final name = _phase!;
    final wallMs = _wall!.elapsedMilliseconds;
    await ProcessStats.instance.sample();
    final cpu = ProcessStats.instance.cpuPercent;
    final rss = ProcessStats.instance.rssKb / 1024.0;
    await Future<void>.delayed(const Duration(milliseconds: 300));
    SchedulerBinding.instance.removeTimingsCallback(_onTimings);
    final result = PhaseResult(name: name, wallMs: wallMs, frames: _frames ?? [], cpuPercent: cpu, rssMb: rss);
    phases.add(result);
    _frames = null;
    _phase = null;
    _wall = null;
    return result;
  }

  Map<String, Object?> toJson() => {
        'suite': suite,
        'env': {
          'os': Platform.operatingSystem,
          'cpus': Platform.numberOfProcessors,
          'display': Platform.environment['DISPLAY'] != null,
          'container': Platform.environment['GUI_RUN'] != null || File('/.dockerenv').existsSync(),
          'mode': const bool.fromEnvironment('dart.vm.product')
              ? 'release'
              : const bool.fromEnvironment('dart.vm.profile')
                  ? 'profile'
                  : 'debug',
        },
        'phases': {for (final p in phases) p.name: p.toJson()},
        ...extra,
      };

  /// Prints the report and, when `PERF_OUT` names a file, writes it there.
  void report() {
    final json = jsonEncode(toJson());
    // ignore: avoid_print
    print('PERF_JSON $json');
    final out = Platform.environment['PERF_OUT'];
    if (out != null && out.isNotEmpty) {
      File(out).writeAsStringSync('${const JsonEncoder.withIndent('  ').convert(toJson())}\n');
    }
  }
}
