// E2E: the client's log. A playback lifecycle at `debug` writes the expected
// events to the log file (channel load, controller create/dispose, the GL
// queue, the on-air summary) without a secret in it, and at `error` a healthy
// lifecycle writes nothing. Two real app boots.
import 'dart:io';

import 'package:client/log/log.dart';
import 'package:client/main.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('client log (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;
    late Directory logDir;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();
      deviceToken = await backend.createDeviceToken();
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      logDir = Directory.systemTemp.createTempSync('playheadtv_log_e2e_');
    });

    tearDown(() async {
      await Log.close();
      Log.configure(level: LogLevel.error);
      logDir.deleteSync(recursive: true);
    });

    // Boots the app on a channel of short items and plays through one
    // crossfade, so the log covers a load, a preload and a transition.
    Future<void> playThroughOneTransition(WidgetTester tester, int number, String prefix) async {
      final channelId = await seedChannel(backend, number: number, prefix: prefix, items: 4, itemMs: 12000, startOffset: const Duration(seconds: -1));
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelId);
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
        preloadLead: const Duration(seconds: 2),
        preloadRetryBase: const Duration(milliseconds: 200),
      );
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) == '$prefix 0', failMessage: 'never landed on $prefix 0');
      await pumpUntil(tester, () => osdTitle(tester) == '$prefix 1', timeout: const Duration(seconds: 60), failMessage: 'never crossed over to $prefix 1');
    }

    testWidgets('at debug the lifecycle is written and carries no secret', (tester) async {
      Log.configure(level: LogLevel.debug, directory: logDir, secrets: ['fake-test-key', deviceToken], flushDelay: const Duration(milliseconds: 50));
      await playThroughOneTransition(tester, 95, 'LOGD');
      await tester.runAsync(Log.flush);
      final text = File('${logDir.path}/$logFileName').readAsStringSync();
      for (final expected in [
        'channel load started',
        'channel on air',
        '[controller] created',
        '[controller] disposed',
        '[gl] initialize ran',
        'opened in',
        'preload]',
        'crossfade] item 1 on air',
      ]) {
        expect(text, contains(expected), reason: 'the debug log should mention "$expected"');
      }
      expect(text, isNot(contains('fake-test-key')));
      expect(text, isNot(contains(deviceToken)));
    });

    testWidgets('at error a healthy lifecycle writes nothing', (tester) async {
      Log.configure(level: LogLevel.error, directory: logDir, secrets: ['fake-test-key', deviceToken], flushDelay: const Duration(milliseconds: 50));
      await playThroughOneTransition(tester, 96, 'LOGE');
      await tester.runAsync(Log.flush);
      final file = File('${logDir.path}/$logFileName');
      final text = file.existsSync() ? file.readAsStringSync() : '';
      expect(text, isEmpty, reason: 'nothing failed, so an error-level log stays empty');
    });
  });
}
