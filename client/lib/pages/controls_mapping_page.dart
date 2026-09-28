// "Remap controls" screen: lists every InputAction grouped by category, shows
// each one's current key (default or override) via
// KeyBindingsService.effectiveBindings, and lets you remap by tapping "Change"
// + pressing the new key (capture mode). "Restore defaults" clears all
// overrides at once.
//
// No binding-conflict detection and no reserved-key list — out of scope for
// now.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../input/input_action.dart';
import '../services/settings_service.dart';

class _ActionGroup {
  final String title;
  final List<InputAction> actions;
  const _ActionGroup(this.title, this.actions);
}

const _groups = [
  _ActionGroup('Navigation', [
    InputAction.channelPrevious,
    InputAction.channelNext,
    InputAction.digit0,
    InputAction.digit1,
    InputAction.digit2,
    InputAction.digit3,
    InputAction.digit4,
    InputAction.digit5,
    InputAction.digit6,
    InputAction.digit7,
    InputAction.digit8,
    InputAction.digit9,
  ]),
  _ActionGroup('Playback', [
    InputAction.select,
    InputAction.back,
  ]),
  _ActionGroup('General', [
    InputAction.home,
    InputAction.debugOverlay,
  ]),
];

String _actionLabel(InputAction action) {
  final digit = action.digitValue;
  if (digit != null) return 'Digit $digit';
  switch (action) {
    case InputAction.channelNext:
      return 'Next channel';
    case InputAction.channelPrevious:
      return 'Previous channel';
    case InputAction.select:
      return 'Select / Info / EPG';
    case InputAction.back:
      return 'Back';
    case InputAction.home:
      return 'Home';
    case InputAction.debugOverlay:
      return 'Debug overlay (FPS/CPU/video)';
    default:
      return action.name;
  }
}

String _keyLabel(LogicalKeyboardKey key) => key.debugName ?? key.keyLabel;

class ControlsMappingPage extends StatefulWidget {
  const ControlsMappingPage({super.key});

  @override
  State<ControlsMappingPage> createState() => _ControlsMappingPageState();
}

class _ControlsMappingPageState extends State<ControlsMappingPage> {
  SettingsService? _settings;
  KeyBindingsService? _keyBindings;
  Map<InputAction, LogicalKeyboardKey> _bindings = {};
  InputAction? _capturingAction;

  final FocusNode _pageFocusNode = FocusNode();

  @override
  void initState() {
    super.initState();
    _bootstrap();
  }

  Future<void> _bootstrap() async {
    final settings = await SettingsService.create();
    if (!mounted) return;
    setState(() {
      _settings = settings;
      _keyBindings = KeyBindingsService(settings);
      _bindings = _keyBindings!.effectiveBindings;
    });
  }

  void _startCapture(InputAction action) {
    setState(() => _capturingAction = action);
  }

  Future<void> _handleKeyEvent(KeyEvent event) async {
    final capturing = _capturingAction;
    if (capturing == null) return;
    if (event is! KeyDownEvent) return;
    await _keyBindings!.rebind(capturing, event.logicalKey);
    if (!mounted) return;
    setState(() {
      _bindings = _keyBindings!.effectiveBindings;
      _capturingAction = null;
    });
  }

  Future<void> _restoreDefaults() async {
    await _keyBindings!.restoreDefaults();
    if (!mounted) return;
    setState(() => _bindings = _keyBindings!.effectiveBindings);
  }

  @override
  void dispose() {
    _pageFocusNode.dispose();
    super.dispose();
  }

  Widget _buildActionTile(InputAction action) {
    final capturing = _capturingAction == action;
    return ListTile(
      title: Text(_actionLabel(action)),
      subtitle: capturing
          ? const Text('Press a key...', key: Key('capturing-label'))
          : Text(
              _keyLabel(_bindings[action]!),
              key: Key('binding-label-${action.name}'),
            ),
      trailing: ElevatedButton(
        key: Key('remap-button-${action.name}'),
        onPressed: () => _startCapture(action),
        child: const Text('Change'),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    if (_settings == null || _keyBindings == null) {
      return const Scaffold(
        key: Key('controls-mapping-page'),
        body: Center(child: CircularProgressIndicator()),
      );
    }

    return KeyboardListener(
      focusNode: _pageFocusNode,
      autofocus: true,
      onKeyEvent: (event) {
        unawaited(_handleKeyEvent(event));
      },
      child: Scaffold(
        key: const Key('controls-mapping-page'),
        appBar: AppBar(
          title: const Text('Remap controls'),
          actions: [
            TextButton(
              key: const Key('restore-defaults-button'),
              onPressed: _restoreDefaults,
              child: const Text('Restore defaults', style: TextStyle(color: Colors.white)),
            ),
          ],
        ),
        body: ListView(
          children: [
            for (final group in _groups) ...[
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 16, 16, 4),
                child: Text(
                  group.title,
                  style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 16),
                ),
              ),
              for (final action in group.actions) _buildActionTile(action),
            ],
          ],
        ),
      ),
    );
  }
}
