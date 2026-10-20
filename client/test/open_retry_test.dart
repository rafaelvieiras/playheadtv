import 'package:flutter_test/flutter_test.dart';
import 'package:client/util/open_retry.dart';

void main() {
  Future<void> noSleep(Duration _) async {}

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
