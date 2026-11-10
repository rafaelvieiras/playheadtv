// The phone's mosaic column. What can go wrong here is not the data (that is
// mosaic_tiles.dart, tested on its own): it is the card eating the screen, a
// focus ring that moves out of sight, or a tap opening a row that is not the one
// under the finger.
import 'package:client/ui/form_factor.dart';
import 'package:client/ui/ui_scale.dart';
import 'package:client/widgets/mosaic_frame.dart';
import 'package:client/widgets/phone_mosaic_view.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

List<MosaicTileSpec> specs(int channels) => [
      const MosaicTileSpec.channel(key: Key('mosaic-tile-c0'), number: 0, name: 'Featured', nowTitle: 'On now'),
      for (var i = 1; i < channels; i++)
        MosaicTileSpec.channel(key: Key('mosaic-tile-c$i'), number: i, name: 'Channel $i', nowTitle: 'Show $i'),
      const MosaicTileSpec.settings(),
    ];

Future<void> pumpView(
  WidgetTester tester, {
  required Size size,
  required List<MosaicTileSpec> tiles,
  int focused = 0,
  void Function(int)? onSelect,
}) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1.0;
  addTearDown(tester.view.reset);
  final focusNodes = [for (var i = 0; i < tiles.length; i++) FocusNode()];
  addTearDown(() {
    for (final n in focusNodes) {
      n.dispose();
    }
  });
  await tester.pumpWidget(
    MaterialApp(
      theme: ThemeData.dark(),
      builder: (context, child) => ClientFormFactorHost(child: UiScaleHost(child: child!)),
      home: Scaffold(
        backgroundColor: Colors.black,
        body: PhoneMosaicView(
          tiles: tiles,
          focusNodes: focusNodes,
          focusedIndex: focused,
          featuredBackground: const ColoredBox(color: Colors.indigo),
          onSelect: onSelect ?? (_) {},
        ),
      ),
    ),
  );
  await tester.pump();
}

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
    ClientFormFactorState.notifier.value = ClientFormFactor.phone;
  });
  tearDown(() => ClientFormFactorState.notifier.value = ClientFormFactor.tv);

  testWidgets('the featured card leaves room for the rows, in both orientations', (tester) async {
    for (final size in const [Size(360, 780), Size(780, 360)]) {
      await pumpView(tester, size: size, tiles: specs(20));
      final card = tester.getSize(find.byKey(const Key('mosaic-tile-c0')));
      expect(card.height, lessThanOrEqualTo(size.height * PhoneMosaicView.featuredMaxHeightFraction + 0.5),
          reason: '$size: the card never takes more than 40% of the height');
      expect(card.width, moreOrLessEquals(size.width), reason: '$size: it spans the column');
    }
  });

  testWidgets('every channel is in the list, not twelve', (tester) async {
    await pumpView(tester, size: const Size(360, 3000), tiles: specs(40));
    expect(find.byKey(const Key('mosaic-tile-c39')), findsOneWidget);
    expect(find.byKey(const Key('mosaic-tile-settings')), findsOneWidget);
  });

  testWidgets('a tap reports the row under the finger', (tester) async {
    final taps = <int>[];
    await pumpView(tester, size: const Size(360, 780), tiles: specs(6), onSelect: taps.add);
    await tester.tap(find.byKey(const Key('mosaic-tile-c3')));
    await tester.pump();
    expect(taps, [3]);
  });

  testWidgets('a tap on the featured card reports index 0', (tester) async {
    final taps = <int>[];
    await pumpView(tester, size: const Size(360, 780), tiles: specs(4), onSelect: taps.add);
    await tester.tap(find.byKey(const Key('mosaic-tile-c0')));
    await tester.pump();
    expect(taps, [0]);
  });

  testWidgets('the focused row has a focus ring and the others do not', (tester) async {
    await pumpView(tester, size: const Size(360, 780), tiles: specs(6), focused: 2);
    Decoration? decorationOf(int i) =>
        tester.widget<Container>(find.byKey(Key('mosaic-tile-c$i'))).decoration;
    Border borderOf(int i) => (decorationOf(i) as BoxDecoration).border! as Border;
    expect(borderOf(2).top.color, Colors.white);
    expect(borderOf(1).top.color, Colors.white24);
    expect(borderOf(2).top.width, greaterThan(borderOf(1).top.width));
  });

  testWidgets('moving the focus past the bottom scrolls the row into view', (tester) async {
    // The page moves the focus with the arrows; the column has to follow it, or
    // the ring walks off the screen and the viewer sees nothing move.
    final tiles = specs(24);
    final focusNodes = [for (var i = 0; i < tiles.length; i++) FocusNode()];
    addTearDown(() {
    for (final n in focusNodes) {
      n.dispose();
    }
  });
    tester.view.physicalSize = const Size(360, 640);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    Widget build(int focused) => MaterialApp(
          theme: ThemeData.dark(),
          builder: (context, child) => ClientFormFactorHost(child: UiScaleHost(child: child!)),
          home: Scaffold(
            body: PhoneMosaicView(
              tiles: tiles,
              focusNodes: focusNodes,
              focusedIndex: focused,
              featuredBackground: const ColoredBox(color: Colors.indigo),
              onSelect: (_) {},
            ),
          ),
        );
    await tester.pumpWidget(build(0));
    await tester.pump();
    final lastKey = const Key('mosaic-tile-c23');
    expect(tester.getRect(find.byKey(lastKey)).top, greaterThan(640), reason: 'below the fold to begin with');

    await tester.pumpWidget(build(23));
    await tester.pumpAndSettle();
    final rect = tester.getRect(find.byKey(lastKey));
    expect(rect.top, lessThan(640), reason: 'the list scrolled to it');
    expect(rect.bottom, greaterThan(0));
  });

  testWidgets('a row without a title line still lays out', (tester) async {
    await pumpView(
      tester,
      size: const Size(360, 780),
      tiles: const [
        MosaicTileSpec.channel(key: Key('mosaic-tile-c0'), number: 0, name: 'Featured'),
        MosaicTileSpec.channel(key: Key('mosaic-tile-c1'), number: 1, name: 'Quiet channel'),
        MosaicTileSpec.settings(),
      ],
    );
    expect(tester.takeException(), isNull);
    expect(find.text('Quiet channel'), findsOneWidget);
    expect(find.text('Settings'), findsOneWidget);
  });
}
