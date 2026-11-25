import 'package:flutter_test/flutter_test.dart';
import 'package:client/util/open_retry.dart';

void main() {
  Future<void> noSleep(Duration _) async {}

  test('a failure that cannot change is not retried', () async {
    // What a decoder that does not exist answers: retrying only builds and
    // tears down another player for the same refusal (measured on a TV box: 49
    // players in fifteen minutes, then the process was killed for heap).
    var calls = 0;
    await expectLater(
      retryOpen<int>(
        attempt: (_) async {
          calls++;
          throw const FormatException('no decoder for mpeg4');
        },
        shouldRetry: (e) => e is! FormatException,
        sleep: noSleep,
      ),
      throwsA(isA<FormatException>()),
    );
    expect(calls, 1);
  });

  test('a retryable failure still retries', () async {
    var calls = 0;
    final r = await retryOpen<int>(
      maxAttempts: 3,
      attempt: (n) async {
        calls++;
        if (n < 3) throw StateError('slow');
        return n;
      },
      shouldRetry: (e) => e is StateError,
      sleep: noSleep,
    );
    expect(r, 3);
    expect(calls, 3);
  });

  test('returns the first result without retrying', () async {
    var calls = 0;
    final r = await retryOpen<int>(attempt: (_) async => ++calls, sleep: noSleep);
    expect(r, 1);
    expect(calls, 1);
  });

  test('recovers when a later attempt succeeds', () async {
    var calls = 0;
    final failed = <int>[];
    final r = await retryOpen<String>(
      maxAttempts: 3,
      sleep: noSleep,
      onRetry: (n, _) => failed.add(n),
      attempt: (n) async {
        calls++;
        if (n < 3) throw StateError('timeout $n');
        return 'ok';
      },
    );
    expect(r, 'ok');
    expect(calls, 3);
    expect(failed, [1, 2]);
  });

  test('rethrows the last error once every attempt failed', () async {
    var calls = 0;
    await expectLater(
      retryOpen<int>(
        maxAttempts: 2,
        sleep: noSleep,
        attempt: (n) async {
          calls++;
          throw StateError('failure $n');
        },
      ),
      throwsA(predicate((e) => e is StateError && e.message == 'failure 2')),
    );
    expect(calls, 2);
  });

  test('waits the configured delay between attempts only', () async {
    final waits = <Duration>[];
    await retryOpen<int>(
      maxAttempts: 3,
      delay: const Duration(milliseconds: 250),
      sleep: (d) async => waits.add(d),
      attempt: (n) async => n < 3 ? throw StateError('x') : n,
    );
    expect(waits, [const Duration(milliseconds: 250), const Duration(milliseconds: 250)]);
  });
}
