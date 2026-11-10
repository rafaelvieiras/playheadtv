// Goldens of the phone layout, kept apart from the television ones on purpose:
// `ui_scale*_golden_test.dart` is a frozen baseline that nobody regenerates by
// accident, and `--update-goldens` on one file must not be able to touch the
// other's images. Every image below is drawn at devicePixelRatio 1 with the Ahem
// font, so they show layout and proportions, not glyph shapes.
//
// Slice 2 of docs/PHONE_UI_PLAN.md: the channel OSD. Regenerate with
// `flutter test --update-goldens test/phone_layout_golden_test.dart`.
import 'package:client/ui/form_factor.dart';
import 'package:client/ui/osd_metrics.dart';
import 'package:client/ui/ui_scale.dart';
import 'package:client/widgets/mosaic_frame.dart';
import 'package:client/widgets/osd_overlay.dart';
import 'package:client/widgets/phone_mosaic_view.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

const _sizes = [
  ('360x780', Size(360, 780)),
  ('430x932', Size(430, 932)),
  ('780x360', Size(780, 360)),
  ('932x430', Size(932, 430)),
];

Widget app(Widget home) => ClientFormFactorHost(
  child: MaterialApp(
    debugShowCheckedModeBanner: false,
    theme: ThemeData.dark(),
    builder: (context, child) => UiScaleHost(child: child!),
    home: home,
  ),
);

