// Goldens of the screens scaled in the second UI-scale slice (EPG overlay, track
// picker, update banner and changelog, Remap controls, Offline cache), each at
// 1280x720 and 3840x2160, plus the checks that a large fixed "Interface size"
// cannot overflow the OSD on a small window. flutter_test draws text with the
// Ahem font, so the goldens show layout and proportions, not glyph shapes.
// Regenerate with `flutter test --update-goldens test/ui_scale_screens_golden_test.dart`.
import 'dart:io';

import 'package:client/cache/schedule_cache.dart';
import 'package:client/main.dart' show AppConfig, epgThumbnailHeight, epgThumbnailMargin, epgThumbnailWidth;
import 'package:client/cache/offline_runtime.dart';
import 'package:client/pages/controls_mapping_page.dart';
import 'package:client/pages/offline_cache_page.dart';
import 'package:client/playhead_api.dart';
import 'package:client/ui/ui_scale.dart';
import 'package:client/update/install_kind.dart';
import 'package:client/update/install_layout.dart';
import 'package:client/update/release_source.dart';
import 'package:client/update/semver.dart';
import 'package:client/update/update_notices.dart';
import 'package:client/widgets/epg_overlay.dart';
import 'package:client/widgets/osd_overlay.dart';
import 'package:client/widgets/track_selector_overlay.dart';
import 'package:client/widgets/update_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

const _sizes = [('1280x720', Size(1280, 720)), ('3840x2160', Size(3840, 2160))];

Future<void> useSize(WidgetTester tester, Size size) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
}

Widget app(Widget home, {bool updateHost = false}) => MaterialApp(
  debugShowCheckedModeBanner: false,
  theme: ThemeData.dark(),
  builder: (context, child) => UiScaleHost(child: updateHost ? UpdateOverlayHost(child: child!) : child!),
  home: home,
);

class _FixedSource implements ReleaseSource {
  @override
  Future<ReleaseInfo?> latestStable() async {
    final url = Uri.parse('https://releases.example.test/x');
    return ReleaseInfo(version: SemVer.tryParse('2.0.0')!, packageUrl: url, checksumsUrl: url);
  }

  @override
  Future<List<int>> fetchBytes(Uri url) => throw UnimplementedError();

