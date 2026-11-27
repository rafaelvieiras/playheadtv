import 'package:client/pages/settings_page.dart';
import 'package:client/services/app_window.dart';
import 'package:client/services/settings_service.dart';
import 'package:client/ui/ui_scale.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
    UiScaleOverride.notifier.value = null;
  });

  testWidgets('Select on "Interface size" cycles the choice, saves it and applies it', (tester) async {
    tester.view.physicalSize = const Size(1920, 1080);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(MaterialApp(
      builder: (context, child) => UiScaleHost(child: child!),
      home: const SettingsPage(),
    ));
    await tester.pumpAndSettle();

    String value() => tester.widget<Text>(find.byKey(const Key('settings-ui-scale-value'))).data!;
    expect(value(), 'Automatic');

    await tester.tap(find.byKey(const Key('settings-ui-scale-tile')));
    await tester.pumpAndSettle();
    expect(value(), '85%');
    expect(UiScaleOverride.notifier.value, 0.85);
    expect((await SettingsService.create()).uiScaleOverride, 0.85);

    // The factor in force follows the choice: 85% of what 1080p asks for (1.0).
    final context = tester.element(find.byType(SettingsPage));
    expect(UiScale.of(context), closeTo(0.85, 1e-9));

    for (var i = 0; i < uiScaleSteps.length; i++) {
      await tester.tap(find.byKey(const Key('settings-ui-scale-tile')));
      await tester.pumpAndSettle();
    }
    expect(value(), 'Automatic');
    expect(UiScaleOverride.notifier.value, isNull);
    expect((await SettingsService.create()).uiScaleOverride, isNull);
  });

  testWidgets('reachable with the six buttons: Down to the row, Select', (tester) async {
    await tester.pumpWidget(MaterialApp(
      builder: (context, child) => UiScaleHost(child: child!),
      home: const SettingsPage(),
    ));
    await tester.pumpAndSettle();
    // Down until the Interface size row has the focus. The count is the rows
    // between the autofocused one (Remap controls) and this row: adding a row
    // there means changing it, which is the point of a reachability test.
    final rows = FullScreenController.shared.supported ? 4 : 3;
    for (var i = 0; i < rows; i++) {
      await tester.sendKeyEvent(LogicalKeyboardKey.arrowDown);
    }
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pumpAndSettle();
    expect(tester.widget<Text>(find.byKey(const Key('settings-ui-scale-value'))).data, '85%');
  });
}
