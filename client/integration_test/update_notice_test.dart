// E2E suite for the one-time "updated to X" changelog (update_overlay.dart):
// real app, real Go backend, fake video server (conventions as in
// mosaic_test.dart / update_banner_test.dart). The updater's state file is
// written by the test, as the updater itself does after a swap.
//
// Real boots in this file: 2.
//   1. the changelog shows over the playing channel as PLAIN text, a long one
//      scrolls with Down (and Up) only, Select closes it
//   2. it is not shown again on the next start
import 'dart:io';

import 'package:client/main.dart';
import 'package:client/update/install_kind.dart';
import 'package:client/update/install_layout.dart';
import 'package:client/update/update_notices.dart';
import 'package:client/widgets/update_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/video_fixture.dart';

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('post-update changelog (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String channelId;
    late Directory scratch;
    late File stateFile;

    // Long enough to overflow the panel, with markup that must stay literal.
    final notes = [
      'Changes since 1.0.0:',
      '',
      '- **bold** and [a link](https://example.test/x) and <b>tags</b> stay literal text',
      for (var i = 1; i <= 60; i++) '- change number $i',
    ].join('\n');

    setUpAll(() async {
      scratch = Directory.systemTemp.createTempSync('playheadtv_notice_');
      stateFile = File('${scratch.path}/state.json');
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();

      channelId = await backend.createChannel(number: 5, name: 'Notice Channel');
      final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Notice source');
      final items = await backend.syncCatalogItems(mediaSourceId, [
        {'external_id': 'notice-a', 'kind': 'episode', 'title': 'NOTICE PROGRAM', 'duration_ms': 1800000},
      ]);
      final blockId = await backend.createBlockWithItems(items);
      await backend.createScheduleOnce(
        channelId: channelId,
        blockId: blockId,
        startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 3)),
      );
      final deviceToken = await backend.createDeviceToken();
      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
      );
      UpdateState(
        lastUpdate: LastUpdate(version: '1.1.0', previous: '1.0.0', notes: notes, installedAt: DateTime.now().toUtc()),
      ).write(stateFile);
      UpdateNotices.overrideForTest(UpdateNoticeEnvironment(
        runningVersion: '1.1.0',
        kind: InstallKind.tarball,
        stateFile: stateFile,
        releaseSource: null,
        autoUpdate: () => true,
        startDelay: Duration.zero,
      ));
    });

    tearDownAll(() async {
      UpdateNotices.overrideForTest(null);
      await backend.stop();
      await video.stop();
      scratch.deleteSync(recursive: true);
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelId);
    });

    Future<void> pumpUntil(WidgetTester tester, bool Function() condition,
        {Duration timeout = const Duration(seconds: 20), String? failMessage}) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met within $timeout');
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    bool changelogShown() => find.byKey(const Key('update-changelog')).evaluate().isNotEmpty;
    bool playerShown() => find.byKey(const Key('osd-channel-number')).evaluate().isNotEmpty;
    double scrollOffset(WidgetTester tester) => tester
        .widget<SingleChildScrollView>(find.descendant(
            of: find.byKey(const Key('update-changelog')), matching: find.byType(SingleChildScrollView)))
        .controller!
        .offset;

    testWidgets('shows once as plain text, scrolls with Up/Down, Select closes it', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.remove('update_changelog_shown_for');

      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, playerShown, failMessage: 'the player did not come up');
      await pumpUntil(tester, changelogShown, failMessage: 'the changelog did not appear');

      expect(tester.widget<Text>(find.byKey(const Key('update-changelog-title'))).data, 'Updated to 1.1.0');
      expect(tester.widget<Text>(find.byKey(const Key('update-changelog-text'))).data, notes,
          reason: 'the release body, verbatim, as a plain Text');
      expect(find.byType(RichText).evaluate().where((e) => (e.widget as RichText).text.toPlainText().contains('<b>tags</b>')),
          isNotEmpty);
      expect(playerShown(), isTrue, reason: 'the player keeps running under it');

      // Long: the hint offers scrolling, and Down/Up move the text.
      await pumpUntil(
        tester,
        () => tester.widget<Text>(find.byKey(const Key('update-changelog-hint'))).data!.contains('Up/Down'),
        failMessage: 'a long changelog did not offer scrolling',
      );
      expect(scrollOffset(tester), 0);
      await pressKey(tester, LogicalKeyboardKey.arrowDown);
      for (var i = 0; i < 4; i++) {
        await tester.pump(const Duration(milliseconds: 100));
      }
      final afterDown = scrollOffset(tester);
      expect(afterDown, greaterThan(0));
      await pressKey(tester, LogicalKeyboardKey.arrowUp);
      for (var i = 0; i < 4; i++) {
        await tester.pump(const Duration(milliseconds: 100));
      }
      expect(scrollOffset(tester), lessThan(afterDown));

      await pressKey(tester, LogicalKeyboardKey.enter); // Select
      await tester.pump();
      expect(changelogShown(), isFalse, reason: 'Select closes the changelog');
      expect(playerShown(), isTrue);
      expect(prefs.getString('update_changelog_shown_for'), '1.1.0');
    });

    testWidgets('it is not shown again on the next start', (tester) async {
      // The previous case left "shown for 1.1.0" in the real store.
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, playerShown, failMessage: 'the player did not come up');
      for (var i = 0; i < 10; i++) {
        await tester.pump(const Duration(milliseconds: 200));
      }
      expect(changelogShown(), isFalse);
    });
  });
}