  @override
  Future<void> download(Uri url, File dest) => throw UnimplementedError();
}

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
    UiScaleOverride.notifier.value = null;
  });

  group('epg goldens', () {
    final now = DateTime.utc(2030, 1, 1, 12, 10);
    final channels = [for (var i = 0; i < 7; i++) PlayheadChannel(id: 'c$i', number: i + 1, name: 'Channel ${i + 1}')];
    ScheduleCache buildCache() {
      final cache = ScheduleCache.inMemory(clock: () => now);
      cache.replaceChannels(channels);
      EpgEntry program(String channel, int n, Duration from, Duration length) => EpgEntry(
        id: '$channel-$n',
        kind: 'program',
        catalogItemId: 'item-$channel-$n',
        startAt: now.add(from),
        endAt: now.add(from + length),
        title: 'Program $channel $n',
      );
      cache.applySnapshot({
        for (var i = 0; i < channels.length; i++)
          channels[i].id: [
            program(channels[i].id, 0, Duration(minutes: -50 - 10 * i), Duration(minutes: 80 + 10 * i)),
            program(channels[i].id, 1, Duration(minutes: 30), const Duration(minutes: 45)),
            program(channels[i].id, 2, Duration(minutes: 75 + 5 * i), const Duration(minutes: 90)),
            program(channels[i].id, 3, Duration(minutes: 165 + 5 * i), const Duration(minutes: 60)),
          ],
      }, from: now.subtract(const Duration(hours: 3)));
      return cache;
    }

    for (final (name, size) in _sizes) {
      testWidgets('epg_$name', (tester) async {
        await useSize(tester, size);
        final cache = buildCache();
        addTearDown(cache.close);
        final k = resolveUiScale(size);
        final rect = Rect.fromLTWH(
          size.width - (epgThumbnailMargin + epgThumbnailWidth) * k,
          epgThumbnailMargin * k,
          epgThumbnailWidth * k,
          epgThumbnailHeight * k,
        );
        await tester.pumpWidget(
          app(
            Scaffold(
              backgroundColor: Colors.indigo.shade900,
              body: EpgOverlay(
                key: const Key('epg'),
                channels: channels,
                cache: cache,
                currentChannelId: 'c2',
                videoThumbnailRect: rect,
                onSelectLiveChannel: (_) {},
                onClose: () {},
                clock: () => now,
              ),
            ),
          ),
        );
        await tester.pump();
        await tester.pump(const Duration(milliseconds: 400));
        expect(tester.takeException(), isNull);
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/epg_$name.png'));
        await tester.pumpWidget(const SizedBox());
      });
    }

    testWidgets('grid keeps the 3h span and the sizes follow the scale', (tester) async {
      Future<(double, double, double)> measure(Size size) async {
        final k = resolveUiScale(size);
        await useSize(tester, size);
        final cache = buildCache();
        addTearDown(cache.close);
        await tester.pumpWidget(
          app(
            Scaffold(
              body: EpgOverlay(
                key: const Key('epg'),
                channels: channels,
                cache: cache,
                currentChannelId: 'c2',
                videoThumbnailRect: Rect.fromLTWH(size.width - 100, 10, 90, 50),
                onSelectLiveChannel: (_) {},
                onClose: () {},
                clock: () => now,
              ),
            ),
          ),
        );
        await tester.pump();
        await tester.pump(const Duration(milliseconds: 400));
        final row = tester.getSize(find.byKey(const Key('epg-row-c0'))).height;
        final rows = tester.getSize(find.byKey(const Key('epg-rows')));
        // Two 30-minute ticks are 1/6 of the visible span apart.
        final ticks = find.byWidgetPredicate(
          (w) => w.key is ValueKey<String> && (w.key as ValueKey<String>).value.startsWith('epg-tick-'),
        );
        final first = tester.getTopLeft(ticks.at(1)).dx - tester.getTopLeft(ticks.at(0)).dx;
        await tester.pumpWidget(const SizedBox());
        // The time viewport is the rows' width minus the scaled channel column.
        return (row, first * 6, rows.width - 220 * k);
      }

      final small = await measure(const Size(1280, 720));
      final big = await measure(const Size(3840, 2160));
      expect(big.$1 / small.$1, closeTo(3.0, 1e-6)); // row height: 2.0 vs 0.667
      // The 3h span still fills the time viewport (width minus padding and the channel column).
      expect(small.$2, closeTo(small.$3, 1.0));
      expect(big.$2, closeTo(big.$3, 1.0));
    });
  });

  group('track picker goldens', () {
    for (final (name, size) in _sizes) {
      testWidgets('track_picker_$name', (tester) async {
        await useSize(tester, size);
        await tester.pumpWidget(
          app(
            Scaffold(
              backgroundColor: Colors.black,
              body: TrackSelectorOverlay(
                key: const Key('picker'),
                audioOptions: const [
                  TrackOption(index: 0, label: 'English (5.1)'),
                  TrackOption(index: 1, label: 'Portuguese (stereo)'),
                  TrackOption(index: 2, label: 'Commentary'),
                ],
                subtitleOptions: const [
                  TrackOption(index: -1, label: 'None'),
                  TrackOption(index: 0, label: 'English'),
                  TrackOption(index: 1, label: 'Portuguese'),
                ],
                initialAudioFocusIndex: 1,
                onSelect: (_, _) {},
                onClose: () {},
              ),
            ),
          ),
        );
        await tester.pump();
        expect(tester.takeException(), isNull);
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/track_picker_$name.png'));
      });
    }
  });

  group('update overlay goldens', () {
    late Directory scratch;
    setUp(() => scratch = Directory.systemTemp.createTempSync('playheadtv_update_golden_'));
    tearDown(() {
      UpdateNotices.overrideForTest(null);
      scratch.deleteSync(recursive: true);
    });

    UpdateNoticeEnvironment env({File? stateFile, ReleaseSource? source}) => UpdateNoticeEnvironment(
      runningVersion: '1.0.0',
      kind: InstallKind.tarball,
      stateFile: stateFile,
      releaseSource: source,
      autoUpdate: () => true,
      startDelay: Duration.zero,
    );

    Future<void> shows(WidgetTester tester, Size size, Key key) async {
      await useSize(tester, size);
      await tester.pumpWidget(app(const Scaffold(backgroundColor: Colors.black), updateHost: true));
      // The host loads the settings and asks the (fake) source on real time.
      for (var i = 0; i < 20 && find.byKey(key).evaluate().isEmpty; i++) {
        await tester.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 20)));
        await tester.pump(const Duration(milliseconds: 10));
      }
      expect(find.byKey(key), findsOneWidget);
    }

    for (final (name, size) in _sizes) {
      testWidgets('update_banner_$name', (tester) async {
        UpdateNotices.overrideForTest(env(source: _FixedSource()));
        await shows(tester, size, const Key('update-banner'));
        expect(tester.takeException(), isNull);
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/update_banner_$name.png'));
        await tester.pumpWidget(const SizedBox());
      });

      testWidgets('update_changelog_$name', (tester) async {
        final file = File('${scratch.path}/state.json');
        UpdateState(
          lastUpdate: LastUpdate(
            version: '1.0.0',
            previous: '0.9.0',
            notes: [for (var i = 1; i <= 40; i++) '- Change number $i of this release'].join('\n'),
            installedAt: DateTime.utc(2030),
          ),
        ).write(file);
        UpdateNotices.overrideForTest(env(stateFile: file));
        await shows(tester, size, const Key('update-changelog'));
        expect(tester.takeException(), isNull);
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/update_changelog_$name.png'));
        await tester.pumpWidget(const SizedBox());
      });
    }

    testWidgets('the banner scales with the screen and never takes focus', (tester) async {
      UpdateNotices.overrideForTest(env(source: _FixedSource()));
      await shows(tester, const Size(1280, 720), const Key('update-banner'));
      final small = tester.widget<Text>(find.byKey(const Key('update-banner-message'))).style!.fontSize!;
      var focusInBanner = false;
      FocusManager.instance.primaryFocus?.context?.visitAncestorElements((e) {
        if (e.widget.key == const Key('update-banner')) focusInBanner = true;
        return true;
      });
      expect(focusInBanner, isFalse);
      await tester.pumpWidget(const SizedBox());
      UpdateNotices.overrideForTest(env(source: _FixedSource()));
      await shows(tester, const Size(3840, 2160), const Key('update-banner'));
      final big = tester.widget<Text>(find.byKey(const Key('update-banner-message'))).style!.fontSize!;
      expect(big / small, closeTo(3.0, 1e-9));
      await tester.pumpWidget(const SizedBox());
    });
  });

  group('page goldens', () {
    for (final (name, size) in _sizes) {
      testWidgets('controls_mapping_$name', (tester) async {
        await useSize(tester, size);
        await tester.pumpWidget(app(const ControlsMappingPage()));
        await tester.pumpAndSettle();
        expect(tester.takeException(), isNull);
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/controls_mapping_$name.png'));
      });

      testWidgets('offline_cache_$name', (tester) async {
        await useSize(tester, size);
        final cacheDir = Directory.systemTemp.createTempSync('playheadtv_offline_golden_').path;
        AppConfig.overrideForTest(
          jellyfinUrl: 'http://127.0.0.1:1',
          jellyfinApiKey: 'unused',
          playheadBaseUrl: 'http://127.0.0.1:1',
          cacheDir: cacheDir,
          cacheSyncInterval: const Duration(days: 1),
        );
        addTearDown(() {
          OfflineRuntime.reset();
          Directory(cacheDir).deleteSync(recursive: true);
        });
        await tester.runAsync(() => OfflineRuntime.ensure());
        await tester.pumpWidget(app(const OfflineCachePage()));
        await tester.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 200)));
        await tester.pump();
        await tester.pump();
        expect(tester.takeException(), isNull);
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/offline_cache_$name.png'));
        await tester.pumpWidget(const SizedBox());
      });
    }
  });

  group('largest fixed Interface size cannot overflow the OSD', () {
    Widget osd() => Scaffold(
      backgroundColor: Colors.black,
      body: Stack(
        children: [
          ChannelOsd(
            key: const Key('osd'),
            channelNumber: 7,
            channelName: 'Movies',
            channelArtUrl: null,
            itemLabel: 'A Long Film Title',
            nextItemLabel: 'The Next One',
            startAt: DateTime.utc(2030, 1, 1, 11, 30),
            endAt: DateTime.utc(2030, 1, 1, 13, 0),
            progressByClock: true,
            controller: null,
            focusMode: ValueNotifier<bool>(false),
            onOpenSubtitles: () {},
            onOpenAudio: () {},
            recentChannels: const [
              OsdChannelShortcut(id: 'a', number: 2, name: 'News'),
              OsdChannelShortcut(id: 'b', number: 9, name: 'Sports'),
              OsdChannelShortcut(id: 'c', number: 11, name: 'Kids'),
            ],
            onSelectChannel: (_) {},
            now: () => DateTime.utc(2030, 1, 1, 12, 0),
          ),
        ],
      ),
    );

    testWidgets('the design constants hold the OSD at scale 1', (tester) async {
      await useSize(tester, const Size(1920, 1080));
      await tester.pumpWidget(app(osd()));
      await tester.pump();
      // The panel anchored at the bottom grows from there; the clock is at the top.
      final panelTop = tester.getTopLeft(find.byKey(const Key('osd-channel-logo'))).dy;
      final clockBottom = tester.getBottomLeft(find.byKey(const Key('osd-ends-at'))).dy;
      expect(1080 - panelTop, lessThan(uiScaleFitHeight - clockBottom), reason: 'panel and clock fit uiScaleFitHeight');
      final cards = tester.getTopRight(find.byKey(const Key('osd-recent-c'))).dx;
      expect(cards, lessThan(uiScaleFitWidth * 1.5)); // sanity: cards sit in the leftmost part
      await tester.pumpWidget(const SizedBox());
    });

    for (final (size, label) in const [
      (Size(1280, 720), '1280x720'),
      (Size(800, 600), '800x600'),
      (Size(640, 360), '640x360'),
      (Size(3840, 2160), '3840x2160'),
    ]) {
      testWidgets('at $label with every fixed step', (tester) async {
        await useSize(tester, size);
        for (final step in uiScaleSteps) {
          await tester.pumpWidget(app(osd()));
          // The host first loads the saved choice (none); then the step is applied.
          await tester.runAsync(() => Future<void>.delayed(const Duration(milliseconds: 20)));
          UiScaleOverride.notifier.value = step;
          await tester.pump();
          expect(tester.takeException(), isNull, reason: 'step $step at $label');
          final context = tester.element(find.byKey(const Key('osd')));
          expect(UiScale.of(context), lessThanOrEqualTo(step));
          expect(UiScale.of(context), resolveUiScale(size, override: step));
          // The whole panel stays on screen.
          final top = tester.getTopLeft(find.byKey(const Key('osd-channel-logo'))).dy;
          expect(top, greaterThanOrEqualTo(0), reason: 'step $step at $label');
          final clock = tester.getRect(find.byKey(const Key('osd-clock')));
          expect(clock.top, greaterThanOrEqualTo(0));
          expect(clock.bottom, lessThan(top + 1), reason: 'clock above the panel, step $step at $label');
        }
        await tester.pumpWidget(const SizedBox());
      });
    }
  });
}
