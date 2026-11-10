import 'package:client/ui/form_factor.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  const phone = Size(360, 780);
  const landscapePhone = Size(780, 360);

  group('resolveFormFactor', () {
    test('a handheld is a phone in both orientations', () {
      // Rotation changes width and height, never the shortest side: the answer
      // must not depend on which way the device is held.
      expect(resolveFormFactor(size: phone, isTelevision: false, isMobilePlatform: true), ClientFormFactor.phone);
      expect(
        resolveFormFactor(size: landscapePhone, isTelevision: false, isMobilePlatform: true),
        ClientFormFactor.phone,
      );
      expect(
        resolveFormFactor(size: const Size(430, 932), isTelevision: false, isMobilePlatform: true),
        ClientFormFactor.phone,
      );
    });

    test('a tablet is not a phone', () {
      expect(
        resolveFormFactor(size: const Size(800, 1280), isTelevision: false, isMobilePlatform: true),
        ClientFormFactor.tv,
      );
    });

    test('the shortest-side boundary is exclusive', () {
      expect(
        resolveFormFactor(size: const Size(599, 900), isTelevision: false, isMobilePlatform: true),
        ClientFormFactor.phone,
      );
      expect(
        resolveFormFactor(size: const Size(600, 900), isTelevision: false, isMobilePlatform: true),
        ClientFormFactor.tv,
      );
    });

    test('a television stays a television at any size', () {
      // A 720p box has a shortest side of 720, but the television flag decides
      // first, and a small TV window must not turn into a phone layout.
      expect(resolveFormFactor(size: const Size(1280, 720), isTelevision: true, isMobilePlatform: true),
          ClientFormFactor.tv);
      expect(resolveFormFactor(size: phone, isTelevision: true, isMobilePlatform: true), ClientFormFactor.tv);
    });

    test('a desktop is always a television, however small the window', () {
      // These are the sizes the existing desktop suites and goldens use: they
      // must keep the 10-foot layout.
      for (final size in const [Size(1920, 1080), Size(640, 360), Size(800, 450), Size(320, 180), Size(600, 1000)]) {
        expect(resolveFormFactor(size: size, isTelevision: false, isMobilePlatform: false), ClientFormFactor.tv,
            reason: '$size');
      }
    });

    test('an unusable size falls back to the television layout', () {
      expect(resolveFormFactor(size: Size.zero, isTelevision: false, isMobilePlatform: true), ClientFormFactor.tv);
      expect(
        resolveFormFactor(size: const Size(double.infinity, double.infinity), isTelevision: false, isMobilePlatform: true),
        ClientFormFactor.tv,
      );
    });
  });

  group('ClientFormFactorHost', () {
    tearDown(() => ClientFormFactorState.notifier.value = ClientFormFactor.tv);

    testWidgets('a bare widget test sees a television', (tester) async {
      late ClientFormFactor seen;
      await tester.pumpWidget(MaterialApp(
        home: Builder(builder: (context) {
          seen = context.formFactor;
          return const SizedBox();
        }),
      ));
      expect(seen, ClientFormFactor.tv);
    });

    testWidgets('provides the resolved form factor to everything below it', (tester) async {
      await tester.pumpWidget(const ClientFormFactorHost(child: MaterialApp(home: _Probe())));
      expect(find.text('tv'), findsOneWidget);

      ClientFormFactorState.notifier.value = ClientFormFactor.phone;
      await tester.pump();
      expect(find.text('phone'), findsOneWidget);
    });

    testWidgets('a widget that does not ask is not rebuilt', (tester) async {
      var builds = 0;
      await tester.pumpWidget(ClientFormFactorHost(
        child: MaterialApp(
          home: Builder(builder: (context) {
            builds++;
            return const SizedBox();
          }),
        ),
      ));
      expect(builds, 1);
      ClientFormFactorState.notifier.value = ClientFormFactor.phone;
      await tester.pump();
      // The Navigator is handed through as `child:`, so the page below does not
      // rebuild — the same discipline UiScaleHost uses, and what keeps a video
      // surface out of a rebuild.
      expect(builds, 1);
    });
  });
}

class _Probe extends StatelessWidget {
  const _Probe();

  @override
  Widget build(BuildContext context) => Text(context.isPhone ? 'phone' : 'tv');
}
