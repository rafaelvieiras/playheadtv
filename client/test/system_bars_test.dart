import 'package:client/services/system_bars.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';

/// Records what the policy asked the platform for, in order.
class FakeSystemBars implements SystemBars {
  final List<String> calls = [];
  Object? failWith;

  @override
  Future<void> immersive() async {
    calls.add('immersive');
    if (failWith != null) throw failWith!;
  }

  @override
  Future<void> edgeToEdge() async {
    calls.add('edgeToEdge');
    if (failWith != null) throw failWith!;
  }
}

void main() {
  test('hides the bars on the player and gives them back on leave', () async {
    final platform = FakeSystemBars();
    final bars = SystemBarsController(platform);

    bars.player = true;
    await pumpEventQueue();
    expect(platform.calls, ['immersive']);
    expect(bars.hidden, isTrue);

    bars.player = false;
    await pumpEventQueue();
    expect(platform.calls, ['immersive', 'edgeToEdge']);
    expect(bars.hidden, isFalse);
  });

  test('an identical input does not reach the platform twice', () async {
    final platform = FakeSystemBars();
    final bars = SystemBarsController(platform);

    bars.player = true;
    bars.player = true;
    bars.player = true;
    await pumpEventQueue();
    expect(platform.calls, ['immersive'], reason: 'immersiveSticky churns the window; it is a state, not a gesture');

    bars.player = false;
    bars.player = false;
    await pumpEventQueue();
    expect(platform.calls, ['immersive', 'edgeToEdge']);
  });

  test('losing focus keeps them hidden; leaving the foreground returns them', () async {
    final platform = FakeSystemBars();
    final bars = SystemBarsController(platform);

    bars.player = true;
    bars.lifecycle(AppLifecycleState.inactive);
    await pumpEventQueue();
    expect(platform.calls, ['immersive'], reason: 'the picture is still on screen');

    bars.lifecycle(AppLifecycleState.hidden);
    await pumpEventQueue();
    expect(platform.calls, ['immersive', 'edgeToEdge']);

    bars.lifecycle(AppLifecycleState.resumed);
    await pumpEventQueue();
    expect(platform.calls, ['immersive', 'edgeToEdge', 'immersive']);
  });

  test('a player that is not foreground never hides them', () async {
    final platform = FakeSystemBars();
    final bars = SystemBarsController(platform);

    bars.lifecycle(AppLifecycleState.paused);
    bars.player = true;
    await pumpEventQueue();
    expect(platform.calls, isEmpty);

    bars.lifecycle(AppLifecycleState.resumed);
    await pumpEventQueue();
    expect(platform.calls, ['immersive']);
  });

  test('dispose returns them and stops reacting', () async {
    final platform = FakeSystemBars();
    final bars = SystemBarsController(platform);

    bars.player = true;
    await pumpEventQueue();
    bars.dispose();
    await pumpEventQueue();
    expect(platform.calls, ['immersive', 'edgeToEdge']);

    bars.player = true;
    bars.lifecycle(AppLifecycleState.hidden);
    await pumpEventQueue();
    expect(platform.calls, ['immersive', 'edgeToEdge'], reason: 'a disposed controller is out of the conversation');
  });

  test('a platform that refuses does not escape as an unhandled error', () async {
    final platform = FakeSystemBars()..failWith = StateError('no such channel');
    final bars = SystemBarsController(platform);

    bars.player = true;
    await pumpEventQueue();
    // The policy still believes what it asked for: retrying every frame would
    // be worse than a bar that stayed visible.
    expect(bars.hidden, isTrue);
    bars.player = false;
    await pumpEventQueue();
    expect(platform.calls, ['immersive', 'edgeToEdge']);
  });

  group('the real implementation', () {
    const bars = FlutterSystemBars();
    late List<MethodCall> calls;

    setUp(() {
      calls = [];
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          calls.add(call);
          return null;
        },
      );
    });

    tearDown(() {
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger.setMockMethodCallHandler(SystemChannels.platform, null);
    });

    List<Object?> modes() => calls
        .where((c) => c.method == 'SystemChrome.setEnabledSystemUIMode')
        .map((c) => c.arguments)
        .toList();

    testWidgets('touches nothing off a mobile platform', (tester) async {
      // The desktop suites run on Linux through this same code path; a platform
      // channel with no opinion about system UI would throw MissingPluginException
      // on an await nobody catches.
      // Reset inside the body: the binding verifies invariants before tearDown
      // runs, and a debug variable left changed fails the test.
      debugDefaultTargetPlatformOverride = TargetPlatform.linux;
      try {
        await bars.immersive();
        await bars.edgeToEdge();
      } finally {
        debugDefaultTargetPlatformOverride = null;
      }
      expect(modes(), isEmpty);
    });

    testWidgets('sets the Android mode on Android', (tester) async {
      debugDefaultTargetPlatformOverride = TargetPlatform.android;
      try {
        await bars.immersive();
        await bars.edgeToEdge();
      } finally {
        debugDefaultTargetPlatformOverride = null;
      }
      expect(modes(), ['SystemUiMode.immersiveSticky', 'SystemUiMode.edgeToEdge']);
    });
  });
}
