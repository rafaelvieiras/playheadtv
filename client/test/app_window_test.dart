import 'package:client/config/config_file.dart';
import 'package:client/services/app_window.dart';
import 'package:client/services/settings_service.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

class _FakeWindow implements AppWindow {
  _FakeWindow({this.supported = true});

  final bool supported;
  final calls = <bool>[];

  @override
  bool get supportsFullScreen => supported;

  @override
  Future<void> setFullScreen(bool on) async => calls.add(on);
}

class _FailingWindow implements AppWindow {
  int attempts = 0;

  @override
  bool get supportsFullScreen => true;

  @override
  Future<void> setFullScreen(bool on) {
    attempts++;
    return Future.error('boom');
  }
}

void main() {
  group('FullScreenController', () {
    test('applies a state once, never repeats the same request', () async {
      final window = _FakeWindow();
      final c = FullScreenController(window);
      await c.apply(true);
      await c.apply(true);
      await c.apply(false);
      await c.apply(false);
      await c.apply(true);
      expect(window.calls, [true, false, true]);
      expect(c.applied, isTrue);
    });

    test('does nothing where the window is not resizable (Android)', () async {
      final window = _FakeWindow(supported: false);
      final c = FullScreenController(window);
      expect(c.supported, isFalse);
      await c.apply(true);
      expect(window.calls, isEmpty);
      expect(c.applied, isNull);
    });

    test('a failing platform never throws and is retried on the next request', () async {
      final window = _FailingWindow();
      final c = FullScreenController(window);
      await c.apply(true);
      await c.apply(true);
      expect(window.attempts, 2);
      expect(c.applied, isNull);
    });

    test('the shared controller follows a swapped platform window', () async {
      final first = _FakeWindow();
      appWindowOverride = first;
      addTearDown(() => appWindowOverride = const NativeAppWindow());
      await FullScreenController.shared.apply(true);
      final second = _FakeWindow();
      appWindowOverride = second;
      await FullScreenController.shared.apply(true);
      expect(first.calls, [true]);
      expect(second.calls, [true]);
    });
  });

  group('start in full screen setting', () {
    test('parseBoolText accepts the usual spellings and ignores the rest', () {
      for (final t in ['1', 'true', 'TRUE', ' yes ', 'on']) {
        expect(parseBoolText(t), isTrue, reason: t);
      }
      for (final t in ['0', 'false', 'No', 'off']) {
        expect(parseBoolText(t), isFalse, reason: t);
      }
      for (final t in [null, '', 'maybe']) {
        expect(parseBoolText(t), isNull, reason: '$t');
      }
    });

    test('precedence: test override, environment, config file, default on', () {
      bool resolve({bool? override, String? env, Map<String, Object?> file = const {}}) =>
          resolveBoolSetting(override: override, envValue: env, file: file, key: 'start_fullscreen', fallback: true);
      expect(resolve(), isTrue);
      expect(resolve(file: {'start_fullscreen': false}), isFalse);
      expect(resolve(env: 'on', file: {'start_fullscreen': false}), isTrue);
      expect(resolve(env: '0', file: {'start_fullscreen': true}), isFalse);
      expect(resolve(env: 'garbage', file: {'start_fullscreen': false}), isFalse, reason: 'unreadable env falls through');
      expect(resolve(file: {'start_fullscreen': 'no'}), isTrue, reason: 'a non-boolean file value is ignored');
      expect(resolve(override: false, env: '1'), isFalse);
    });

    test('a saved choice wins over the installation default; none saved uses it', () async {
      SharedPreferences.setMockInitialValues({});
      final settings = await SettingsService.create();
      expect(settings.startFullScreen(true), isTrue);
      expect(settings.startFullScreen(false), isFalse);
      await settings.setStartFullScreen(false);
      expect(settings.startFullScreen(true), isFalse);
      await settings.setStartFullScreen(true);
      expect(settings.startFullScreen(false), isTrue);
    });
  });
}
