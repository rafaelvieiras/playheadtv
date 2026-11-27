// The channel banner: the card a channel change puts in a corner. It has to be
// readable without the OSD, say the four things a viewer needs (channel, what is
// on, when it started and ends, what it is about), and sit in the corner the
// viewer chose.
import 'package:client/widgets/channel_banner.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  final now = DateTime.utc(2030, 1, 1, 21, 0);

  ChannelBannerData data({String? program, String? description, bool times = true}) => ChannelBannerData(
        number: 1001,
        name: 'Dizzy Channel',
        programTitle: program,
        description: description,
        startAt: times ? DateTime.utc(2030, 1, 1, 20, 0) : null,
        endAt: times ? DateTime.utc(2030, 1, 1, 21, 52) : null,
      );

  Future<void> pump(WidgetTester tester, ChannelBannerData d, {BannerCorner corner = BannerCorner.topLeft}) async {
    tester.view.physicalSize = const Size(1920, 1080);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: Stack(children: [ChannelBanner(data: d, corner: corner, now: () => now)]),
      ),
    ));
  }

  testWidgets('says the channel, the program, its span and its description', (tester) async {
    await pump(tester, data(program: 'Descendants', description: 'The teenage son of King Adam.'));
    expect(find.byKey(const Key('channel-banner')), findsOneWidget);
    expect(find.text('1001'), findsOneWidget);
    expect(find.text('DIZZY CHANNEL'), findsOneWidget);
    expect(find.text('Descendants'), findsOneWidget);
    expect(find.text('The teenage son of King Adam.'), findsOneWidget);
    // 20:00-21:52 in local time, with how long is left of it.
    final span = tester.widget<Text>(find.byKey(const Key('channel-banner-times'))).data!;
    expect(span, contains('52 min left'));
  });

  testWidgets('a channel whose guide is not known still announces itself', (tester) async {
    await pump(tester, data(times: false));
    expect(find.byKey(const Key('channel-banner')), findsOneWidget);
    expect(find.text('1001'), findsOneWidget);
    expect(find.byKey(const Key('channel-banner-program')), findsNothing);
    expect(find.byKey(const Key('channel-banner-times')), findsNothing);
  });

  testWidgets('it sits in the chosen corner', (tester) async {
    for (final corner in BannerCorner.values) {
      await pump(tester, data(program: 'Descendants'), corner: corner);
      final banner = tester.getRect(find.byKey(const Key('channel-banner')));
      final screen = tester.getRect(find.byType(Scaffold));
      switch (corner) {
        case BannerCorner.topLeft:
          expect(banner.left, lessThan(screen.width / 4));
          expect(banner.top, lessThan(screen.height / 4));
        case BannerCorner.topRight:
          expect(banner.right, greaterThan(screen.width * 3 / 4));
          expect(banner.top, lessThan(screen.height / 4));
        case BannerCorner.bottomLeft:
          expect(banner.left, lessThan(screen.width / 4));
          expect(banner.bottom, greaterThan(screen.height * 3 / 4));
        case BannerCorner.bottomRight:
          expect(banner.right, greaterThan(screen.width * 3 / 4));
          expect(banner.bottom, greaterThan(screen.height * 3 / 4));
      }
    }
  });

  testWidgets('it never takes a pointer or the focus', (tester) async {
    await pump(tester, data(program: 'Descendants'));
    expect(find.byType(IgnorePointer), findsWidgets);
    // No focusable widget inside the card.
    expect(
      find.descendant(of: find.byKey(const Key('channel-banner')), matching: find.byWidgetPredicate((w) => w is Focus)),
      findsNothing,
    );
  });
}
