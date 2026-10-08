// "Exit Playhead TV?": Cancel is focused; Back (via [isBack]) or Cancel closes
// it with false, Exit with true. A focus scope of its own, six buttons only.
// Scaled with the app-wide UiScale.
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../ui/ui_scale.dart';

class ExitDialog extends StatelessWidget {
  /// Whether a key is the (remappable) Back action.
  final bool Function(LogicalKeyboardKey key) isBack;

  const ExitDialog({super.key, required this.isBack});

  @override
  Widget build(BuildContext context) {
    return UiScaled(
      child: Focus(
        onKeyEvent: (node, event) {
          if (event is KeyDownEvent && isBack(event.logicalKey)) {
            Navigator.of(context).pop(false);
            return KeyEventResult.handled;
          }
          return KeyEventResult.ignored;
        },
        child: AlertDialog(
          key: const Key('exit-dialog'),
          title: const Text('Exit Playhead TV?'),
          actions: [
            TextButton(
              key: const Key('exit-cancel'),
              autofocus: true,
              onPressed: () => Navigator.of(context).pop(false),
              child: const Text('Cancel'),
            ),
            TextButton(
              key: const Key('exit-confirm'),
              onPressed: () => Navigator.of(context).pop(true),
              child: const Text('Exit'),
            ),
          ],
        ),
      ),
    );
  }
}
