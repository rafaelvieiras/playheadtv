import 'dart:async';

import 'package:client/services/app_exit.dart';
import 'package:flutter_test/flutter_test.dart';

class _FakeExit implements AppExit {
  _FakeExit(this.log);

  final List<String> log;

  @override
  Future<void> terminate() async => log.add('terminate');
}

void main() {
  test('runs the teardown steps in order, then terminates', () async {
    final log = <String>[];
    final c = AppExitController(
      platform: _FakeExit(log),
      steps: [
        () async => log.add('controllers disposed'),
        () async => log.add('wake lock released'),
        () async => log.add('background work stopped'),
      ],
    );
    await c.exit();
    expect(log, ['controllers disposed', 'wake lock released', 'background work stopped', 'terminate']);
  });

  test('waits for an asynchronous step before the next one and before terminating', () async {
    final log = <String>[];
    final c = AppExitController(
      platform: _FakeExit(log),
      steps: [
        () async {
          await Future<void>.delayed(const Duration(milliseconds: 20));
          log.add('slow dispose done');
        },
        () async => log.add('wake lock released'),
      ],
    );
    await c.exit();
    expect(log, ['slow dispose done', 'wake lock released', 'terminate']);
  });

  test('a failing step does not keep the app from closing or the later steps from running', () async {
    final log = <String>[];
    final c = AppExitController(
      platform: _FakeExit(log),
      steps: [
        () => Future<void>.error('boom'),
        () async => log.add('wake lock released'),
      ],
    );
    await c.exit();
    expect(log, ['wake lock released', 'terminate']);
  });

  test('a hanging step is abandoned after its timeout', () async {
    final log = <String>[];
    final c = AppExitController(
      platform: _FakeExit(log),
      stepTimeout: const Duration(milliseconds: 30),
      steps: [
        () => Completer<void>().future,
        () async => log.add('wake lock released'),
      ],
    );
    await c.exit();
    expect(log, ['wake lock released', 'terminate']);
  });

  test('runs once: a second request while exiting is ignored', () async {
    final log = <String>[];
    final c = AppExitController(platform: _FakeExit(log), steps: [() async => log.add('step')]);
    await Future.wait([c.exit(), c.exit()]);
    await c.exit();
    expect(log, ['step', 'terminate']);
  });
}
