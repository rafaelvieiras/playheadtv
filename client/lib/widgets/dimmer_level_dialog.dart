// Dialog to choose the global dim level. Six buttons only: Left/Down lower the
// dim, Right/Up raise it (raw arrows, like every menu), Select confirms, Back
// cancels. [onChanged] is called on every step so the caller can preview the
// level live. Sizes follow the app-wide UiScale.
//
// Touch: the two step buttons and the Cancel/Save pair are the same four
// outcomes the keys give, so a phone that reached this dialog by tapping the
// dimmer's level chip can also leave it. They sit inside `ExcludeFocus`: the
// dialog's own `Focus` owns every key, and a button that could take the focus
// would swallow Select and turn it into "click this button".
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../dimmer.dart';
import '../input/input_action.dart';
import '../ui/ui_scale.dart';

/// Shows the dialog; resolves to the confirmed level, or null when cancelled.
Future<int?> showDimmerLevelDialog(
  BuildContext context, {
  required int initial,
  required InputAction? Function(LogicalKeyboardKey key) resolve,
  void Function(int percent)? onChanged,
}) {
  return showDialog<int>(
    context: context,
    builder: (_) => DimmerLevelDialog(initial: initial, resolve: resolve, onChanged: onChanged),
  );
}

class DimmerLevelDialog extends StatefulWidget {
  final int initial;
  final InputAction? Function(LogicalKeyboardKey key) resolve;
  final void Function(int percent)? onChanged;

  const DimmerLevelDialog({super.key, required this.initial, required this.resolve, this.onChanged});

  @override
  State<DimmerLevelDialog> createState() => _DimmerLevelDialogState();
}

class _DimmerLevelDialogState extends State<DimmerLevelDialog> {
  late int _level = sanitizeDimLevelPercent(widget.initial);

  void _close([int? result]) => Navigator.of(context).pop(result);

  void _step(int steps) {
    final next = stepDimLevel(_level, steps);
    if (next == _level) return;
    setState(() => _level = next);
    widget.onChanged?.call(next);
  }

  KeyEventResult _onKey(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent) return KeyEventResult.ignored;
    final key = event.logicalKey;
    final action = widget.resolve(key);
    if (action == InputAction.back) {
      Navigator.of(context).pop();
    } else if (action == InputAction.select) {
      Navigator.of(context).pop(_level);
    } else if (key == LogicalKeyboardKey.arrowLeft || key == LogicalKeyboardKey.arrowDown) {
      _step(-1);
    } else if (key == LogicalKeyboardKey.arrowRight || key == LogicalKeyboardKey.arrowUp) {
      _step(1);
    } else {
      return KeyEventResult.ignored;
    }
    return KeyEventResult.handled;
  }

  @override
  Widget build(BuildContext context) {
    final range = maxDimLevelPercent - minDimLevelPercent;
    return UiScaled(
      child: Builder(
        builder: (context) => Focus(
          autofocus: true,
          onKeyEvent: _onKey,
          child: AlertDialog(
            key: const Key('dimmer-dialog'),
            title: const Text('Dim level'),
            actions: [
              ExcludeFocus(
                child: Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    TextButton(
                      key: const Key('dimmer-dialog-cancel'),
                      onPressed: () => _close(),
                      child: const Text('Cancel'),
                    ),
                    TextButton(
                      key: const Key('dimmer-dialog-save'),
                      onPressed: () => _close(_level),
                      child: const Text('Save'),
                    ),
                  ],
                ),
              ),
            ],
            // Scrollable: a phone held sideways is 360 dp tall and this content
            // (value, bar, preview, two button rows) is taller than that. The
            // dialog is then usable at any window height instead of overflowing.
            content: SingleChildScrollView(
              child: SizedBox(
              width: context.ui(420),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  // The steps sit beside the value, not below it: a phone held
                  // sideways has 360 dp of height, and a second button row
                  // pushed Save below the fold.
                  Row(
                    mainAxisAlignment: MainAxisAlignment.center,
                    children: [
                      ExcludeFocus(
                        child: TextButton(
                          key: const Key('dimmer-dialog-down'),
                          onPressed: () => _step(-1),
                          child: const Text('−'),
                        ),
                      ),
                      SizedBox(width: context.ui(16)),
                      Text(
                        dimLevelLabel(_level),
                        key: const Key('dimmer-dialog-value'),
                        style: TextStyle(fontSize: 32, fontWeight: FontWeight.bold),
                      ),
                      SizedBox(width: context.ui(16)),
                      ExcludeFocus(
                        child: TextButton(
                          key: const Key('dimmer-dialog-up'),
                          onPressed: () => _step(1),
                          child: const Text('+'),
                        ),
                      ),
                    ],
                  ),
                  SizedBox(height: context.ui(12)),
                  LinearProgressIndicator(
                    value: (_level - minDimLevelPercent) / range,
                    minHeight: context.ui(8),
                  ),
                  SizedBox(height: context.ui(16)),
                  // A sample of what the level does to a bright picture.
                  Container(
                    key: const Key('dimmer-dialog-preview'),
                    height: context.ui(60),
                    color: Colors.white,
                    foregroundDecoration: BoxDecoration(color: Color.fromRGBO(0, 0, 0, _level / 100)),
                  ),
                  SizedBox(height: context.ui(12)),
                  Text('Left / Right or -/+ to adjust, Select to save, Back to cancel',
                      style: TextStyle(fontSize: 14)),
                ],
              ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}
