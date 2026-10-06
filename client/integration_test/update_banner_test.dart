// E2E suite for the "new version" banner (update_overlay.dart): real app against
// a real Go backend and a fake video server, as in mosaic_test.dart (read that
// file first for the conventions: manual `pumpUntil`, never `pumpAndSettle`,
// keys sent through `pressKey`, the real SharedPreferences store cleared in
// `setUp`). The release listing is a fixed in-test ReleaseSource, so no network.
//
// Real boots in this file: 3 (see AGENTS.md on the boot budget).
//   1. banner shown over the playing channel, Select dismisses it
//   2. the same version is not shown again
//   3. a newer release is shown again, and Back only closes it (the page below
//      does not see that Back)
import 'dart:io';

import 'package:client/main.dart';
import 'package:client/update/install_kind.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/semver.dart';
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

/// A release listing that always answers the given newest version.
class _FixedSource implements ReleaseSource {
  final String version;
  int asked = 0;
  _FixedSource(this.version);

  @override
  Future<ReleaseInfo?> latestStable() async {
    asked++;
    final v = SemVer.tryParse(version)!;
    final url = Uri.parse('https://releases.example.test/x');
    return ReleaseInfo(version: v, packageUrl: url, checksumsUrl: url);
  }

  @override
  Future<List<int>> fetchBytes(Uri url) => throw UnimplementedError();

  @override
  Future<void> download(Uri url, File dest) => throw UnimplementedError();
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('new-version banner (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String channelId;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start();
      await backend.start();

      channelId = await backend.createChannel(number: 4, name: 'Banner Channel');
      final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Banner source');
      final items = await backend.syncCatalogItems(mediaSourceId, [
        {'external_id': 'banner-a', 'kind': 'episode', 'title': 'BANNER PROGRAM', 'duration_ms': 1800000},
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
    });

    tearDownAll(() async {
      UpdateNotices.overrideForTest(null);
      await backend.stop();
      await video.stop();
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      // Start every case on the player of the channel (history), not the mosaic.
      await prefs.setString('last_watched_channel_id', channelId);
    });

    // An environment of a released, unmanaged client (an unknown install kind,
    // so the message is the generic one) whose newest release is [latest].
    _FixedSource useRelease(String latest, {String running = '1.0.0'}) {
      final source = _FixedSource(latest);
      UpdateNotices.overrideForTest(UpdateNoticeEnvironment(
        runningVersion: running,
        kind: InstallKind.unknown,
        stateFile: null,
        releaseSource: source,
        autoUpdate: () => true,
        startDelay: Duration.zero,
      ));
      return source;
    }

    Future<void> pumpUntil(WidgetTester tester, bool Function() condition,
        {Duration timeout = const Duration(seconds: 20), String? failMessage}) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) fail(failMessage ?? 'condition not met within $timeout');
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    bool bannerShown() => find.byKey(const Key('update-banner')).evaluate().isNotEmpty;
    bool playerShown() => find.byKey(const Key('osd-channel-number')).evaluate().isNotEmpty;

    Future<void> waitForPlayer(WidgetTester tester) => pumpUntil(tester, playerShown, failMessage: 'the player did not come up');

    testWidgets('shows over the playing channel and Select dismisses it', (tester) async {
      final source = useRelease('1.1.0');
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForPlayer(tester);
      await pumpUntil(tester, bannerShown, failMessage: 'the banner did not appear');

      expect(source.asked, 1);
      expect(find.text('Update available'), findsOneWidget);
      expect(tester.widget<Text>(find.byKey(const Key('update-banner-message'))).data, contains('Version 1.1.0 is available'));
      expect(playerShown(), isTrue, reason: 'the banner sits over the player, which keeps running');

      await pressKey(tester, LogicalKeyboardKey.enter); // Select
      await tester.pump();
      expect(bannerShown(), isFalse, reason: 'Select dismisses the banner');
      final prefs = await SharedPreferences.getInstance();
      expect(prefs.getString('dismissed_update_version'), '1.1.0');
    });

    testWidgets('the same version is not shown again', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('dismissed_update_version', '1.1.0');
      final source = useRelease('1.1.0');
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForPlayer(tester);
      // The check ran (the listing was asked) and found nothing to show.
      await pumpUntil(tester, () => source.asked > 0, failMessage: 'the release listing was never asked');
      for (var i = 0; i < 10; i++) {
        await tester.pump(const Duration(milliseconds: 200));
      }
      expect(bannerShown(), isFalse);
    });

    testWidgets('a newer release is shown again, and Back only closes it', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('dismissed_update_version', '1.1.0');
      useRelease('1.2.0');
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForPlayer(tester);
      await pumpUntil(tester, bannerShown, failMessage: 'the banner for the newer release did not appear');
      expect(tester.widget<Text>(find.byKey(const Key('update-banner-message'))).data, contains('1.2.0'));

      await pressKey(tester, LogicalKeyboardKey.escape); // Back
      await tester.pump();
      expect(bannerShown(), isFalse, reason: 'Back closes the banner');
      expect(playerShown(), isTrue, reason: 'that Back was taken by the banner: the player stays');
      expect(find.byKey(const Key('mosaic-page')), findsNothing);
      expect(prefs.getString('dismissed_update_version'), '1.1.0', reason: 'Back is not a dismissal');
    });
  });
}
