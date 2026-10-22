import 'package:client/services/perf_trace.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('summarizeFrameMillis', () {
    test('an empty window reports zeros, not NaN', () {
      final s = summarizeFrameMillis(totals: [], builds: [], rasters: []);
      expect(s['frames'], 0);
      expect(s['jank_frames'], 0);
      expect((s['frame_total_ms']! as Map)['max'], 0);
    });

    test('counts only frames over 1.5x the 60 Hz budget as jank', () {
      // 25 ms is the threshold (1000/60 * 1.5): at it is not jank, over it is.
      final s = summarizeFrameMillis(totals: [10, 24.9, 25.1, 80], builds: [1, 1, 1, 1], rasters: [1, 1, 1, 1]);
      expect(s['frames'], 4);
      expect(s['jank_frames'], 2);
    });

    test('reports average, p95 and max, rounded to 2 decimals', () {
      final totals = [for (var i = 1; i <= 100; i++) i.toDouble()];
      final s = summarizeFrameMillis(totals: totals, builds: totals, rasters: totals);
      final t = s['frame_total_ms']! as Map;
      expect(t['avg'], 50.5);
      expect(t['p95'], 95.0);
      expect(t['max'], 100.0);
    });

    test('percentiles do not depend on the order the frames arrived in', () {
      final s = summarizeFrameMillis(totals: [50, 10, 30, 20, 40], builds: [], rasters: []);
      expect((s['frame_total_ms']! as Map)['max'], 50.0);
    });
  });

  test('tracing is a no-op unless the build turns it on', () {
    expect(perfTraceEnabled, isFalse);
    // Must not throw or schedule anything.
    PerfTrace.begin('x');
    PerfTrace.burst('y');
    PerfTrace.end();
  });
}