Widget osd({bool withChannels = true}) => Scaffold(
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
        onToggleDimmer: () {},
        onOpenDimmerLevel: () {},
        recentChannels: withChannels
            ? const [
                OsdChannelShortcut(id: 'a', number: 2, name: 'News'),
                OsdChannelShortcut(id: 'b', number: 9, name: 'Sports'),
                OsdChannelShortcut(id: 'c', number: 11, name: 'Kids'),
              ]
            : const [],
        onSelectChannel: (_) {},
        now: () => DateTime.utc(2030, 1, 1, 12, 0),
      ),
    ],
  ),
);

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
    UiScaleOverride.notifier.value = null;
    ClientFormFactorState.notifier.value = ClientFormFactor.phone;
  });
  tearDown(() => ClientFormFactorState.notifier.value = ClientFormFactor.tv);

  group('phone osd goldens', () {
    for (final (name, size) in _sizes) {
      testWidgets('phone_osd_$name', (tester) async {
        tester.view.physicalSize = size;
        tester.view.devicePixelRatio = 1.0;
        addTearDown(tester.view.reset);
        await tester.pumpWidget(app(osd()));
        await tester.pump();
        expect(tester.takeException(), isNull);
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/phone_osd_$name.png'));
        await tester.pumpWidget(const SizedBox());
      });
    }
  });

  group('phone mosaic goldens', () {
    // Slice 6: the mosaic is a column on a phone, not the television's ring.
    // Four channels plus Settings is enough to show the card and the rows.
    for (final (name, size) in [('360x780', const Size(360, 780)), ('780x360', const Size(780, 360))]) {
      testWidgets('phone_mosaic_$name', (tester) async {
        tester.view.physicalSize = size;
        tester.view.devicePixelRatio = 1.0;
        addTearDown(tester.view.reset);
        final tiles = [
          const MosaicTileSpec.channel(key: Key('mosaic-tile-c0'), number: 1, name: 'One', nowTitle: 'A Film'),
          for (var i = 2; i <= 4; i++)
            MosaicTileSpec.channel(key: Key('mosaic-tile-c$i'), number: i, name: 'Channel $i', nowTitle: 'On now $i'),
          const MosaicTileSpec.settings(),
        ];
        final focusNodes = [for (var i = 0; i < tiles.length; i++) FocusNode()];
        addTearDown(() {
          for (final n in focusNodes) {
            n.dispose();
          }
        });
        await tester.pumpWidget(
          app(
            Scaffold(
              backgroundColor: Colors.black,
              body: PhoneMosaicView(
                tiles: tiles,
                focusNodes: focusNodes,
                focusedIndex: 1,
                featuredBackground: const ColoredBox(color: Color(0xFF223344)),
                onSelect: (_) {},
              ),
            ),
          ),
        );
        await tester.pump();
        expect(tester.takeException(), isNull);
        await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/phone_mosaic_$name.png'));
        await tester.pumpWidget(const SizedBox());
      });
    }
  });

  group('phone osd shape', () {
    for (final (size, maxPanelShare) in const [
      (Size(360, 780), 0.25),
      (Size(430, 932), 0.25),
      (Size(780, 360), 0.45),
      (Size(932, 430), 0.45),
    ]) {
      testWidgets('the panel leaves the picture visible at $size', (tester) async {
        tester.view.physicalSize = size;
        tester.view.devicePixelRatio = 1.0;
        addTearDown(tester.view.reset);
        await tester.pumpWidget(app(osd()));
        await tester.pump();
        final panelTop = tester.getTopLeft(find.byKey(const Key('osd-item-title'))).dy;
        final share = (size.height - panelTop) / size.height;
        expect(share, lessThanOrEqualTo(maxPanelShare), reason: 'measured $share');
        // The panel is anchored to the bottom edge, and the clock stays clear.
        expect(tester.getRect(find.byKey(const Key('osd-progress'))).bottom, lessThanOrEqualTo(size.height));
        final clock = tester.getRect(find.byKey(const Key('osd-clock')));
        expect(clock.bottom, lessThan(panelTop), reason: 'clock above the panel');
        await tester.pumpWidget(const SizedBox());
      });
    }

    testWidgets('a cutout pushes the OSD out of its way', (tester) async {
      // `viewPadding`, not `padding`: the player is immersive, so the bars are
      // hidden while the notch and the gesture handle are still there.
      tester.view.physicalSize = const Size(780, 360);
      tester.view.devicePixelRatio = 1.0;
      tester.view.viewPadding = const FakeViewPadding(top: 24, bottom: 12, left: 30, right: 6);
      addTearDown(tester.view.reset);
      await tester.pumpWidget(app(osd()));
      await tester.pump();
      final scale = resolveUiScale(const Size(780, 360), formFactor: ClientFormFactor.phone);
      final m = OsdMetrics.of(ClientFormFactor.phone);
      final clock = tester.getRect(find.byKey(const Key('osd-clock')));
      expect(clock.top, closeTo(m.clockTop * scale + 24, 0.5), reason: 'no clock under the notch');
      expect(clock.right, closeTo(780 - 6 - m.clockRight * scale, 0.5), reason: 'no clock under the right cutout');
      final panel = tester.getRect(find.byKey(const Key('osd-progress')));
      // The gesture handle sits 12 dp off the bottom; the progress bar clears it.
      expect(panel.bottom, lessThanOrEqualTo(360 - 12 + 0.5));
      await tester.pumpWidget(const SizedBox());
    });

    testWidgets('every control is a touch target and every glyph is legible', (tester) async {
      tester.view.physicalSize = const Size(360, 780);
      tester.view.devicePixelRatio = 1.0;
      addTearDown(tester.view.reset);
      await tester.pumpWidget(app(osd()));
      await tester.pump();
      // The dimmer toggle is always present; it is the reference control.
      final dimmer = tester.getSize(find.byKey(const Key('osd-btn-dimmer')));
      expect(dimmer.height, greaterThanOrEqualTo(kMinInteractiveDimension));
      expect(dimmer.width, greaterThanOrEqualTo(kMinInteractiveDimension));
      // Text at the phone's ~0.86 scale: the television's 11 px chip resolved to
      // 6.6 dp, which is what the phone metrics exist to fix.
      final texts = find.descendant(of: find.byKey(const Key('osd')), matching: find.byType(Text));
      expect(texts, findsWidgets);
      for (final element in texts.evaluate()) {
        final size = (element.widget as Text).style?.fontSize;
        if (size == null) continue;
        expect(size, greaterThanOrEqualTo(12), reason: 'a phone must draw no text below 12 dp');
      }
      await tester.pumpWidget(const SizedBox());
    });

    testWidgets('the dead transport row and the channel strip are gone', (tester) async {
      tester.view.physicalSize = const Size(360, 780);
      tester.view.devicePixelRatio = 1.0;
      addTearDown(tester.view.reset);
      await tester.pumpWidget(app(osd()));
      await tester.pump();
      expect(find.byKey(const Key('osd-btn-play-pause')), findsNothing);
      expect(find.byKey(const Key('osd-channels-title')), findsNothing);
      expect(find.byKey(const Key('osd-recent-a')), findsNothing);
      await tester.pumpWidget(const SizedBox());
    });

    testWidgets('the television keeps both, unchanged', (tester) async {
      SharedPreferences.setMockInitialValues({});
      ClientFormFactorState.notifier.value = ClientFormFactor.tv;
      tester.view.physicalSize = const Size(1920, 1080);
      tester.view.devicePixelRatio = 1.0;
      addTearDown(tester.view.reset);
      await tester.pumpWidget(app(osd()));
      await tester.pump();
      expect(find.byKey(const Key('osd-btn-play-pause')), findsOneWidget);
      expect(find.byKey(const Key('osd-channels-title')), findsOneWidget);
      // Exactly today's button box at scale 1: 2 px of border, 8 px of padding
      // and a 28 px icon.
      final dimmer = tester.getSize(find.byKey(const Key('osd-btn-dimmer')));
      expect(dimmer.height, 2 * (2 + 8) + 28);
      await tester.pumpWidget(const SizedBox());
    });
  });

  group('OsdMetrics', () {
    test('tv is the layout the widget used before the phone existed', () {
      const m = OsdMetrics.tv;
      expect(m.chipFontSize, 11);
      expect(m.buttonPaddingH, 10);
      expect(m.buttonPaddingHLabel, 12);
      expect(m.buttonPaddingV, 8);
      expect(m.buttonBorder, 2);
      expect(m.buttonIconSize, 28);
      expect(m.buttonGap, 8);
      expect(m.buttonLabelSize, 16);
      expect(m.minTapTarget, 0);
      expect(m.logoSize, 64);
      expect(m.logoIconSize, 32);
      expect(m.clockTop, 24);
      expect(m.clockRight, 32);
      expect(m.clockFontSize, 28);
      expect(m.endsAtFontSize, 14);
      expect(m.panelLeft, 32);
      expect(m.panelTop, 48);
      expect(m.panelRight, 32);
      expect(m.panelBottom, 20);
      expect(m.titleFontSize, 26);
      expect(m.titleGap, 4);
      expect(m.metaFontSize, 15);
      expect(m.transportRow, isTrue);
      expect(m.recentChannelCards, isTrue);
    });

    test('the phone drops the rows a remote needs and floors its targets', () {
      const m = OsdMetrics.phone;
      expect(m.transportRow, isFalse);
      expect(m.recentChannelCards, isFalse);
      expect(m.minTapTarget, kMinInteractiveDimension);
      // Legibility at the phone's ~0.86 scale: nothing a phone draws below 12 dp
      // (the card sizes are excluded: a phone draws no cards).
      for (final size in [m.chipFontSize, m.buttonLabelSize, m.metaFontSize, m.endsAtFontSize]) {
        expect(size * phoneUiScaleMin, greaterThanOrEqualTo(12));
      }
    });

    test('the phone panel fits inside the OSD budget', () {
      // uiScaleFitHeightPhone is what a fixed "Interface size" on a phone is
      // capped by, so the panel plus the clock has to stay inside it.
      const m = OsdMetrics.phone;
      final height = m.panelTop + m.panelBottom + m.clockTop + m.clockFontSize;
      expect(height, lessThanOrEqualTo(uiScaleFitHeightPhone));
    });

    test('of() picks the family', () {
      expect(OsdMetrics.of(ClientFormFactor.tv), same(OsdMetrics.tv));
      expect(OsdMetrics.of(ClientFormFactor.phone), same(OsdMetrics.phone));
    });
  });
}
