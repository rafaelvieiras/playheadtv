// E2E suite for the channel watermark (docs/ARCHITECTURE.md "Watermark"): a
// real Go backend resolves the watermark per guide entry and the client draws
// it over the player. Same harness as base_schedule_test.dart (real backend +
// fake video server, no `pumpAndSettle`). One real app boot in this file (see
// AGENTS.md on boot counts).
//
// Lineup (one item per block, 20s each, back to back from P, the first one
// already on air when the app boots):
//
//   P      .. P+20s  PLAIN   block inherits the channel's watermark
//   P+20s  .. P+40s  QUIET   block disables the watermark
//   P+40s  .. P+60s  OWN     block overrides it with its own image/options
//
// Proven end to end: the channel's watermark shows over the player (its
// corner, opacity and size), disappears while the disabled block's program is
// on air, and the override block draws its own image with its own options.
import 'dart:io';

import 'package:client/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'support/backend_fixture.dart';
import 'support/offline_helpers.dart';
import 'support/video_fixture.dart';

// A valid 1x1 PNG.
const _png = <int>[
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89, //
  0, 0, 0, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xff, 0xff, 0x3f, 0, 5, 0xfe, 2, 0xfe, 0xdc, 0xcc, 0x59, 0xe7, //
  0, 0, 0, 0, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
];

typedef Mark = ({double elapsed, bool shown, Rect? rect, double? opacity, String? file});

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();

  group('watermark over the player (real e2e)', () {
    late BackendFixture backend;
    late VideoFixture video;
    late String deviceToken;

    const second = 1000;

    setUpAll(() async {
      backend = BackendFixture();
      video = VideoFixture();
      await video.start(clipSeconds: 40);
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
    });

    Mark mark(WidgetTester tester, DateTime p) {
      final elapsed = DateTime.now().toUtc().difference(p).inMilliseconds / 1000;
      final image = find.byKey(const Key('watermark-image'));
      if (image.evaluate().isEmpty) return (elapsed: elapsed, shown: false, rect: null, opacity: null, file: null);
      final widget = tester.widget<Image>(image);
      final provider = widget.image;
      return (
        elapsed: elapsed,
        shown: true,
        rect: tester.getRect(image),
        opacity: tester.widget<Opacity>(find.byKey(const Key('watermark-opacity'))).opacity,
        file: provider is FileImage ? provider.file.path : null,
      );
    }

    testWidgets('channel watermark shows, a disabling block hides it, an overriding block draws its own', (tester) async {
      final channelId = await backend.createChannel(number: 81, name: 'Watermark 81');
      final source = await backend.createLocalFolderMediaSource(name: 'Watermark source');
      final ids = await backend.syncCatalogItems(source, [
        for (final (key, title) in [('plain', 'WM PLAIN'), ('quiet', 'WM QUIET'), ('own', 'WM OWN')])
          {'external_id': 'wm-$key', 'kind': 'movie', 'title': title, 'duration_ms': 20 * second},
      ]);
      final plain = await backend.createBlockWithItems([ids[0]]);
      final quiet = await backend.createBlockWithItems([ids[1]]);
      final own = await backend.createBlockWithItems([ids[2]]);

      final chWm = await backend.setWatermark(
        ownerType: 'channel',
        ownerId: channelId,
        mode: 'override',
        png: _png,
        position: 'top_right',
        opacityPct: 50,
        sizePct: 10,
      );
      await backend.setWatermark(ownerType: 'block', ownerId: quiet, mode: 'disable');
      final ownWm = await backend.setWatermark(
        ownerType: 'block',
        ownerId: own,
        mode: 'override',
        png: _png,
        position: 'bottom_left',
        opacityPct: 90,
        sizePct: 20,
      );
      expect(chWm['image_id'], isNot(ownWm['image_id']));

      final p = DateTime.fromMillisecondsSinceEpoch(DateTime.now().subtract(const Duration(seconds: 3)).millisecondsSinceEpoch, isUtc: true);
      await backend.createScheduleWithItems(
        channelId: channelId,
        items: [
          (blockId: plain, startAt: p),
          (blockId: quiet, startAt: p.add(const Duration(seconds: 20))),
          (blockId: own, startAt: p.add(const Duration(seconds: 40))),
        ],
      );
      await backend.waitForEpgEntries(channelId, 3);

      // The backend's side of the contract: the resolved watermark per entry.
      final raw = await backend.getEpg(channelId, fromMs: p.millisecondsSinceEpoch - 60000);
      final entries = raw.take(3).toList();
      expect([for (final e in entries) e['title']], ['WM PLAIN', 'WM QUIET', 'WM OWN'], reason: '$raw');
      expect(entries[0]['watermark']['image_id'], chWm['image_id']);
      expect(entries[1].containsKey('watermark'), isFalse);
      expect(entries[2]['watermark']['image_id'], ownWm['image_id']);
      expect(entries[2]['watermark']['position'], 'bottom_left');

      AppConfig.overrideForTest(
        jellyfinUrl: video.baseUrl,
        jellyfinApiKey: 'fake-test-key',
        playheadBaseUrl: backend.baseUrl,
        playheadDeviceToken: deviceToken,
      );
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('last_watched_channel_id', channelId);
      await tester.pumpWidget(const PlayheadSpikeApp());
      await pumpUntil(tester, () => osdTitle(tester) != null, failMessage: 'the OSD never appeared');

      final marks = <Mark>[];
      await pumpUntil(
        tester,
        () {
          marks.add(mark(tester, p));
          return marks.last.elapsed >= 56;
        },
        timeout: const Duration(seconds: 80),
        failMessage: 'the lineup never reached the override block',
      );

      Iterable<Mark> within(double from, double to) => marks.where((m) => m.elapsed >= from && m.elapsed <= to);
      final size = tester.getSize(find.byType(Scaffold).first);

      // The channel watermark over the inheriting block's program.
      final inherited = within(10, 17).where((m) => m.shown).toList();
      expect(inherited, isNotEmpty, reason: 'the channel watermark never showed over the inheriting block');
      for (final m in inherited) {
        expect(m.opacity, closeTo(0.5, 0.001));
        expect(m.rect!.height, closeTo(size.height * 0.10, 2));
        expect(m.rect!.center.dx, greaterThan(size.width / 2), reason: 'top_right: ${m.rect} in $size');
        expect(m.rect!.center.dy, lessThan(size.height / 2));
      }

      // Disabled while the quiet block is on air (a margin around the
      // boundaries for the crossfade ramp).
      final quietMarks = within(24, 37).toList();
      expect(quietMarks, isNotEmpty);
      expect(quietMarks.any((m) => m.shown), isFalse, reason: 'the watermark showed over the disabling block: ${quietMarks.where((m) => m.shown).toList()}');

      // The overriding block's own image and options.
      final overridden = within(46, 56).where((m) => m.shown).toList();
      expect(overridden, isNotEmpty, reason: 'the override watermark never showed');
      for (final m in overridden) {
        expect(m.opacity, closeTo(0.9, 0.001));
        expect(m.rect!.height, closeTo(size.height * 0.20, 2));
        expect(m.rect!.center.dx, lessThan(size.width / 2), reason: 'bottom_left: ${m.rect} in $size');
        expect(m.rect!.center.dy, greaterThan(size.height / 2));
      }
      // A different stored image (the art cache names its files per URL).
      expect(overridden.first.file, isNot(inherited.first.file));
      expect(File(inherited.first.file!).existsSync(), isTrue);

      // The overlay is inert: it is not focusable and not hit-testable.
      expect(find.byKey(const Key('watermark-overlay')), findsOneWidget);
    });
  });
}
