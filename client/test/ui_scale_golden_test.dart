// Goldens of the scaled UI at two sizes (1280x720 and 3840x2160, plus a 21:9
// mosaic): the same screen with proportional text, icons and rings. flutter_test
// draws text with the Ahem font, so the goldens show layout and proportions,
// not glyph shapes. Regenerate with `flutter test --update-goldens`.
import 'package:client/mosaic_layout.dart';
import 'package:client/ui/ui_scale.dart';
import 'package:client/widgets/exit_dialog.dart';
import 'package:client/widgets/mosaic_frame.dart';
import 'package:client/widgets/osd_overlay.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

Future<void> useSize(WidgetTester tester, Size size) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
}

Widget app(Widget home) => MaterialApp(
      debugShowCheckedModeBanner: false,
      theme: ThemeData.dark(),
      builder: (context, child) => UiScaleHost(child: child!),
      home: home,
    );

class _Mosaic extends StatelessWidget {
  final int focused;
  final Widget? background;
  const _Mosaic({this.focused = 0, this.background});

  @override
  Widget build(BuildContext context) {
    // Featured channel, ten more channels, then Settings (a full frame).
    const tiles = 13;
    final all = [
      MosaicTileSpec.channel(key: const Key('feat'), number: 1, name: 'Featured'),
      for (var i = 0; i < tiles - 2; i++)
        MosaicTileSpec.channel(key: Key('t$i'), number: i + 2, name: 'Channel ${i + 2}'),
      const MosaicTileSpec.settings(),
    ];
    return Scaffold(
      backgroundColor: Colors.black,
      body: MosaicFrameView(
        tiles: all,
        focusNodes: List.generate(tiles, (_) => FocusNode()),
        focusedIndex: focused,
        featuredBackground: background ?? Container(color: Colors.indigo.shade900),
      ),
    );
  }
}

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  group('mosaic goldens', () {
    for (final (name, size) in const [
      ('mosaic_1280x720', Size(1280, 720)),
      ('mosaic_3840x2160', Size(3840, 2160)),
      ('mosaic_21x9_2560x1080', Size(2560, 1080)),
    ]) {
      testWidgets(name, (tester) async {
        await useSize(tester, size);
        await tester.pumpWidget(app(const _Mosaic(focused: 5)));
        await tester.pump();
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/$name.png'));
      });
    }
  });

  group('osd goldens', () {
    for (final (name, size) in const [('osd_1280x720', Size(1280, 720)), ('osd_3840x2160', Size(3840, 2160))]) {
      testWidgets(name, (tester) async {
        await useSize(tester, size);
        final now = DateTime.utc(2030, 1, 1, 12, 0);
        await tester.pumpWidget(app(Scaffold(
          backgroundColor: Colors.black,
          body: Stack(children: [
            ChannelOsd(
              key: const Key('osd'),
              channelNumber: 7,
              channelName: 'Movies',
              channelArtUrl: null,
              itemLabel: 'A Long Film Title',
              nextItemLabel: 'The Next One',
              startAt: now.subtract(const Duration(minutes: 30)),
              endAt: now.add(const Duration(minutes: 60)),
              progressByClock: true,
              controller: null,
              focusMode: ValueNotifier<bool>(false),
              onOpenSubtitles: () {},
              onOpenAudio: () {},
              recentChannels: const [
                OsdChannelShortcut(id: 'a', number: 2, name: 'News'),
                OsdChannelShortcut(id: 'b', number: 9, name: 'Sports'),
              ],
              onSelectChannel: (_) {},
              now: () => now,
            ),
          ]),
        )));
        await tester.pump();
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/$name.png'));
        // Dispose the OSD (and its ticker) before the test ends.
        await tester.pumpWidget(const SizedBox());
      });
    }
  });

  group('dialog goldens', () {
    for (final (name, size) in const [('dialog_1280x720', Size(1280, 720)), ('dialog_3840x2160', Size(3840, 2160))]) {
      testWidgets(name, (tester) async {
        await useSize(tester, size);
        await tester.pumpWidget(app(const Scaffold(backgroundColor: Colors.black)));
        final context = tester.element(find.byType(Scaffold));
        showDialog<bool>(context: context, builder: (_) => ExitDialog(isBack: (_) => false));
        await tester.pump();
        await tester.pump(const Duration(milliseconds: 400));
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/$name.png'));
      });
    }
  });

  group('mosaic frame', () {
    testWidgets('does not overflow or throw at any size', (tester) async {
      for (final size in const [Size(320, 180), Size(200, 600), Size(100, 100), Size(1, 1), Size(3840, 2160), Size(1080, 1920)]) {
        await useSize(tester, size);
        await tester.pumpWidget(app(const _Mosaic(focused: 12)));
        await tester.pump();
        expect(tester.takeException(), isNull, reason: '$size');
      }
    });

    testWidgets('the frame height follows the display height; the featured background survives a resize',
        (tester) async {
      final probe = _Probe();
      await useSize(tester, const Size(1280, 720));
      await tester.pumpWidget(app(_Mosaic(background: probe)));
      await tester.pump();
      final first = tester.state<_ProbeState>(find.byType(_Probe));
      expect(tester.getSize(find.byKey(MosaicFrameView.frameKey)).height, closeTo(720 * (1 - 2 * defaultMosaicSafeMargin), 1e-6));

      await useSize(tester, const Size(3840, 2160));
      await tester.pumpWidget(app(_Mosaic(background: probe)));
      await tester.pump();
      expect(tester.getSize(find.byKey(MosaicFrameView.frameKey)).height, closeTo(2160 * (1 - 2 * defaultMosaicSafeMargin), 1e-6));
      // The very same State object: only its layout box changed.
      expect(identical(tester.state<_ProbeState>(find.byType(_Probe)), first), isTrue);
      expect(first.disposed, isFalse);
    });

    testWidgets('text and focus ring scale with the display', (tester) async {
      await useSize(tester, const Size(1280, 720));
      await tester.pumpWidget(app(const _Mosaic(focused: 5)));
      await tester.pump();
      final small = tester.widget<Text>(find.text('Settings')).style!.fontSize!;
      await useSize(tester, const Size(3840, 2160));
      await tester.pumpWidget(app(const _Mosaic(focused: 5)));
      await tester.pump();
      final big = tester.widget<Text>(find.text('Settings')).style!.fontSize!;
      expect(big / small, closeTo(3.0, 1e-9)); // 2.0 vs 0.667
    });
  });
}

class _Probe extends StatefulWidget {
  @override
  State<_Probe> createState() => _ProbeState();
}

class _ProbeState extends State<_Probe> {
  bool disposed = false;
  @override
  void dispose() {
    disposed = true;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => const ColoredBox(color: Colors.teal);
}
