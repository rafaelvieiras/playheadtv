import 'dart:async';

import 'package:client/video/exo_teardown.dart';
import 'package:flutter_test/flutter_test.dart';

// The Android engine releases its native players through one FIFO (see
// `lib/video/exo_teardown.dart`): a MediaCodec is scarce, and two releases
// beside an allocation is what makes a tune-in fail. These tests pin the
// contract the rest of the engine relies on.
void main() {
  test('a teardown waits for the one before it', () async {
    final order = <String>[];
    final gate = Completer<void>();

    final first = serializeExoTeardown(() async {
      order.add('first in');
      await gate.future;
      order.add('first out');
    });
    final second = serializeExoTeardown(() async => order.add('second'));

    await pumpEventQueue();
    expect(order, ['first in'], reason: 'the second teardown started beside the first');

    gate.complete();
    await Future.wait([first, second]);
    expect(order, ['first in', 'first out', 'second']);
  });

  test('a failed teardown reaches its caller and does not stall the FIFO', () async {
    final first = serializeExoTeardown(() async => throw StateError('decoder already gone'));
    final second = serializeExoTeardown(() async {});

    await expectLater(first, throwsStateError);
    await second;
    expect(exoTeardownTail, completes);
  });

  test('a teardown that never answers lets the FIFO move on', () async {
    // A plugin call that returns nothing must not keep every later player
    // alive: the timeout is what bounds it.
    final stuck = serializeExoTeardown(() => Completer<void>().future, timeout: const Duration(milliseconds: 30));
    final after = serializeExoTeardown(() async {});

    await stuck;
    await after.timeout(const Duration(seconds: 5));
  });
}
