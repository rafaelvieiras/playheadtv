/// Runs [attempt] up to [maxAttempts] times, waiting [delay] between tries, and
/// rethrows the last error when every try failed.
///
/// Opening a stream can fail transiently when the media server is slow to read
/// its storage (a cold network-backed library answers the first read late and
/// the next one fast), so a single retry recovers most of those without the
/// viewer seeing an error. Pure Dart: the caller builds a fresh resource in
/// every [attempt] and disposes it when that attempt throws.
Future<T> retryOpen<T>({
  required Future<T> Function(int attempt) attempt,
  int maxAttempts = 2,
  Duration delay = const Duration(milliseconds: 600),
  void Function(int failedAttempt, Object error)? onRetry,
  Future<void> Function(Duration) sleep = _sleep,
}) async {
  assert(maxAttempts >= 1);
  for (var n = 1;; n++) {
    try {
      return await attempt(n);
    } catch (e) {
      if (n >= maxAttempts) rethrow;
      onRetry?.call(n, e);
      await sleep(delay);
    }
  }
}

Future<void> _sleep(Duration d) => Future<void>.delayed(d);
