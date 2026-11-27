// E2E suite for the two client-side features of this slice, against a real Go
// backend (BackendFixture) and a fake video server (VideoFixture):
//
//  1. the channel logo uploaded through PUT /channels/{id}/logo is drawn in
//     place of the channel name in the guide's channel column and in the OSD
//     header, and a channel without one still shows its name;
//  2. the always-on clock (Settings -> "Always show the clock") is drawn in the
//     top-right corner with the OSD closed, hides while the OSD is open (the OSD
//     draws the same clock there) and honours the configured opacity and size.
//
// Same conventions as channel_navigation_test.dart (read it first): no
// `pumpAndSettle` (the OSD's 500ms ticker never settles), `pressKey` for keys,
// a manual `pumpUntil`, and one real app boot per `testWidgets` — two here, the
// budget AGENTS.md asks for.
import 'dart:convert';

import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/key_focus.dart';
import 'support/video_fixture.dart';

// A valid 1x1 PNG, the same one watermark_test.dart uploads.
final _png = base64Decode(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
);

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('channel logo and always-on clock (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;

    // Channel 100 has a logo, channel 200 does not: the pair proves both the
    // logo path and the name fallback in one boot.
    const withLogo = 100;
    const withoutLogo = 200;
    late String withLogoId;
    late String withoutLogoId;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start(clipSeconds: 40);
      await backend.start();

      for (final number in [withLogo, withoutLogo]) {
        final channelId = await backend.createChannel(number: number, name: 'LOGO CHANNEL $number');
        if (number == withLogo) {
          withLogoId = channelId;
        } else {
          withoutLogoId = channelId;
        }
        final mediaSourceId = await backend.createLocalFolderMediaSource(name: 'Logo source $number');
        final catalogItemIds = await backend.syncCatalogItems(mediaSourceId, [
          {
            'external_id': 'logo-$number-a',
            'kind': 'episode',
            'title': 'PROGRAM $number A',
            // duration_ms far above the real 40s clip and the whole suite's
            // running time, so the first item stays "live" for every test.
            'duration_ms': 1800000,
          },
        ]);
        final blockId = await backend.createBlockWithItems(catalogItemIds);
        await backend.createScheduleOnce(
          channelId: channelId,
          blockId: blockId,
          startAt: DateTime.now().toUtc().subtract(const Duration(seconds: 3)),
        );
      }
      await backend.setChannelLogo(channelId: withLogoId, png: _png);

      final deviceToken = await backend.createDeviceToken();

      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'chave-de-teste-fake',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
      );
    });

    tearDownAll(() async {
      await backend.stop();
      await video.stop();
    });

    setUp(() async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.clear();
      // A history entry that matches no channel keeps boot going straight to the
      // player (the mosaic is out of scope here), which then falls back to the
      // lowest number: channel 100.
      await prefs.setString('last_watched_channel_id', 'nonexistent-channel-fixture-logo');
    });

    Future<void> pumpUntil(
      WidgetTester tester,
      bool Function() condition, {
      Duration timeout = const Duration(seconds: 15),
      String? failMessage,
    }) async {
      final deadline = DateTime.now().add(timeout);
      while (!condition()) {
        if (DateTime.now().isAfter(deadline)) {
          fail(failMessage ?? 'condition not met within $timeout');
        }
        await tester.pump(const Duration(milliseconds: 200));
      }
    }

    String? osdChannelNumberText(WidgetTester tester) {
      final finder = find.byKey(const Key('osd-channel-number'));
      if (finder.evaluate().isEmpty) return null;
      return tester.widget<Text>(finder).data;
    }

    bool osdVisible(WidgetTester tester) {
      final finder = find.ancestor(
        of: find.byKey(const Key('channel-osd')),
        matching: find.byType(AnimatedOpacity),
      );
      if (finder.evaluate().isEmpty) return false;
      return tester.widget<AnimatedOpacity>(finder).opacity == 1;
    }

    Future<void> waitForChannelNumber(WidgetTester tester, int expectedNumber) async {
      await pumpUntil(
        tester,
        () => osdChannelNumberText(tester) == '$expectedNumber',
        failMessage: 'expected OSD showing channel $expectedNumber, but it stayed on "${osdChannelNumberText(tester)}"',
      );
    }

    testWidgets('the guide and the OSD draw the uploaded logo instead of the name', (tester) async {
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, withLogo);

      // The OSD header: the logo image took the name's place (no name Text).
      expect(find.byKey(const Key('osd-channel-logo-inline')), findsOneWidget,
          reason: 'the OSD should draw the uploaded logo in the header');
      expect(find.byKey(const Key('osd-channel-name')), findsNothing,
          reason: 'the channel name should be replaced by the logo');

      // Open the guide: channel 100 shows the logo in its column, channel 200
      // keeps its name (no logo uploaded).
      await pumpUntil(
        tester,
        () => !osdVisible(tester),
        timeout: const Duration(seconds: 8),
        failMessage: 'OSD never hid on its own after the boot auto-show',
      );
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-details-panel')).evaluate().isNotEmpty,
        failMessage: 'EPG never finished loading',
      );

      // Channel 100: the label is number + logo, the name text is gone.
      expect(
        find.descendant(
          of: find.byKey(Key('epg-channel-name-$withLogoId')),
          matching: find.byKey(Key('epg-channel-logo-$withLogoId')),
        ),
        findsOneWidget,
        reason: 'the logo sits inside the channel column label',
      );
      expect(
        find.descendant(
          of: find.byKey(Key('epg-channel-name-$withLogoId')),
          matching: find.text('LOGO CHANNEL 100'),
        ),
        findsOneWidget,
        reason: 'the guide keeps the name after the logo, clipped when it does not fit',
      );

      // Channel 200: no logo, so the label is the small number followed by the
      // name — the same two pieces, minus the image.
      expect(
        find.descendant(
          of: find.byKey(Key('epg-channel-name-$withoutLogoId')),
          matching: find.text('LOGO CHANNEL 200'),
        ),
        findsOneWidget,
        reason: 'the name is drawn next to the number, not instead of it',
      );
      expect(
        find.descendant(
          of: find.byKey(Key('epg-channel-name-$withoutLogoId')),
          matching: find.text('200'),
        ),
        findsOneWidget,
        reason: 'the number keeps its place in the channel column',
      );
      expect(find.byKey(Key('epg-channel-logo-$withoutLogoId')), findsNothing,
          reason: 'a channel without a logo draws no image');

      // Back to the player.
      await pressKey(tester, LogicalKeyboardKey.escape);
      await pumpUntil(
        tester,
        () => find.byKey(const Key('epg-overlay')).evaluate().isEmpty,
        failMessage: 'Escape did not close the guide',
      );
    });

    testWidgets('the always-on clock shows with the OSD closed and steps aside while it is open', (tester) async {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setBool('clock_always_visible', true);
      await prefs.setInt('clock_opacity_percent', 60);
      await prefs.setDouble('clock_scale', 1.5);

      const clock = Key('always-on-clock');
      await tester.pumpWidget(const PlayheadSpikeApp());
      await waitForChannelNumber(tester, withLogo);

      // Boot announces the channel with the banner; the OSD is opened the way a
      // viewer opens it (Select).
      await pressKey(tester, LogicalKeyboardKey.enter);
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Select did not open the OSD');
      expect(osdVisible(tester), isTrue);
      expect(find.byKey(clock), findsNothing, reason: 'two clocks in one corner while the OSD is open');

      await pumpUntil(
        tester,
        () => !osdVisible(tester) && find.byKey(clock).evaluate().isNotEmpty,
        timeout: const Duration(seconds: 8),
        failMessage: 'the clock did not appear once the OSD hid',
      );
      final opacity = tester.widget<Opacity>(find.ancestor(of: find.byKey(clock), matching: find.byType(Opacity)).first);
      expect(opacity.opacity, closeTo(0.6, 1e-9), reason: 'the configured opacity should apply');

      final text = tester.widget<Text>(find.byKey(clock));
      expect(RegExp(r'^\d{2}:\d{2}$').hasMatch(text.data ?? ''), isTrue, reason: 'the clock shows HH:mm');

      // Opening the OSD takes it away again, closing it brings it back.
      await pressKey(tester, LogicalKeyboardKey.enter);
      await tester.pump();
      await pumpUntil(tester, () => osdVisible(tester), failMessage: 'Enter did not show the OSD');
      expect(find.byKey(clock), findsNothing, reason: 'the always-on clock must yield to the OSD clock');

      await pressKey(tester, LogicalKeyboardKey.escape);
      await tester.pump();
      await pumpUntil(
        tester,
        () => !osdVisible(tester) && find.byKey(clock).evaluate().isNotEmpty,
        failMessage: 'the always-on clock did not come back after closing the OSD',
      );
    });
  });
}
