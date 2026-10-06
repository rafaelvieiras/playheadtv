import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:client/services/screen_awake.dart';

class _Fake implements ScreenAwake {
  final calls = <String>[];

  @override
  Future<void> enable() async => calls.add('enable');

  @override
  Future<void> disable() async => calls.add('disable');
}

class _Failing implements ScreenAwake {
  @override
  Future<void> enable() => Future.error('boom');

  @override
  Future<void> disable() => Future.error('boom');
}

void main() {
  test('enables once when playback starts, never double-enables', () {
    final fake = _Fake();
    final c = ScreenAwakeController(fake);
    c.playing = true;
    c.playing = true;
    c.lifecycle(AppLifecycleState.resumed);
    expect(fake.calls, ['enable']);
    expect(c.held, isTrue);
  });

  test('does nothing before playback starts', () {
    final fake = _Fake();
    final c = ScreenAwakeController(fake);
    c.lifecycle(AppLifecycleState.resumed);
    expect(fake.calls, isEmpty);
  });

  test('releases when the app goes to the background and re-takes on resume', () {
    final fake = _Fake();
    final c = ScreenAwakeController(fake);
    c.playing = true;
    c.lifecycle(AppLifecycleState.inactive); // focus lost only: keeps the lock
    expect(fake.calls, ['enable']);
    c.lifecycle(AppLifecycleState.hidden);
    c.lifecycle(AppLifecycleState.paused);
    c.lifecycle(AppLifecycleState.resumed);
    expect(fake.calls, ['enable', 'disable', 'enable']);
  });

  test('background before playback starts does not hold; resume then play does', () {
    final fake = _Fake();
    final c = ScreenAwakeController(fake);
    c.lifecycle(AppLifecycleState.paused);
    c.playing = true;
    expect(fake.calls, isEmpty);
    c.lifecycle(AppLifecycleState.resumed);
    expect(fake.calls, ['enable']);
  });

  test('dispose releases once and ignores later input', () {
    final fake = _Fake();
    final c = ScreenAwakeController(fake);
    c.playing = true;
    c.dispose();
    c.playing = true;
    c.lifecycle(AppLifecycleState.resumed);
    expect(fake.calls, ['enable', 'disable']);
  });

  test('dispose without ever playing never touches the platform', () {
    final fake = _Fake();
    ScreenAwakeController(fake).dispose();
    expect(fake.calls, isEmpty);
  });

  test('a failing platform call does not throw', () async {
    final c = ScreenAwakeController(_Failing());
    c.playing = true;
    c.dispose();
    await Future<void>.delayed(Duration.zero);
  });
}
