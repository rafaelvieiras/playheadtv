import 'package:client/preload_lead.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('configured lead', () {
    test('a missing or unusable value is the default', () {
      expect(sanitizePreloadLeadSeconds(null), defaultPreloadLeadSeconds);
      expect(sanitizePreloadLeadSeconds('20'), defaultPreloadLeadSeconds);
      expect(sanitizePreloadLeadSeconds(0), defaultPreloadLeadSeconds);
      expect(sanitizePreloadLeadSeconds(-3), defaultPreloadLeadSeconds);
    });

    test('an out-of-range value is clamped, an in-range one is kept', () {
      expect(sanitizePreloadLeadSeconds(1), minPreloadLeadSeconds);
      expect(sanitizePreloadLeadSeconds(600), maxPreloadLeadSeconds);
      expect(sanitizePreloadLeadSeconds(25), 25);
    });

    test('the default is 20 seconds and a Settings choice', () {
      expect(defaultPreloadLeadSeconds, 20);
      expect(preloadLeadChoicesSeconds, contains(defaultPreloadLeadSeconds));
    });

    test('Select steps through the choices and wraps', () {
      expect(nextPreloadLeadChoice(20), 30);
      expect(nextPreloadLeadChoice(25), 30);
      expect(nextPreloadLeadChoice(maxPreloadLeadSeconds), preloadLeadChoicesSeconds.first);
      var v = preloadLeadChoicesSeconds.first;
      final seen = <int>[];
      for (var i = 0; i < preloadLeadChoicesSeconds.length; i++) {
        seen.add(v);
        v = nextPreloadLeadChoice(v);
      }
      expect(seen, preloadLeadChoicesSeconds);
      expect(v, preloadLeadChoicesSeconds.first);
    });
  });

  group('PreloadLeadTracker', () {
    const configured = Duration(seconds: 20);

    test('with no measurement the lead is the configured one', () {
      expect(PreloadLeadTracker().effective(configured), configured);
    });

    test('fast preloads never go below the configured lead', () {
      final t = PreloadLeadTracker()..record(const Duration(milliseconds: 300));
      expect(t.effective(configured), configured);
    });

    test('a slow preload raises the lead above the configured one', () {
      final t = PreloadLeadTracker()..record(const Duration(seconds: 15));
      expect(t.effective(configured), const Duration(seconds: 32));
    });

    test('growth is bounded', () {
      final t = PreloadLeadTracker()..record(const Duration(minutes: 5));
      expect(t.effective(configured), maxAdaptiveLead);
    });

    test('a configured lead above the ceiling is kept', () {
      final t = PreloadLeadTracker()..record(const Duration(minutes: 5));
      expect(t.effective(const Duration(seconds: 120)), const Duration(seconds: 120));
    });

    test('it decays back once the source is fast again', () {
      final t = PreloadLeadTracker()..record(const Duration(seconds: 30));
      expect(t.effective(configured) > configured, isTrue);
      for (var i = 0; i < preloadSampleWindow; i++) {
        t.record(const Duration(milliseconds: 200));
      }
      expect(t.effective(configured), configured);
      expect(t.samples, hasLength(preloadSampleWindow));
    });

    test('the slowest recent sample drives it', () {
      final t = PreloadLeadTracker()
        ..record(const Duration(seconds: 20))
        ..record(const Duration(seconds: 1));
      expect(t.effective(configured), const Duration(seconds: 42));
    });
  });

  group('preloadDelay', () {
    test('a long item preloads lead before its end', () {
      expect(preloadDelay(remaining: const Duration(seconds: 60), lead: const Duration(seconds: 20)), const Duration(seconds: 40));
    });

    test('an item shorter than the lead preloads right after it starts', () {
      expect(preloadDelay(remaining: const Duration(seconds: 8), lead: const Duration(seconds: 20)), Duration.zero);
      expect(preloadDelay(remaining: const Duration(seconds: 20), lead: const Duration(seconds: 20)), Duration.zero);
    });
  });

  group('retry back-off', () {
    const base = Duration(seconds: 1);

    test('doubles and is capped', () {
      expect([for (var i = 1; i <= 7; i++) preloadRetryDelay(i, base).inSeconds], [1, 2, 4, 8, 16, 16, 16]);
    });

    test('scales with the base and survives absurd counts', () {
      expect(preloadRetryDelay(3, const Duration(milliseconds: 100)), const Duration(milliseconds: 400));
      expect(preloadRetryDelay(500, base), base * preloadRetryCapFactor);
      expect(preloadRetryDelay(0, base), base);
    });

    test('an error is reported only after several failures', () {
      expect(preloadAttemptsBeforeError, greaterThan(1));
    });
  });

  group('preloadRetryDelayFor', () {
    test('a cheap source keeps the plain growth', () {
      expect(preloadRetryDelayFor(failures: 1, base: const Duration(seconds: 1), costlySource: false),
          const Duration(seconds: 1));
      expect(preloadRetryDelayFor(failures: 3, base: const Duration(seconds: 1), costlySource: false),
          const Duration(seconds: 4));
    });

    test('an expensive source never retries sooner than the floor', () {
      for (var failures = 1; failures <= 6; failures++) {
        final delay = preloadRetryDelayFor(failures: failures, base: const Duration(seconds: 1), costlySource: true);
        expect(delay, greaterThanOrEqualTo(costlySourceRetryFloor), reason: 'failure $failures');
      }
    });

    test('the floor does not shorten a longer backoff', () {
      expect(preloadRetryDelayFor(failures: 30, base: const Duration(seconds: 1), costlySource: true),
          preloadRetryDelay(30, const Duration(seconds: 1)));
    });
  });

}

